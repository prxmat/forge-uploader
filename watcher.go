package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	scanEvery = 3 * time.Second
	// Un log est envoyé quand sa taille n'a plus bougé pendant ce temps (arcdps l'écrit puis le compresse).
	settleFor = 4 * time.Second
	// Au démarrage, les logs plus vieux que ça sont considérés déjà envoyés.
	recentWindow = 10 * time.Minute
)

type uploadResponse struct {
	Permalink string `json:"permalink"`
	Error     string `json:"error"`
	Encounter struct {
		Boss    string `json:"boss"`
		Success bool   `json:"success"`
	} `json:"encounter"`
}

type intakeResponse struct {
	Stored       bool   `json:"stored"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Fights       int    `json:"fights"`
	Reason       string `json:"reason"`
	Roster       string `json:"roster"`
	Encounter    string `json:"encounter"`
	Success      bool   `json:"success"`
	Known        bool   `json:"known"`
	LiveAdvanced bool   `json:"liveAdvanced"`
	RaidDate     string `json:"date"`
	Error        string `json:"error"`
}

type sizeAt struct {
	size int64
	at   time.Time
}

// Scrute le dossier des logs et envoie chaque nouveau fichier une fois qu'il est fini d'écrire.
func (a *app) watch() {
	defer func() {
		if r := recover(); r != nil {
			a.mu.Lock()
			cfg := a.cfg
			a.mu.Unlock()
			reportError(&cfg, "panique dans la surveillance des logs", map[string]any{"panic": fmt.Sprint(r)})
			panic(r)
		}
	}()
	seen := loadSeen(a.dir)
	sizes := map[string]sizeAt{}
	since := time.Now().Add(-recentWindow)
	warned := ""
	for {
		a.mu.Lock()
		cfg := a.cfg
		a.mu.Unlock()
		if _, err := os.Stat(cfg.LogsDir); err != nil {
			if warned != cfg.LogsDir {
				a.say("Dossier des logs introuvable : %s", cfg.LogsDir)
				warned = cfg.LogsDir
			}
			time.Sleep(scanEvery)
			continue
		}
		for _, path := range findLogs(cfg.LogsDir, since) {
			if seen[path] {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			last, known := sizes[path]
			if !known || last.size != info.Size() {
				sizes[path] = sizeAt{size: info.Size(), at: time.Now()}
				continue
			}
			if time.Since(last.at) < settleFor {
				continue
			}
			// Un .evtc dont le .zevtc compressé existe : arcdps garde le second, c'est lui qui part.
			if strings.HasSuffix(path, ".evtc") && hasCompressedTwin(path) {
				seen[path] = true
				continue
			}
			seen[path] = true
			saveSeen(a.dir, seen)
			delete(sizes, path)
			a.handle(cfg, path)
		}
		time.Sleep(scanEvery)
	}
}

func hasCompressedTwin(path string) bool {
	if _, err := os.Stat(path + ".zevtc"); err == nil {
		return true
	}
	_, err := os.Stat(strings.TrimSuffix(path, ".evtc") + ".zevtc")
	return err == nil
}

func (a *app) handle(cfg config, path string) {
	name := filepath.Base(path)
	e := entry{At: time.Now(), File: name, Status: "uploading"}
	a.push(e)
	a.say("Envoi de %s sur dps.report…", name)
	up, err := uploadToDpsReport(&cfg, path)
	if err != nil {
		a.say("  dps.report a refusé %s : %v", name, err)
		e.Status, e.Where = "error", "dps.report : "+err.Error()
		a.push(e)
		reportError(&cfg, "dps.report a refusé un log : "+err.Error(), map[string]any{"file": name})
		return
	}
	e.Boss, e.Success, e.Permalink, e.Status = up.Encounter.Boss, up.Encounter.Success, up.Permalink, "forge"
	a.push(e)
	a.say("  %s → %s", up.Encounter.Boss, up.Permalink)
	in, err := sendToForge(&cfg, up.Permalink)
	if err != nil {
		a.say("  Forge n’a pas rangé le log : %v", err)
		e.Status, e.Where = "error", "Forge : "+err.Error()
		a.push(e)
		reportError(&cfg, "Forge n’a pas rangé un log : "+err.Error(), map[string]any{"file": name, "permalink": up.Permalink})
		return
	}
	if !in.Stored {
		a.say("  Forge : %s", in.Reason)
		e.Status, e.Where = "rejected", in.Reason
		a.push(e)
		return
	}
	e.Status, e.Kind = "done", in.Kind
	switch in.Kind {
	case "wvw":
		e.Where = fmt.Sprintf("%s · %d combat(s)", in.Title, in.Fights)
	default:
		e.Where = in.Roster + " · soirée du " + in.RaidDate
		if in.LiveAdvanced {
			e.Where += " · le direct passe au boss suivant"
		}
	}
	if in.Known {
		e.Where += " (déjà connu)"
	}
	a.say("  Rangé : %s", e.Where)
	a.push(e)
}

func uploadToDpsReport(cfg *config, path string) (*uploadResponse, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	writer.Close()
	query := url.Values{"json": {"1"}, "generator": {"ei"}}
	if cfg.DetailedWvw {
		query.Set("detailedwvw", "true")
	}
	if cfg.DpsReportUserToken != "" {
		query.Set("userToken", cfg.DpsReportUserToken)
	}
	request, err := http.NewRequest(http.MethodPost, dpsReportBase+"/uploadContent?"+query.Encode(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("User-Agent", "forge-uploader/"+version)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload uploadResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("réponse illisible (%s)", response.Status)
	}
	if payload.Error != "" {
		return nil, errors.New(payload.Error)
	}
	if payload.Permalink == "" {
		return nil, fmt.Errorf("pas de lien dans la réponse (%s)", response.Status)
	}
	return &payload, nil
}

func sendToForge(cfg *config, permalink string) (*intakeResponse, error) {
	body, _ := json.Marshal(map[string]string{"permalink": permalink})
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.ForgeURL, "/")+"/api/uploads", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("User-Agent", "forge-uploader/"+version)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload intakeResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("réponse illisible (%s)", response.Status)
	}
	if payload.Error != "" {
		return nil, errors.New(payload.Error)
	}
	return &payload, nil
}

func checkToken(cfg *config) (string, error) {
	if cfg.Token == "" {
		return "", errors.New("aucun token")
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(cfg.ForgeURL, "/")+"/api/uploads", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("User-Agent", "forge-uploader/"+version)
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		Member string `json:"member"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("Forge ne répond pas (%s)", response.Status)
	}
	if payload.Error != "" {
		return "", errors.New(payload.Error)
	}
	return payload.Member, nil
}

// Les logs récents du dossier arcdps (un sous-dossier par boss ou par personnage, à toute profondeur).
func findLogs(root string, since time.Time) []string {
	var found []string
	filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, err error) error {
		if err != nil || dirEntry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".zevtc") && !strings.HasSuffix(path, ".evtc") {
			return nil
		}
		info, err := dirEntry.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		found = append(found, path)
		return nil
	})
	return found
}

func loadSeen(dir string) map[string]bool {
	seen := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(dir, "seen.json"))
	if err == nil {
		var list []string
		if json.Unmarshal(data, &list) == nil {
			for _, path := range list {
				seen[path] = true
			}
		}
	}
	return seen
}

func saveSeen(dir string, seen map[string]bool) {
	list := make([]string, 0, len(seen))
	for path := range seen {
		list = append(list, path)
	}
	data, _ := json.Marshal(list)
	os.WriteFile(filepath.Join(dir, "seen.json"), data, 0o644)
}

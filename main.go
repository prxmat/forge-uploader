// Forge Uploader : surveille les logs arcdps, les envoie sur dps.report puis à Forge (Le Bus Magique).
// Un seul binaire, sans dépendance. Voir README.md.
package main

import (
	"bufio"
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
	version       = "0.1.1"
	defaultForge  = "https://forge-lbm.vercel.app"
	dpsReportBase = "https://dps.report"
	scanEvery     = 3 * time.Second
	// Un log est envoyé quand sa taille n'a plus bougé pendant ce temps (arcdps l'écrit puis le compresse).
	settleFor = 4 * time.Second
	// Au démarrage, les logs plus vieux que ça sont considérés déjà envoyés.
	recentWindow = 10 * time.Minute
)

type config struct {
	ForgeURL           string `json:"forge_url"`
	Token              string `json:"token"`
	LogsDir            string `json:"logs_dir"`
	DetailedWvw        bool   `json:"detailed_wvw"`
	DpsReportUserToken string `json:"dps_report_user_token,omitempty"`
}

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

var client = &http.Client{Timeout: 3 * time.Minute}

func main() {
	fmt.Printf("Forge Uploader %s — Le Bus Magique\n", version)
	dir, err := configDir()
	if err != nil {
		fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		fatal(err)
	}
	logFile, _ := os.OpenFile(filepath.Join(dir, "forge-uploader.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if logFile != nil {
		defer logFile.Close()
	}
	say := func(format string, args ...any) {
		line := fmt.Sprintf(time.Now().Format("15:04:05")+"  "+format+"\n", args...)
		fmt.Print(line)
		if logFile != nil {
			logFile.WriteString(line)
		}
	}

	for {
		name, err := checkToken(cfg)
		if err == nil {
			say("Connecté à Forge : %s", name)
			break
		}
		say("Token Forge refusé : %v", err)
		cfg.Token = ask("Colle ton token Forge Uploader (Mon suivi → Réglages) : ")
		if err := saveConfig(dir, cfg); err != nil {
			fatal(err)
		}
	}
	if _, err := os.Stat(cfg.LogsDir); err != nil {
		say("Dossier des logs introuvable : %s", cfg.LogsDir)
		cfg.LogsDir = ask("Chemin du dossier arcdps.cbtlogs : ")
		if err := saveConfig(dir, cfg); err != nil {
			fatal(err)
		}
	}
	say("Surveille %s (laisse cette fenêtre ouverte pendant la soirée)", cfg.LogsDir)

	seen := loadSeen(dir)
	sizes := map[string]sizeAt{}
	since := time.Now().Add(-recentWindow)
	for {
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
			if strings.HasSuffix(path, ".evtc") {
				if _, err := os.Stat(path + ".zevtc"); err == nil {
					seen[path] = true
					continue
				}
				if _, err := os.Stat(strings.TrimSuffix(path, ".evtc") + ".zevtc"); err == nil {
					seen[path] = true
					continue
				}
			}
			seen[path] = true
			saveSeen(dir, seen)
			delete(sizes, path)
			handle(cfg, path, say)
		}
		time.Sleep(scanEvery)
	}
}

type sizeAt struct {
	size int64
	at   time.Time
}

func handle(cfg *config, path string, say func(string, ...any)) {
	name := filepath.Base(path)
	say("Envoi de %s sur dps.report…", name)
	up, err := uploadToDpsReport(cfg, path)
	if err != nil {
		say("  dps.report a refusé %s : %v", name, err)
		return
	}
	result := "wipe"
	if up.Encounter.Success {
		result = "kill"
	}
	say("  %s — %s → %s", up.Encounter.Boss, result, up.Permalink)
	in, err := sendToForge(cfg, up.Permalink)
	if err != nil {
		say("  Forge n’a pas rangé le log : %v (il reste sur dps.report)", err)
		return
	}
	if !in.Stored {
		say("  Forge : %s", in.Reason)
		return
	}
	if in.Kind == "wvw" {
		known := ""
		if in.Known {
			known = " (déjà connu)"
		}
		say("  McM → %s, %d combat(s)%s", in.Title, in.Fights, known)
		return
	}
	extra := ""
	if in.Known {
		extra = " (déjà connu)"
	}
	if in.LiveAdvanced {
		extra += " · le direct passe au boss suivant"
	}
	say("  Rangé dans %s, soirée du %s%s", in.Roster, in.RaidDate, extra)
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
		query.Set("detailedwvw", "1")
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

// Les logs récents du dossier arcdps (un sous-dossier par boss).
func findLogs(root string, since time.Time) []string {
	var found []string
	filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".zevtc") && !strings.HasSuffix(path, ".evtc") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		found = append(found, path)
		return nil
	})
	return found
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "ForgeUploader")
	return dir, os.MkdirAll(dir, 0o755)
}

func loadConfig(dir string) (*config, error) {
	cfg := &config{ForgeURL: defaultForge, DetailedWvw: true, LogsDir: defaultLogsDir()}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config.json illisible : %w", err)
		}
	}
	if cfg.ForgeURL == "" {
		cfg.ForgeURL = defaultForge
	}
	if cfg.LogsDir == "" {
		cfg.LogsDir = defaultLogsDir()
	}
	return cfg, saveConfig(dir, cfg)
}

func saveConfig(dir string, cfg *config) error {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

func defaultLogsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", "Guild Wars 2", "addons", "arcdps", "arcdps.cbtlogs")
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

func ask(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func fatal(err error) {
	fmt.Println("Erreur :", err)
	fmt.Println("Appuie sur Entrée pour fermer.")
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

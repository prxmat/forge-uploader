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
	// Au démarrage, les logs plus vieux que ça sont considérés déjà envoyés… sauf ceux écrits depuis la dernière
	// exécution (l'uploader lancé après le premier boss rattrape le début de soirée).
	recentWindow = 10 * time.Minute
	catchUpMax   = 36 * time.Hour
	// Rattrapage à la demande : les logs des dernières heures jamais envoyés.
	catchUpWindow = 8 * time.Hour
	maxAttempts   = 6
)

// Délai avant la tentative suivante quand dps.report ou Forge n'ont pas répondu.
var retryAfter = []time.Duration{1 * time.Minute, 3 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 60 * time.Minute}

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

// Scrute le dossier des logs et envoie chaque nouveau fichier une fois qu'il est fini d'écrire ; rejoue les
// envois qui ont échoué, de plus en plus espacés.
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
	sizes := map[string]sizeAt{}
	since := time.Now().Add(-recentWindow)
	if last := a.lastRun(); !last.IsZero() && time.Since(last) < catchUpMax && last.Before(since) {
		since = last.Add(-time.Minute)
		a.say("Reprise : les logs écrits depuis %s seront envoyés.", last.Format("02/01 15:04"))
	}
	warned := ""
	lastSaved := time.Time{}
	for {
		a.mu.Lock()
		cfg := a.cfg
		a.mu.Unlock()
		if time.Since(lastSaved) > time.Minute {
			a.saveLastRun()
			lastSaved = time.Now()
		}
		if _, err := os.Stat(cfg.LogsDir); err != nil {
			if warned != cfg.LogsDir {
				a.say("Dossier des logs introuvable : %s", cfg.LogsDir)
				warned = cfg.LogsDir
			}
			time.Sleep(scanEvery)
			continue
		}
		for _, path := range findLogs(cfg.LogsDir, since) {
			if a.isSeen(path) {
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
				a.markSeen(path)
				continue
			}
			a.markSeen(path)
			delete(sizes, path)
			a.handle(cfg, path, 1)
		}
		a.retryDue(cfg)
		time.Sleep(scanEvery)
	}
}

// Les envois en attente d'une nouvelle tentative dont l'heure est venue.
func (a *app) retryDue(cfg config) {
	a.mu.Lock()
	var due []entry
	for _, e := range a.history {
		if e.Status == "retry" && e.Path != "" && !e.NextTry.After(time.Now()) {
			due = append(due, e)
		}
	}
	a.mu.Unlock()
	for _, e := range due {
		if _, err := os.Stat(e.Path); err != nil {
			e.Status, e.Where = "error", "fichier introuvable : "+e.Path
			a.push(e)
			continue
		}
		a.handle(cfg, e.Path, e.Attempts+1)
	}
}

// Renvoie un log à la main (bouton de la fenêtre), quel que soit son état.
func (a *app) resend(file string) {
	a.mu.Lock()
	cfg := a.cfg
	var found *entry
	for index := range a.history {
		if a.history[index].File == file {
			e := a.history[index]
			found = &e
			break
		}
	}
	a.mu.Unlock()
	if found == nil || found.Path == "" {
		a.say("Renvoi impossible : chemin inconnu pour %s", file)
		return
	}
	go a.handle(cfg, found.Path, 1)
}

// Envoie les logs des dernières heures jamais envoyés (fenêtre « Rattraper »).
func (a *app) catchUp() {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	var todo []string
	for _, path := range findLogs(cfg.LogsDir, time.Now().Add(-catchUpWindow)) {
		if a.isSeen(path) || (strings.HasSuffix(path, ".evtc") && hasCompressedTwin(path)) {
			continue
		}
		todo = append(todo, path)
	}
	if len(todo) == 0 {
		a.say("Rattrapage : rien à envoyer, tous les logs des %d dernières heures sont passés.", int(catchUpWindow.Hours()))
		return
	}
	a.say("Rattrapage : %d log(s) à envoyer.", len(todo))
	go func() {
		for _, path := range todo {
			a.markSeen(path)
			a.handle(cfg, path, 1)
		}
	}()
}

func (a *app) isSeen(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[path]
}

func (a *app) markSeen(path string) {
	a.mu.Lock()
	a.seen[path] = true
	seen := a.seen
	a.mu.Unlock()
	saveSeen(a.dir, seen)
}

func (a *app) lastRun() time.Time {
	data, err := os.ReadFile(filepath.Join(a.dir, "last_run.txt"))
	if err != nil {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return at
}

func (a *app) saveLastRun() {
	os.WriteFile(filepath.Join(a.dir, "last_run.txt"), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

func hasCompressedTwin(path string) bool {
	if _, err := os.Stat(path + ".zevtc"); err == nil {
		return true
	}
	_, err := os.Stat(strings.TrimSuffix(path, ".evtc") + ".zevtc")
	return err == nil
}

// Un log : dps.report puis Forge. Quand l'un des deux ne répond pas, l'envoi est reprogrammé ; un refus
// motivé de Forge (« aucune soirée ») ne l'est pas.
func (a *app) handle(cfg config, path string, attempt int) {
	name := filepath.Base(path)
	e := entry{At: time.Now(), File: name, Path: path, Status: "uploading", Attempts: attempt}
	a.push(e)
	a.say("Envoi de %s sur dps.report…%s", name, attemptNote(attempt))
	up, err := uploadToDpsReport(&cfg, path)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "identical file") {
		// Un autre outil (le Log Uploader de Nexus, souvent) l'a envoyé juste avant : dps.report refuse le
		// doublon. Le log est sur le compte dps.report du joueur, on y retrouve son lien par l'heure du combat.
		if found, lookupErr := findUploaded(&cfg, path); lookupErr == nil && found != nil {
			a.say("  Déjà sur dps.report (envoyé par un autre outil) : %s", found.Permalink)
			up, err = found, nil
		} else if lookupErr != nil {
			a.say("  Déjà sur dps.report, mais introuvable sur ton compte : %v", lookupErr)
		}
	}
	if err != nil {
		a.say("  dps.report a refusé %s : %v", name, err)
		a.schedule(e, "dps.report : "+err.Error(), true)
		reportError(&cfg, "dps.report a refusé un log : "+err.Error(), map[string]any{"file": name, "attempt": attempt})
		return
	}
	e.Boss, e.Success, e.Permalink, e.Status = up.Encounter.Boss, up.Encounter.Success, up.Permalink, "forge"
	a.push(e)
	a.say("  %s → %s", up.Encounter.Boss, up.Permalink)
	in, code, err := sendToForge(&cfg, up.Permalink)
	if err != nil {
		a.say("  Forge n’a pas rangé le log : %v", err)
		a.schedule(e, "Forge : "+err.Error(), code == 0 || code >= 500 || code == 429)
		reportError(&cfg, "Forge n’a pas rangé un log : "+err.Error(), map[string]any{"file": name, "permalink": up.Permalink, "status": code, "attempt": attempt})
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

// Les derniers envois du compte dps.report du joueur (son token), pour retrouver un log parti par un autre outil.
type uploadsPage struct {
	Uploads []struct {
		Permalink     string `json:"permalink"`
		EncounterTime int64  `json:"encounterTime"`
		Encounter     struct {
			Boss    string `json:"boss"`
			Success bool   `json:"success"`
		} `json:"encounter"`
	} `json:"uploads"`
	Pages int `json:"pages"`
}

// L'envoi du compte dont l'heure de combat est celle du fichier (arcdps nomme chaque log de son heure locale de
// début, à quelques secondes près) ; nil sans token ou sans correspondance.
func findUploaded(cfg *config, path string) (*uploadResponse, error) {
	if cfg.DpsReportUserToken == "" {
		return nil, errors.New("pas de token dps.report dans les réglages")
	}
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".zevtc"), ".evtc")
	started, err := time.ParseInLocation("20060102-150405", name, time.Local)
	if err != nil {
		return nil, fmt.Errorf("nom de fichier sans heure (%s)", name)
	}
	for page := 1; page <= 3; page++ {
		request, err := http.NewRequest(http.MethodGet, dpsReportBase+"/getUploads?"+url.Values{"userToken": {cfg.DpsReportUserToken}, "page": {fmt.Sprint(page)}}.Encode(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", "forge-uploader/"+version)
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		var listing uploadsPage
		decodeErr := json.NewDecoder(response.Body).Decode(&listing)
		response.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("liste dps.report illisible (%s)", response.Status)
		}
		for _, upload := range listing.Uploads {
			at := time.Unix(upload.EncounterTime, 0)
			if diff := at.Sub(started); diff > -3*time.Minute && diff < 3*time.Minute && upload.Permalink != "" {
				found := &uploadResponse{Permalink: upload.Permalink}
				found.Encounter.Boss, found.Encounter.Success = upload.Encounter.Boss, upload.Encounter.Success
				return found, nil
			}
			// La liste est du plus récent au plus ancien : au-delà d'une heure avant le combat, inutile d'aller plus loin.
			if at.Before(started.Add(-time.Hour)) {
				return nil, nil
			}
		}
		if page >= listing.Pages {
			break
		}
	}
	return nil, nil
}

func attemptNote(attempt int) string {
	if attempt <= 1 {
		return ""
	}
	return fmt.Sprintf(" (tentative %d)", attempt)
}

// Après un échec : nouvelle tentative plus tard, ou abandon après la dernière (le bouton Renvoyer reste).
func (a *app) schedule(e entry, where string, retryable bool) {
	if retryable && e.Attempts < maxAttempts {
		wait := retryAfter[e.Attempts-1]
		e.Status, e.NextTry = "retry", time.Now().Add(wait)
		e.Where = fmt.Sprintf("%s · nouvel essai dans %d min", where, int(wait.Minutes()))
		a.say("  Nouvel essai dans %d min.", int(wait.Minutes()))
	} else {
		e.Status, e.Where = "error", where
	}
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

// Le code HTTP accompagne l'erreur : un 5xx ou un silence se retente, un 4xx non.
func sendToForge(cfg *config, permalink string) (*intakeResponse, int, error) {
	body, _ := json.Marshal(map[string]string{"permalink": permalink})
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.ForgeURL, "/")+"/api/uploads", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("User-Agent", "forge-uploader/"+version)
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	var payload intakeResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, response.StatusCode, fmt.Errorf("réponse illisible (%s)", response.Status)
	}
	if payload.Error != "" {
		return nil, response.StatusCode, errors.New(payload.Error)
	}
	return &payload, response.StatusCode, nil
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

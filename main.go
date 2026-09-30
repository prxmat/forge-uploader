// Forge Uploader : surveille les logs arcdps, les envoie sur dps.report puis à Forge (Le Bus Magique).
// Un seul binaire, icône dans la zone de notification, réglages et journal dans le navigateur. Voir README.md.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	version       = "0.2.1"
	defaultForge  = "https://forge-lbm.vercel.app"
	dpsReportBase = "https://dps.report"
	// Adresse de la fenêtre de l'app (locale, jamais exposée).
	listenAddr = "127.0.0.1:47831"
	homeURL    = "http://" + listenAddr + "/"
	// Nombre d'envois gardés dans la fenêtre.
	historySize = 200
)

type config struct {
	ForgeURL           string `json:"forge_url"`
	Token              string `json:"token"`
	LogsDir            string `json:"logs_dir"`
	DetailedWvw        bool   `json:"detailed_wvw"`
	DpsReportUserToken string `json:"dps_report_user_token,omitempty"`
	StartWithWindows   bool   `json:"start_with_windows"`
}

// Un log envoyé (ou en cours), tel que la fenêtre le montre.
type entry struct {
	At        time.Time `json:"at"`
	File      string    `json:"file"`
	Status    string    `json:"status"` // uploading | forge | done | rejected | error
	Boss      string    `json:"boss"`
	Success   bool      `json:"success"`
	Permalink string    `json:"permalink"`
	Where     string    `json:"where"`
	Kind      string    `json:"kind"`
}

type app struct {
	dir     string
	mu      sync.Mutex
	cfg     config
	member  string // Nom Forge quand le token est accepté, sinon vide.
	problem string // Ce qui bloque (token, dossier…), sinon vide.
	history []entry
	logFile *os.File
	// Les abonnés de la fenêtre (SSE) reçoivent chaque changement.
	subs map[chan struct{}]struct{}
}

var client = &http.Client{Timeout: 3 * time.Minute}

func main() {
	dir, err := configDir()
	if err != nil {
		fmt.Println("Erreur :", err)
		os.Exit(1)
	}
	a := &app{dir: dir, subs: map[chan struct{}]struct{}{}}
	a.cfg = loadConfig(dir)
	a.logFile, _ = os.OpenFile(filepath.Join(dir, "forge-uploader.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	a.loadHistory()
	a.say("Forge Uploader %s démarre", version)

	go a.serve()
	go a.watch()
	go a.connect()
	// Sans token, la fenêtre s'ouvre pour le demander.
	if a.cfg.Token == "" {
		go func() { time.Sleep(600 * time.Millisecond); openBrowser(homeURL) }()
	}
	runTray(a)
}

// Vérifie le token auprès de Forge et garde le nom du joueur ; réessaie tant que le réseau ne répond pas.
func (a *app) connect() {
	for {
		a.mu.Lock()
		cfg := a.cfg
		a.mu.Unlock()
		if cfg.Token == "" {
			a.setStatus("", "Aucun token Forge : ouvre les réglages.")
			return
		}
		name, err := checkToken(&cfg)
		if err == nil {
			a.setStatus(name, "")
			a.say("Connecté à Forge : %s", name)
			return
		}
		a.setStatus("", "Token Forge refusé : "+err.Error())
		a.say("Token Forge refusé : %v", err)
		if err.Error() == "Token Forge inconnu ou révoqué." {
			return
		}
		time.Sleep(30 * time.Second)
	}
}

func (a *app) setStatus(member, problem string) {
	a.mu.Lock()
	a.member, a.problem = member, problem
	a.mu.Unlock()
	a.notify()
	updateTray(a)
}

func (a *app) say(format string, args ...any) {
	line := fmt.Sprintf(time.Now().Format("2006-01-02 15:04:05")+"  "+format+"\n", args...)
	if a.logFile != nil {
		a.logFile.WriteString(line)
	}
}

func (a *app) push(e entry) {
	a.mu.Lock()
	// Une entrée par fichier : la ligne se met à jour au fil de l'envoi.
	replaced := false
	for index := range a.history {
		if a.history[index].File == e.File {
			a.history[index] = e
			replaced = true
			break
		}
	}
	if !replaced {
		a.history = append([]entry{e}, a.history...)
		if len(a.history) > historySize {
			a.history = a.history[:historySize]
		}
	}
	a.mu.Unlock()
	a.saveHistory()
	a.notify()
}

func (a *app) notify() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *app) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subs[ch] = struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		delete(a.subs, ch)
		a.mu.Unlock()
	}
}

func (a *app) saveConfig(cfg config) error {
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	if err := writeConfig(a.dir, cfg); err != nil {
		return err
	}
	if err := setAutostart(cfg.StartWithWindows); err != nil {
		a.say("Démarrage automatique : %v", err)
	}
	go a.connect()
	return nil
}

func (a *app) loadHistory() {
	data, err := os.ReadFile(filepath.Join(a.dir, "history.json"))
	if err == nil {
		json.Unmarshal(data, &a.history)
	}
}

func (a *app) saveHistory() {
	a.mu.Lock()
	data, _ := json.Marshal(a.history)
	a.mu.Unlock()
	os.WriteFile(filepath.Join(a.dir, "history.json"), data, 0o644)
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "ForgeUploader")
	return dir, os.MkdirAll(dir, 0o755)
}

func loadConfig(dir string) config {
	cfg := config{ForgeURL: defaultForge, DetailedWvw: true, LogsDir: defaultLogsDir()}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg.ForgeURL == "" {
		cfg.ForgeURL = defaultForge
	}
	if cfg.LogsDir == "" {
		cfg.LogsDir = defaultLogsDir()
	}
	writeConfig(dir, cfg)
	return cfg
}

func writeConfig(dir string, cfg config) error {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

func defaultLogsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", "Guild Wars 2", "addons", "arcdps", "arcdps.cbtlogs")
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

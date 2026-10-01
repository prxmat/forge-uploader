package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/page.html
var pageSource string

//go:embed assets/icon-256.png
var iconPNG []byte

var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"clock": func(t time.Time) string { return t.Local().Format("15:04:05") },
	"day":   func(t time.Time) string { return t.Local().Format("02/01") },
}).Parse(pageSource))

type view struct {
	Version   string
	Tab       string
	Member    string
	Problem   string
	Config    config
	History   []entry
	Journal   string
	Saved     bool
	ConfigDir string
	ForgeURL  string
}

// La fenêtre de l'app : une page locale, qui se met à jour toute seule (SSE).
func (a *app) serve() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		a.render(w, "logs", r.URL.Query().Get("ok") == "1")
	})
	mux.HandleFunc("/reglages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.mu.Lock()
			cfg := a.cfg
			a.mu.Unlock()
			cfg.Token = strings.TrimSpace(r.FormValue("token"))
			cfg.ForgeURL = strings.TrimRight(strings.TrimSpace(r.FormValue("forge_url")), "/")
			if cfg.ForgeURL == "" {
				cfg.ForgeURL = defaultForge
			}
			if dir := strings.TrimSpace(r.FormValue("logs_dir")); dir != "" {
				cfg.LogsDir = dir
			}
			cfg.DpsReportUserToken = strings.TrimSpace(r.FormValue("dps_report_user_token"))
			cfg.DetailedWvw = r.FormValue("detailed_wvw") == "on"
			cfg.StartWithWindows = r.FormValue("start_with_windows") == "on"
			if err := a.saveConfig(cfg); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/reglages?ok=1", http.StatusSeeOther)
			return
		}
		a.render(w, "settings", r.URL.Query().Get("ok") == "1")
	})
	mux.HandleFunc("/journal", func(w http.ResponseWriter, r *http.Request) { a.render(w, "journal", false) })
	mux.HandleFunc("/renvoyer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.resend(r.FormValue("file"))
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/rattraper", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.catchUp()
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE indisponible", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ch, unsubscribe := a.subscribe()
		defer unsubscribe()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				fmt.Fprint(w, "data: refresh\n\n")
				flusher.Flush()
			case <-time.After(25 * time.Second):
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/state.json", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"member": a.member, "problem": a.problem, "history": a.history})
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG)
	})
	server := &http.Server{Addr: listenAddr, Handler: mux}
	if err := server.ListenAndServe(); err != nil {
		a.say("Fenêtre indisponible (%v) : Forge Uploader tourne peut-être déjà.", err)
	}
}

func (a *app) render(w http.ResponseWriter, tab string, saved bool) {
	a.mu.Lock()
	v := view{Version: version, Tab: tab, Member: a.member, Problem: a.problem, Config: a.cfg, History: a.history, Saved: saved, ConfigDir: a.dir, ForgeURL: a.cfg.ForgeURL}
	a.mu.Unlock()
	if tab == "journal" {
		data, _ := os.ReadFile(filepath.Join(a.dir, "forge-uploader.log"))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 400 {
			lines = lines[len(lines)-400:]
		}
		v.Journal = strings.Join(lines, "\n")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, v); err != nil {
		a.say("Page : %v", err)
	}
}

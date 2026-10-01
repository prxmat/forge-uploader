package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Ce qui a cassé sur le PC du joueur remonte à Forge (puis Sentry), une fois par message et par dix minutes.
var reported = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func reportError(cfg *config, message string, context map[string]any) {
	if cfg.Token == "" || cfg.ForgeURL == "" {
		return
	}
	reported.Lock()
	if last, ok := reported.at[message]; ok && time.Since(last) < 10*time.Minute {
		reported.Unlock()
		return
	}
	reported.at[message] = time.Now()
	reported.Unlock()
	body, err := json.Marshal(map[string]any{"tool": "uploader", "version": version, "message": message, "context": context})
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.ForgeURL, "/")+"/api/client-errors", bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
	}
}

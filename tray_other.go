//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
)

// Hors Windows (développement) : pas d'icône, la fenêtre suffit ; Ctrl+C arrête.
func runTray(a *app) {
	fmt.Println("Forge Uploader", version, "—", homeURL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	a.say("Forge Uploader s’arrête")
}

func updateTray(_ *app) {}

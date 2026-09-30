//go:build windows

package main

import (
	_ "embed"
	"os"

	"fyne.io/systray"
)

//go:embed assets/icon.ico
var trayIcon []byte

var trayStatus *systray.MenuItem

// L'icône près de l'horloge : un clic droit ouvre le menu.
func runTray(a *app) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Forge Uploader")
		trayStatus = systray.AddMenuItem("Démarrage…", "")
		trayStatus.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem("Ouvrir Forge Uploader", "Les logs envoyés et l’état")
		settings := systray.AddMenuItem("Réglages", "Token Forge, token dps.report, dossier des logs")
		journal := systray.AddMenuItem("Journal", "Le journal complet")
		systray.AddSeparator()
		forge := systray.AddMenuItem("Ouvrir Forge", "forge-lbm.vercel.app")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quitter", "")
		updateTray(a)
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openBrowser(homeURL)
				case <-settings.ClickedCh:
					openBrowser(homeURL + "reglages")
				case <-journal.ClickedCh:
					openBrowser(homeURL + "journal")
				case <-forge.ClickedCh:
					a.mu.Lock()
					url := a.cfg.ForgeURL
					a.mu.Unlock()
					openBrowser(url)
				case <-quit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}, func() {
		a.say("Forge Uploader s’arrête")
		os.Exit(0)
	})
}

func updateTray(a *app) {
	if trayStatus == nil {
		return
	}
	a.mu.Lock()
	member, problem := a.member, a.problem
	a.mu.Unlock()
	switch {
	case problem != "":
		trayStatus.SetTitle("⚠ " + problem)
		systray.SetTooltip("Forge Uploader — " + problem)
	case member != "":
		trayStatus.SetTitle("Connecté : " + member)
		systray.SetTooltip("Forge Uploader — " + member)
	default:
		trayStatus.SetTitle("Connexion à Forge…")
		systray.SetTooltip("Forge Uploader")
	}
}

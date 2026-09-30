# Forge Uploader

Petit programme Windows pour Le Bus Magique : il surveille les logs arcdps, les envoie sur dps.report puis à Forge. Chaque essai apparaît dans Forge pendant la soirée, le direct passe au boss suivant tout seul sur un kill, et les combats McM font une soirée McM automatique.

## Installation

1. Télécharge `forge-uploader.exe` dans la [dernière version](https://github.com/prxmat/forge-uploader/releases/latest).
2. Lance-le. Windows peut afficher « Windows a protégé votre ordinateur » : « Informations complémentaires » → « Exécuter quand même » (le programme n’est pas signé).
3. Son icône apparaît dans la zone de notification (près de l’horloge) et sa fenêtre s’ouvre dans le navigateur pour demander ton token Forge : dans Forge, Mon suivi → Réglages → « Forge Uploader » → Créer mon token, puis colle-le dans Réglages.
4. Coche « Démarrer avec Windows » et oublie-le.

Clic droit sur l’icône : Ouvrir (les logs envoyés, en direct), Réglages, Journal, Ouvrir Forge, Quitter. Il retrouve tout seul `Documents\Guild Wars 2\addons\arcdps\arcdps.cbtlogs` et tous ses sous-dossiers. Les logs McM partent en « detailed WvW ».

## Réglages

Fichier `config.json` dans `%APPDATA%\ForgeUploader` :

```json
{
  "forge_url": "https://forge-lbm.vercel.app",
  "token": "…",
  "logs_dir": "C:\\Users\\toi\\Documents\\Guild Wars 2\\addons\\arcdps\\arcdps.cbtlogs",
  "detailed_wvw": true,
  "dps_report_user_token": ""
}
```

`dps_report_user_token` (facultatif) : ton token dps.report, pour que les logs apparaissent aussi sur ton compte dps.report. Tout se règle aussi dans la fenêtre (Réglages).

Le journal est dans `forge-uploader.log`, à côté ; la fenêtre est servie sur `127.0.0.1:47831`, jamais exposée.

## Construire

```bash
go run ./tools/icon            # regénère assets/icon.ico et icon-256.png
go-winres make --in winres.json # icône et version de l’exe (.syso)
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -H windowsgui" -o forge-uploader.exe .
```

Dépendances : `fyne.io/systray` (icône de notification), `golang.org/x/sys` (démarrage avec Windows).

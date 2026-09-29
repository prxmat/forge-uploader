# Forge Uploader

Petit programme Windows pour Le Bus Magique : il surveille les logs arcdps, les envoie sur dps.report puis à Forge. Chaque essai apparaît dans Forge pendant la soirée, et le direct passe au boss suivant tout seul sur un kill.

## Installation

1. Télécharge `forge-uploader.exe` dans la [dernière version](https://github.com/prxmat/forge-uploader/releases/latest).
2. Lance-le. Windows peut afficher « Windows a protégé votre ordinateur » : « Informations complémentaires » → « Exécuter quand même » (le programme n’est pas signé).
3. Il demande ton token Forge : dans Forge, Mon suivi → Réglages → « Forge Uploader » → Créer mon token, puis colle-le.
4. Laisse la fenêtre ouverte pendant que tu joues.

Il retrouve tout seul `Documents\Guild Wars 2\addons\arcdps\arcdps.cbtlogs`. Les logs McM partent en « detailed WvW ».

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

`dps_report_user_token` (facultatif) : ton token dps.report, pour que les logs apparaissent aussi sur ton compte dps.report.

Le journal est dans `forge-uploader.log`, à côté.

## Construire

```bash
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o forge-uploader.exe .
```

Aucune dépendance.

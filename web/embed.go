package web

import "embed"

// Assets embeds all static web assets (HTML, CSS, JS).
//
//go:embed index.html style.css app.js
var Assets embed.FS

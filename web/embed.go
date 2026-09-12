package web

import "embed"

// Assets embeds all static web assets (HTML, CSS, JS).
//
//go:embed index.html style.css app.js icon.png donate_wechat.jpg donate_alipay.jpg
var Assets embed.FS

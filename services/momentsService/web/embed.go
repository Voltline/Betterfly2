package web

import "embed"

//go:embed index.html app.css app.js brand.jpg
var Files embed.FS

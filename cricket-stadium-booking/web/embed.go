// Package web embeds the static seat-picker frontend into the server
// binary, so serving it no longer depends on the process's working
// directory (CODE_REVIEW.md finding #10 — http.ServeFile with a relative
// path silently 404s if the server isn't started from the repo root).
package web

import "embed"

//go:embed index.html app.js
var FS embed.FS

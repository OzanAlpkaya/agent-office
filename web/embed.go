// Package web embeds the built UI (web/dist/app, produced by `npm run build`).
// Without a build, a placeholder page explains how to build it, so `go build`
// and `go test` never need Node.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

//go:embed placeholder.html
var placeholder embed.FS

// FS returns the UI file system and whether it is a real build.
func FS() (fs.FS, bool) {
	app, err := fs.Sub(dist, "dist/app")
	if err == nil {
		if _, err := fs.Stat(app, "index.html"); err == nil {
			return app, true
		}
	}
	return placeholderFS{}, false
}

// placeholderFS serves placeholder.html as index.html.
type placeholderFS struct{}

func (placeholderFS) Open(name string) (fs.File, error) {
	if name == "index.html" {
		return placeholder.Open("placeholder.html")
	}
	return nil, fs.ErrNotExist
}

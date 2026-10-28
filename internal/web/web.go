// Package web embeds the built frontend. The binary carries the interface with
// it, so a container needs one file and no Node runtime.
//
// `make web` builds the Vite app into dist/. A checkout that has not run it — and
// a `go test ./...` on a machine with no Node — still compiles and still serves a
// page: the placeholder below is embedded in the source. That way the frontend
// build is a build step, not a precondition for the Go code to exist.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

//go:embed placeholder.html
var placeholder []byte

// Dist returns the built frontend, rooted at its index document.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler serves the embedded frontend. An unknown path falls back to
// index.html, so a deep link such as /tasks/12 boots the app instead of 404ing;
// the app itself then reads the path. Paths under /api/ never reach here — the
// router owns those, and a JSON endpoint that answered HTML would be worse than a
// 404.
func Handler() http.Handler {
	dir := Dist()
	files := http.FileServer(http.FS(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(dir, name); err != nil {
			serveIndex(w, dir)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, dir fs.FS) {
	body, err := fs.ReadFile(dir, "index.html")
	if err != nil {
		// No built bundle in this checkout: serve the placeholder instead.
		body = placeholder
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

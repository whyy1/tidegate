// Package webui embeds the compiled Tidegate Web UI static assets when built.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an HTTP handler serving the embedded Web UI and SPA fallback,
// or nil when the frontend bundle has not been compiled into dist/.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return newSPAHandler(sub)
}

func newSPAHandler(sub fs.FS) http.Handler {
	f, err := sub.Open("index.html")
	if err != nil {
		return nil
	}
	_ = f.Close()

	indexData, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil
	}

	fileSrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		rel := strings.TrimPrefix(clean, "/")
		if rel != "" && rel != "index.html" {
			if info, err := fs.Stat(sub, rel); err == nil && !info.IsDir() {
				if strings.HasPrefix(rel, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileSrv.ServeHTTP(w, r)
				return
			}
			// Do not fall back to index.html for missing static asset extensions.
			if ext := path.Ext(rel); ext != "" && ext != ".html" {
				http.NotFound(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(indexData))
	})
}

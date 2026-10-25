package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var FS embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(FS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	file := http.FileServer(http.FS(sub))
	// index.html is not hashed, so a stale copy would pair an old bundle with a
	// new protocol. Hashed assets under /assets stay cacheable.
	noStore := func(w http.ResponseWriter, h http.Handler, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		h.ServeHTTP(w, r)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || p == "index.html" {
			noStore(w, file, r)
			return
		}
		if _, err := fs.Stat(sub, p); err == nil {
			file.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		noStore(w, file, r2)
	})
}

package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves static files and falls back to index.html for any path
// that is not a regular file (client-side routes, directories), so routes
// survive a browser refresh and directory listings are never exposed.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if info, err := fs.Stat(static, name); err != nil || info.IsDir() {
			http.ServeFileFS(w, r, static, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}

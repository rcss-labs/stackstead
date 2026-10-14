package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// spaHandler serves the single-page app:
//   - /assets/* are content-hashed build files: immutable caching, and a
//     missing one is a plain 404 (falling back to index.html would hand HTML
//     to a <script> tag and blank the page with a MIME error);
//   - other regular files (favicon) are served with no-cache;
//   - everything else (client routes, directories) gets index.html with
//     no-cache and security headers, so routes survive a refresh and
//     directory listings are never exposed.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")

		if clean == "/assets" || strings.HasPrefix(clean, "/assets/") {
			info, err := fs.Stat(static, name)
			if err != nil || info.IsDir() {
				http.Error(w, "404 not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}

		if info, err := fs.Stat(static, name); err == nil && !info.IsDir() && name != "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}

		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		http.ServeFileFS(w, r, static, "index.html")
	})
}

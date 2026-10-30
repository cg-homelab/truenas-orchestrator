package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// handleAPINotFound answers every /api/ path no operation matched with a JSON 404.
func handleAPINotFound(api huma.API) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := humago.NewContext(&huma.Operation{}, r, w)
		_ = huma.WriteErr(api, ctx, http.StatusNotFound, "no such API route")
	})
}

// handleSPA serves files from spa, and index.html for any path that is not a file, so
// client-side routes survive a reload. A nil spa serves 404s.
func handleSPA(spa fs.FS) http.Handler {
	if spa == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(spa)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if _, err := fs.Stat(spa, name); errors.Is(err, fs.ErrNotExist) {
			http.ServeFileFS(w, r, spa, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}

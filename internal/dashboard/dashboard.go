package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("dashboard static not built — run control-plane build"))
		})
	}
	return http.FileServer(http.FS(sub))
}

// StaticFS returns the embedded static FS for tooling.
func StaticFS() fs.FS {
	sub, _ := fs.Sub(staticFS, "static")
	return sub
}

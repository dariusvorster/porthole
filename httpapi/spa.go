package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// distFS holds the built SPA so portholed ships as a single binary (spec §5.2).
// The frontend is built into ./dist (httpapi/dist) by `make build` — go:embed
// cannot reach ../web/dist, so Vite is configured to output here. A placeholder
// dist/index.html is kept so the package still compiles before a frontend build.
//
//go:embed all:dist
var distFS embed.FS

// spaSub is the dist tree rooted at its top, and spaFiles serves real assets.
var (
	spaSub    = mustSub(distFS, "dist")
	spaFiles  = http.FileServer(http.FS(spaSub))
)

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// handleSPA serves the embedded console for all non-API GET routes. Existing
// assets are served directly; everything else falls back to index.html so the
// SPA owns client-side history. /api/* never reaches here as the SPA — an
// unmatched API path 404s as an API miss, not an HTML page.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}

	upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if upath == "" {
		serveIndex(w, r)
		return
	}

	if f, err := spaSub.Open(upath); err == nil {
		f.Close()
		spaFiles.ServeHTTP(w, r)
		return
	}

	// Missing file. Asset-like requests (under /assets, or anything with a file
	// extension) must 404 — handing the browser index.html where it expects a
	// JS/CSS file just produces a confusing MIME error. Extensionless paths are
	// client-side routes, so they get the SPA history fallback to index.html.
	if strings.HasPrefix(r.URL.Path, "/assets/") || path.Ext(upath) != "" {
		http.NotFound(w, r)
		return
	}
	serveIndex(w, r)
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(spaSub, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
)

// TestSPAServing locks in the B7 behavior: extensionless unknown paths get the
// index.html history fallback, but a missing asset 404s (never HTML where the
// browser expects JS/CSS), and unmatched /api paths stay API 404s.
func TestSPAServing(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}
	srv := testServer(f)

	cases := []struct {
		name, path string
		wantCode   int
		wantHTML   bool
	}{
		{"root", "/", http.StatusOK, true},
		{"client route fallback", "/containers/web", http.StatusOK, true},
		{"missing asset 404", "/assets/does-not-exist.js", http.StatusNotFound, false},
		{"api miss not html", "/api/does-not-exist", http.StatusNotFound, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req("GET", c.path))
			if rec.Code != c.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, c.wantCode)
			}
			isHTML := strings.Contains(rec.Header().Get("Content-Type"), "text/html")
			if isHTML != c.wantHTML {
				t.Errorf("html = %v, want %v (content-type=%q)", isHTML, c.wantHTML, rec.Header().Get("Content-Type"))
			}
		})
	}
}

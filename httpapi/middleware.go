package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// browserGuard implements the §5.2a hardening that must hold even in
// localhost/no-auth mode. Two distinct defenses:
//
//   - Host allow-list → defeats DNS rebinding. A malicious page can point a
//     hostname it controls at 127.0.0.1, but it cannot forge the Host header to
//     a value we accept. Enforced on every request, reads included.
//   - Origin check → defeats CSRF. Any request carrying an Origin (all
//     cross-origin fetches do) must present one we allow. Enforced strictly on
//     state-changing methods; for safe methods a present-but-foreign Origin is
//     also rejected, since our SPA is same-origin and never sets one.
//
// This is deliberately independent of auth and of the network bind-guard: the
// browser is an attack surface even when the socket is bound to loopback.
type browserGuard struct {
	allowedHosts   map[string]struct{} // exact host:port and bare host forms
	allowedOrigins map[string]struct{} // scheme://host:port
	next           http.Handler
}

func newBrowserGuard(allowedHosts, allowedOrigins []string, next http.Handler) *browserGuard {
	hs := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		hs[strings.ToLower(h)] = struct{}{}
	}
	os := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		os[strings.ToLower(o)] = struct{}{}
	}
	return &browserGuard{allowedHosts: hs, allowedOrigins: os, next: next}
}

func (g *browserGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Host allow-list (anti-rebinding) — always.
	if !g.hostAllowed(r.Host) {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}

	// 2. Origin check (anti-CSRF). A foreign Origin is never legitimate for our
	// same-origin SPA; reject it on any method that carries one.
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, ok := g.allowedOrigins[strings.ToLower(origin)]; !ok {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
	}

	g.next.ServeHTTP(w, r)
}

// hostAllowed accepts an exact match on either the full host:port or the bare
// hostname (some clients omit the port for default ports).
func (g *browserGuard) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	if _, ok := g.allowedHosts[host]; ok {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		if _, ok := g.allowedHosts[h]; ok {
			return true
		}
	}
	return false
}

// securityHeaders sets conservative defaults on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// No permissive CORS header is ever set — cross-origin reads are not a
		// supported mode for a single-operator local control plane.
		next.ServeHTTP(w, r)
	})
}

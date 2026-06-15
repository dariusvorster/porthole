package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
)

const secretToken = "dckr_pat_TESTSECRET_must_not_leak"

type fakeRegistry struct {
	gotToken  string
	loginErr  error
	logins    []engine.RegistryAuth
	loggedOut []string
}

func (f *fakeRegistry) RegistryLogin(_ context.Context, _, _ string, token io.Reader) error {
	b, _ := io.ReadAll(token)
	f.gotToken = string(b)
	return f.loginErr
}
func (f *fakeRegistry) RegistryList(context.Context) ([]engine.RegistryAuth, error) {
	return f.logins, nil
}
func (f *fakeRegistry) RegistryLogout(_ context.Context, host string) error {
	f.loggedOut = append(f.loggedOut, host)
	return nil
}

var _ RegistryEngine = (*fakeRegistry)(nil)

func regServer(eng *fakeEngine, reg *fakeRegistry) *Server {
	return New(eng, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Registry:       reg,
	})
}

func TestRegistryLoginSuccessNoLeak(t *testing.T) {
	reg := &fakeRegistry{}
	srv := regServer(upEngine(), reg)
	body := `{"host":"registry-1.docker.io","username":"alice","token":"` + secretToken + `"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/registry/login", body))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	// The token reached the engine (piped through)…
	if reg.gotToken != secretToken {
		t.Errorf("engine got token %q, want it piped through", reg.gotToken)
	}
	// …but it must NEVER appear in the response.
	if strings.Contains(rec.Body.String(), secretToken) {
		t.Fatalf("SECRET LEAK: token in response body: %q", rec.Body.String())
	}
}

func TestRegistryLoginFailureScrubbed(t *testing.T) {
	reg := &fakeRegistry{loginErr: &engine.CLIError{
		Kind:    engine.ErrRegistryLoginFailed,
		Message: "login failed — check your username and token",
		Raw:     "",
	}}
	srv := regServer(upEngine(), reg)
	body := `{"host":"registry-1.docker.io","username":"alice","token":"` + secretToken + `"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/registry/login", body))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	var env errorEnvelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Kind != string(engine.ErrRegistryLoginFailed) {
		t.Errorf("kind = %q", env.Error.Kind)
	}
	if strings.Contains(rec.Body.String(), secretToken) {
		t.Fatalf("SECRET LEAK: token in error response: %q", rec.Body.String())
	}
}

func TestRegistryLoginMissingFields(t *testing.T) {
	srv := regServer(upEngine(), &fakeRegistry{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/registry/login", `{"host":"x","username":"y"}`)) // no token
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestRegistryListState(t *testing.T) {
	reg := &fakeRegistry{logins: []engine.RegistryAuth{{Host: "registry-1.docker.io", Username: "butterfingerza"}}}
	srv := regServer(upEngine(), reg)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/registry"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []engine.RegistryAuth
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Username != "butterfingerza" {
		t.Errorf("state = %+v", got)
	}
}

func TestRegistryLogout(t *testing.T) {
	reg := &fakeRegistry{}
	srv := regServer(upEngine(), reg)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/registry/logout", `{"host":"registry-1.docker.io"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	if len(reg.loggedOut) != 1 || reg.loggedOut[0] != "registry-1.docker.io" {
		t.Errorf("loggedOut = %v", reg.loggedOut)
	}
}

func TestRegistryForeignOriginRejected(t *testing.T) {
	srv := regServer(upEngine(), &fakeRegistry{})
	r := httptest.NewRequest("POST", "/api/registry/login", strings.NewReader(`{"host":"x","username":"y","token":"z"}`))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestRegistryGatedWhenDaemonDown(t *testing.T) {
	eng := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false, Detail: "down"}}
	srv := regServer(eng, &fakeRegistry{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/registry"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

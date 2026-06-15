package supervisor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
)

func TestDecideHealth(t *testing.T) {
	hp := HealthPolicy{Type: "http", Port: 80, Retries: 3}
	now := time.Now()

	cases := []struct {
		name        string
		pass        bool
		withinStart bool
		prev        healthRuntime
		wantState   HealthState
		wantFails   int
	}{
		{"pass → healthy", true, false, healthRuntime{State: HealthStarting}, HealthHealthy, 0},
		{"fail in start-period → starting, no count", false, true, healthRuntime{State: HealthStarting}, HealthStarting, 0},
		{"first fail post-start → stays starting, fails=1", false, false, healthRuntime{State: HealthStarting}, HealthStarting, 1},
		{"reach retries → unhealthy", false, false, healthRuntime{State: HealthStarting, Failures: 2}, HealthUnhealthy, 3},
		{"healthy + single fail → stays healthy", false, false, healthRuntime{State: HealthHealthy}, HealthHealthy, 1},
		{"recovery: unhealthy + pass → healthy", true, false, healthRuntime{State: HealthUnhealthy, Failures: 5}, HealthHealthy, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideHealth(c.pass, c.withinStart, c.prev, hp, now)
			if got.State != c.wantState {
				t.Errorf("state = %q, want %q", got.State, c.wantState)
			}
			if got.Failures != c.wantFails {
				t.Errorf("failures = %d, want %d", got.Failures, c.wantFails)
			}
			if !got.LastProbe.Equal(now) {
				t.Errorf("lastProbe not stamped")
			}
		})
	}
}

func TestProbeHTTP(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	host, port := splitHostPort(t, ok.URL)
	if !probeHTTP(context.Background(), host, port, "/") {
		t.Errorf("200 server should pass")
	}
	host, port = splitHostPort(t, bad.URL)
	if probeHTTP(context.Background(), host, port, "/") {
		t.Errorf("500 server should fail")
	}
	// dead port
	if probeHTTP(context.Background(), "127.0.0.1", 1, "/") {
		t.Errorf("dead port should fail")
	}
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port := splitHostPortAddr(t, ln.Addr().String())

	if !probeTCP(context.Background(), "127.0.0.1", port, time.Second) {
		t.Errorf("open listener should connect")
	}
	_ = ln.Close()
	if probeTCP(context.Background(), "127.0.0.1", port, 300*time.Millisecond) {
		t.Errorf("closed listener should fail")
	}
}

func TestProbeTargetPrefersDirectIP(t *testing.T) {
	// Running with a dedicated IP → direct IP + policy port.
	c := engine.Container{ID: "web"}
	c.Status.State = "running"
	c.Status.Networks = []engine.NetworkStatus{{IPv4Address: "192.168.64.5/24"}}
	c.Configuration.PublishedPorts = []engine.PublishedPort{{ContainerPort: 80, HostPort: 8080}}
	host, port, ok := probeTarget(c, HealthPolicy{Port: 80})
	if !ok || host != "192.168.64.5" || port != 80 {
		t.Fatalf("direct: host=%q port=%d ok=%v, want 192.168.64.5/80/true", host, port, ok)
	}

	// No IP (stopped-ish) but a published port → 127.0.0.1:hostPort fallback.
	c.Status.Networks = nil
	host, port, ok = probeTarget(c, HealthPolicy{Port: 80})
	if !ok || host != "127.0.0.1" || port != 8080 {
		t.Fatalf("fallback: host=%q port=%d ok=%v, want 127.0.0.1/8080/true", host, port, ok)
	}

	// No IP, no matching published port → not probeable.
	c.Configuration.PublishedPorts = nil
	if _, _, ok := probeTarget(c, HealthPolicy{Port: 80}); ok {
		t.Errorf("expected ok=false with no target")
	}
}

func TestHealthFromLabels(t *testing.T) {
	hp := healthFromLabels(map[string]string{
		"porthole.health.type":         "http",
		"porthole.health.port":         "80",
		"porthole.health.path":         "/healthz",
		"porthole.health.retries":      "2",
		"porthole.health.on-unhealthy": "restart",
	})
	if hp == nil || hp.Type != "http" || hp.Port != 80 || hp.Path != "/healthz" || hp.Retries != 2 || hp.OnUnhealthy != "restart" {
		t.Fatalf("healthFromLabels wrong: %+v", hp)
	}
	if healthFromLabels(map[string]string{"porthole.health.type": "bogus"}) != nil {
		t.Errorf("invalid type should yield nil")
	}
	if healthFromLabels(nil) != nil {
		t.Errorf("no labels should yield nil")
	}
}

func splitHostPort(t *testing.T, url string) (string, int) {
	t.Helper()
	return splitHostPortAddr(t, url[len("http://"):])
}

func splitHostPortAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	p, err := net.LookupPort("tcp", portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, p
}

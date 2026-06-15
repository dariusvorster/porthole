package stacks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// TestLiveUpDownVolume drives the REAL container runtime through the Executor.
// Gated on PORTHOLE_LIVE=1 so the normal `go test` stays hermetic. It proves the
// commands the executor emits are accepted by `container` v26, that members come
// up labelled + on the per-stack network, and that `down` keeps named volumes.
func TestLiveUpDownVolume(t *testing.T) {
	if os.Getenv("PORTHOLE_LIVE") == "" {
		t.Skip("set PORTHOLE_LIVE=1 to run against the real container runtime")
	}
	const (
		stackName = "pthlive"
		vol       = "pthlivevol"
		reader    = "pthlive-reader"
	)
	eng := engine.NewCLIEngine("")
	ex := NewExecutor(eng, idlock.New())
	ctx := context.Background()

	yml := fmt.Sprintf(`
services:
  writer:
    image: docker.io/library/alpine
    command: sh -c "echo persisted-ok > /data/f; sleep 300"
    volumes: ["%s:/data"]
    restart: always
  web:
    image: docker.io/library/alpine
    command: sleep 300
    depends_on: [writer]
`, vol)

	stack, rep := Parse(stackName, []byte(yml))
	if !rep.Valid {
		t.Fatalf("parse invalid: %v", rep.Errors)
	}

	t.Cleanup(func() {
		_, _ = ex.Down(ctx, stack)
		_ = eng.DeleteContainer(ctx, reader, true)
		_ = removeVolume(vol)
		_ = eng.RemoveNetwork(ctx, stackName)
	})

	// --- Up ---
	res, err := ex.Up(ctx, stack)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("up failures: %+v", res.Failures)
	}
	if res.Status != "up" {
		t.Fatalf("status = %q, want up", res.Status)
	}

	members, err := ex.members(ctx, stackName)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("want 2 members, got %d", len(members))
	}
	for svc, c := range members {
		if !c.IsRunning() {
			t.Errorf("%s not running", svc)
		}
		if c.Configuration.Labels[LabelStack] != stackName {
			t.Errorf("%s missing stack label", svc)
		}
		onNet := false
		for _, n := range c.Configuration.Networks {
			if n.Network == stackName {
				onNet = true
			}
		}
		if !onNet {
			t.Errorf("%s not attached to stack network %q (networks=%+v)", svc, stackName, c.Configuration.Networks)
		}
	}
	writer := members["writer"]
	if writer.Configuration.Labels[LabelRestart] != RestartAlways {
		t.Errorf("writer restart label = %q", writer.Configuration.Labels[LabelRestart])
	}
	t.Logf("UP OK: members=%d status=%s", len(members), res.Status)

	// Give the writer a moment to write the file.
	time.Sleep(2 * time.Second)

	// --- Down (keeps the volume) ---
	down, err := ex.Down(ctx, stack)
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if len(down.Removed) != 2 {
		t.Errorf("down removed = %v", down.Removed)
	}
	after, _ := ex.members(ctx, stackName)
	if len(after) != 0 {
		t.Errorf("members remain after down: %v", after)
	}
	t.Logf("DOWN OK: removed=%v", down.Removed)

	// --- Volume persisted across down? Read it back from a fresh container. ---
	if _, err := eng.RunContainer(ctx, engine.RunSpec{
		Name:    reader,
		Image:   "docker.io/library/alpine",
		Command: []string{"sh", "-c", "cat /data/f; sleep 30"},
		Volumes: []engine.RunVolume{{Source: vol, Target: "/data"}},
	}); err != nil {
		t.Fatalf("reader run: %v", err)
	}
	time.Sleep(1 * time.Second)
	rc, err := eng.Logs(ctx, reader, engine.LogOpts{Tail: 20})
	if err != nil {
		t.Fatalf("reader logs: %v", err)
	}
	defer rc.Close()
	out := readWithTimeout(rc, 5*time.Second)
	if !strings.Contains(out, "persisted-ok") {
		t.Fatalf("volume did NOT persist across down; reader saw %q", out)
	}
	t.Logf("VOLUME PERSISTED OK: reader saw %q", strings.TrimSpace(out))
}

func readWithTimeout(rc io.Reader, d time.Duration) string {
	type res struct{ b []byte }
	ch := make(chan res, 1)
	go func() {
		b, _ := io.ReadAll(rc)
		ch <- res{b}
	}()
	select {
	case r := <-ch:
		return string(r.b)
	case <-time.After(d):
		return ""
	}
}

// removeVolume deletes a named volume directly via the CLI (the engine has no
// volume verbs — v1 relies on `run -v` auto-creating, and never deletes volumes).
// Test-only cleanup, so shelling out here is fine.
func removeVolume(name string) error {
	return exec.Command("container", "volume", "rm", name).Run()
}

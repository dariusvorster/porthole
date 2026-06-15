package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeContainerBin writes a tiny shell script that stands in for the `container`
// binary: it prints two lines, and if "-f" is in its args it then follows
// (blocks forever, like `container logs -f`); otherwise it exits.
func fakeContainerBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fakecontainer.sh")
	script := "#!/bin/sh\n" +
		"echo line-1\n" +
		"echo line-2\n" +
		"for a in \"$@\"; do [ \"$a\" = \"-f\" ] && exec sleep 3600; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLogsTailExits(t *testing.T) {
	eng := NewCLIEngine(fakeContainerBin(t))
	rc, err := eng.Logs(context.Background(), "x", LogOpts{Tail: 10})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	defer rc.Close()
	out, _ := io.ReadAll(rc) // non-follow → child exits → EOF
	if !strings.Contains(string(out), "line-1") || !strings.Contains(string(out), "line-2") {
		t.Errorf("output missing lines: %q", out)
	}
}

// TestLogsFollowPinsTailZero is the PF2 fix: the follower must run `logs -n 0 -f`
// (follow NEW lines only, no backlog replay) so it doesn't duplicate the lines
// the non-follow history read already delivered.
func TestLogsFollowPinsTailZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echoargs.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"ARGS:$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := NewCLIEngine(path)

	rc, _ := eng.Logs(context.Background(), "cid", LogOpts{Follow: true})
	out, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(out), "ARGS:logs -n 0 -f cid") {
		t.Errorf("follow args = %q, want `logs -n 0 -f cid` (new-only)", out)
	}

	rc2, _ := eng.Logs(context.Background(), "cid", LogOpts{Tail: 5})
	out2, _ := io.ReadAll(rc2)
	_ = rc2.Close()
	if !strings.Contains(string(out2), "ARGS:logs -n 5 cid") {
		t.Errorf("history args = %q, want `logs -n 5 cid` (backlog)", out2)
	}
}

func TestLogsFollowCtxCancelKillsChild(t *testing.T) {
	eng := NewCLIEngine(fakeContainerBin(t))
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := eng.Logs(ctx, "x", LogOpts{Follow: true})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	// Read the two initial lines; the follower then blocks.
	buf := make([]byte, 64)
	n, _ := rc.Read(buf)
	if !strings.Contains(string(buf[:n]), "line-1") {
		t.Errorf("first read missing line-1: %q", buf[:n])
	}

	// Cancel ctx → child must die → reader unblocks with EOF promptly.
	cancel()
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(rc) // drains to EOF once the child is killed
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not unblock after ctx cancel — child likely leaked")
	}
	_ = rc.Close()
}

func TestLogsCloseKillsChild(t *testing.T) {
	eng := NewCLIEngine(fakeContainerBin(t))
	rc, err := eng.Logs(context.Background(), "x", LogOpts{Follow: true})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	// Close must force-kill the following child (it won't self-exit).
	done := make(chan error, 1)
	go func() { done <- rc.Close() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return — child not reaped")
	}
}

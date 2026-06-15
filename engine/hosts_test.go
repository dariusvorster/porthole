package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHostsArgs(t *testing.T) {
	if got := readHostsArgs("c1"); !reflect.DeepEqual(got, []string{"exec", "c1", "cat", "/etc/hosts"}) {
		t.Errorf("readHostsArgs = %v", got)
	}
	if got := writeHostsArgs("c1"); !reflect.DeepEqual(got, []string{"exec", "-i", "c1", "sh", "-c", "cat > /etc/hosts"}) {
		t.Errorf("writeHostsArgs = %v", got)
	}
}

// fakeBin writes a tiny shell script that records each invocation (one "INVOKE"
// line per call, plus its argv) and appends stdin, so a test can assert WriteHosts
// shelled out exactly once and piped the whole file. Returns the bin path + log.
func fakeBin(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "fakecontainer")
	script := "#!/bin/sh\n" +
		"{ echo INVOKE; for a in \"$@\"; do echo \"ARG:$a\"; done; echo STDIN_BEGIN; cat; echo; echo STDIN_END; } >> \"" + logPath + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func TestWriteHosts_SingleInvocationFullContent(t *testing.T) {
	bin, logPath := fakeBin(t)
	e := &CLIEngine{Bin: bin}

	content := "127.0.0.1 localhost\n# >>> porthole-managed (stack: shop)\n192.168.64.3  api  shop-api\n# <<<\n"
	if err := e.WriteHosts(context.Background(), "shop-web", content); err != nil {
		t.Fatalf("WriteHosts: %v", err)
	}

	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(out)
	if n := strings.Count(log, "INVOKE"); n != 1 {
		t.Errorf("expected exactly 1 exec invocation, got %d:\n%s", n, log)
	}
	for _, want := range []string{"ARG:exec", "ARG:-i", "ARG:shop-web", "ARG:sh", "ARG:-c", "ARG:cat > /etc/hosts"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in invocation:\n%s", want, log)
		}
	}
	// the FULL content reached the child's stdin (not line-by-line writes)
	if !strings.Contains(log, "192.168.64.3  api  shop-api") || !strings.Contains(log, "# <<<") {
		t.Errorf("full content not piped to stdin:\n%s", log)
	}
}

func TestReadHosts_InvokesCat(t *testing.T) {
	bin, logPath := fakeBin(t)
	e := &CLIEngine{Bin: bin}
	if _, err := e.ReadHosts(context.Background(), "shop-web"); err != nil {
		t.Fatalf("ReadHosts: %v", err)
	}
	log, _ := os.ReadFile(logPath)
	for _, want := range []string{"ARG:exec", "ARG:shop-web", "ARG:cat", "ARG:/etc/hosts"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("missing %q:\n%s", want, log)
		}
	}
}

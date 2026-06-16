package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScriptBin writes an executable fake `container` whose body is the given
// shell script, and returns its path. Distinct from logs_test's fakeContainerBin.
func writeScriptBin(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParsePhase(t *testing.T) {
	cases := []struct {
		in            string
		idx, total    int
		text          string
		ok            bool
	}{
		{"[1/6] Fetching image [0s]", 1, 6, "Fetching image", true},
		{"[2/6] Unpacking image for platform linux/arm64/v8 0% [4s]", 2, 6, "Unpacking image for platform linux/arm64/v8 0%", true},
		{"[1/6] Fetching image 0% (2 of 4 blobs, 10 KB/1,8 MB, 4 KB/s) [4s]", 1, 6, "Fetching image 0% (2 of 4 blobs, 10 KB/1,8 MB, 4 KB/s)", true},
		{"[6/6] Starting container [8s]", 6, 6, "Starting container", true},
		{"[0/6] [0s]", 0, 6, "", true},
		{"myid", 0, 0, "", false},
		{"random output", 0, 0, "", false},
	}
	for _, c := range cases {
		idx, total, text, ok := parsePhase(c.in)
		if ok != c.ok || idx != c.idx || total != c.total || text != c.text {
			t.Errorf("parsePhase(%q) = (%d,%d,%q,%v), want (%d,%d,%q,%v)",
				c.in, idx, total, text, ok, c.idx, c.total, c.text, c.ok)
		}
	}
}

func TestToArgs(t *testing.T) {
	spec := RunSpec{
		Name: "web", Image: "nginx", Network: "default",
		CPUs: 2, Memory: "512m", WorkDir: "/app", User: "1000",
		Env:     map[string]string{"FOO": "bar"},
		EnvFile: []string{"/e.env"},
		Labels:  map[string]string{"porthole.restart": "always"},
		Ports:   []RunPort{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
		Volumes: []RunVolume{{Source: "/host", Target: "/data"}},
		Command: []string{"sh", "-c", "echo hi"},
	}
	got := strings.Join(spec.toArgs(true), " ")
	for _, want := range []string{
		"run -d --progress plain", "--name web", "--network default",
		"--cpus 2", "--memory 512m", "--workdir /app", "--user 1000",
		"--label porthole.restart=always", "-e FOO=bar", "--env-file /e.env",
		"-p 8080:80", "-v /host:/data", "nginx sh -c echo hi",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("toArgs missing %q in: %s", want, got)
		}
	}
	// progress=false omits the flag.
	if strings.Contains(strings.Join(spec.toArgs(false), " "), "--progress") {
		t.Error("toArgs(false) should not include --progress")
	}
}

func TestToArgsNewFlags(t *testing.T) {
	spec := RunSpec{
		Image:      "nginx",
		Init:       true,
		ReadOnly:   true,
		Entrypoint: "/bin/myinit",
		ShmSize:    "128m",
		CapAdd:     []string{"NET_RAW", "SYS_TIME"},
		CapDrop:    []string{"MKNOD"},
		Tmpfs:      []string{"/run", "/tmp"},
	}
	got := strings.Join(spec.toArgs(false), " ")
	for _, want := range []string{
		"--init", "--read-only", "--entrypoint /bin/myinit", "--shm-size 128m",
		"--cap-add NET_RAW", "--cap-add SYS_TIME", "--cap-drop MKNOD",
		"--tmpfs /run", "--tmpfs /tmp",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("toArgs missing %q in: %s", want, got)
		}
	}
	// Repeated flags emit one arg each.
	if n := strings.Count(got, "--cap-add"); n != 2 {
		t.Errorf("expected 2 --cap-add, got %d: %s", n, got)
	}
	if n := strings.Count(got, "--tmpfs"); n != 2 {
		t.Errorf("expected 2 --tmpfs, got %d: %s", n, got)
	}
}

func TestToArgsNewFlagsAbsentWhenUnset(t *testing.T) {
	// A spec with none of the new fields must produce no new flags (no regression).
	got := strings.Join(RunSpec{Image: "nginx"}.toArgs(false), " ")
	for _, absent := range []string{"--init", "--read-only", "--entrypoint", "--shm-size", "--cap-add", "--cap-drop", "--tmpfs"} {
		if strings.Contains(got, absent) {
			t.Errorf("unset spec should not emit %q, got: %s", absent, got)
		}
	}
}

func TestRunStreamSuccess(t *testing.T) {
	bin := writeScriptBin(t, `
echo "[0/6] [0s]" >&2
echo "[1/6] Fetching image [0s]" >&2
echo "[2/6] Unpacking image [1s]" >&2
echo "[6/6] Starting container [2s]" >&2
echo "newid123"
`)
	eng := NewCLIEngine(bin)
	var phases []string
	var created string
	for u := range eng.RunStream(context.Background(), RunSpec{Image: "x"}) {
		switch u.Kind {
		case "progress":
			if u.Phase != "" {
				phases = append(phases, u.Phase)
			}
		case "created":
			created = u.ID
		case "error":
			t.Fatalf("unexpected error: %v", u.Err)
		}
	}
	if created != "newid123" {
		t.Errorf("created id = %q", created)
	}
	if len(phases) != 3 || phases[0] != "Fetching image" || phases[2] != "Starting container" {
		t.Errorf("phases = %v", phases)
	}
}

func TestRunStreamPullFailure(t *testing.T) {
	bin := writeScriptBin(t, `
echo "[1/6] Fetching image [0s]" >&2
echo "Error: HTTP request to https://registry-1.docker.io/v2/library/nope/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io" >&2
exit 1
`)
	eng := NewCLIEngine(bin)
	var gotErr error
	for u := range eng.RunStream(context.Background(), RunSpec{Image: "nope"}) {
		if u.Kind == "error" {
			gotErr = u.Err
		}
		if u.Kind == "created" {
			t.Fatal("should not create on pull failure")
		}
	}
	var ce *CLIError
	if !errors.As(gotErr, &ce) || ce.Kind != ErrImagePullFailed {
		t.Fatalf("want image_pull_failed CLIError, got %v", gotErr)
	}
}

func TestClassifyImagePullFailed(t *testing.T) {
	stderr := `Error: HTTP request to https://registry-1.docker.io/v2/library/no-such-image-xyz999/manifests/latest failed with response: 401 Unauthorized. Reason: Unknown, no credentials found for host registry-1.docker.io`
	kind, _ := classify(stderr)
	if kind != ErrImagePullFailed {
		t.Errorf("classify = %q, want image_pull_failed", kind)
	}
}

func TestMountVirtiofsDiscrimination(t *testing.T) {
	// Captured bind-mount shape (create spec §8.5).
	bindJSON := `{"destination":"/data","options":[],"source":"/tmp/portholetest","type":{"virtiofs":{}}}`
	volJSON := `{"destination":"/data","options":[],"source":".../volume.img","type":{"volume":{"name":"myvol","format":"ext4"}}}`

	var bind, vol Mount
	if err := json.Unmarshal([]byte(bindJSON), &bind); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(volJSON), &vol); err != nil {
		t.Fatal(err)
	}
	if !bind.IsBind() || bind.HostPath() != "/tmp/portholetest" {
		t.Errorf("bind: IsBind=%v HostPath=%q", bind.IsBind(), bind.HostPath())
	}
	if vol.IsBind() || vol.VolumeName() != "myvol" {
		t.Errorf("vol: IsBind=%v VolumeName=%q", vol.IsBind(), vol.VolumeName())
	}
}

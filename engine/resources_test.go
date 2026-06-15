package engine

import (
	"context"
	"reflect"
	"testing"
)

func TestVolumeList(t *testing.T) {
	bin := writeScriptBin(t, `cat <<'EOF'
[
 {"configuration":{"name":"data","creationDate":"2026-06-14T20:26:01Z","driver":"local","format":"ext4","sizeInBytes":549755813888,"source":"/x/data/volume.img","labels":{"k":"v"}},"id":"data"},
 {"configuration":{"name":"39dbb510-6757-4514-8b00-26b49116003d","creationDate":"2026-06-14T20:30:00Z","driver":"local","format":"ext4","sizeInBytes":549755813888,"source":"/x/anon/volume.img","labels":{}},"id":"39dbb510-6757-4514-8b00-26b49116003d"}
]
EOF`)
	vols, err := NewCLIEngine(bin).VolumeList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("want 2 volumes, got %d", len(vols))
	}
	if vols[0].Name != "data" || vols[0].SizeInBytes != 549755813888 || vols[0].Labels["k"] != "v" {
		t.Errorf("vol[0] = %+v", vols[0])
	}
	if vols[1].Name != "39dbb510-6757-4514-8b00-26b49116003d" {
		t.Errorf("anon vol name = %q", vols[1].Name)
	}
}

func TestParsePruneOutput(t *testing.T) {
	// Real capture: locale comma in the figure, ids on following lines.
	out := []byte("Reclaimed 138,8 MB in disk space\ncapvol\n39dbb510-6757-4514-8b00-26b49116003d\n")
	got := parsePruneOutput(out)
	if got.Reclaimed != "138,8 MB" {
		t.Errorf("reclaimed = %q", got.Reclaimed)
	}
	want := []string{"capvol", "39dbb510-6757-4514-8b00-26b49116003d"}
	if !reflect.DeepEqual(got.Removed, want) {
		t.Errorf("removed = %v, want %v", got.Removed, want)
	}

	// Nothing-reclaimed shape.
	z := parsePruneOutput([]byte("Reclaimed Zero KB in disk space\n"))
	if z.Reclaimed != "Zero KB" || len(z.Removed) != 0 {
		t.Errorf("zero prune = %+v", z)
	}
}

func TestClassifyVolumeInUse(t *testing.T) {
	stderr := `failed to delete volume: ["error": invalidArgument: "volume 'capvol' is currently in use and cannot be accessed by another container, or deleted", "id": capvol]
Error: delete failed for one or more volumes: ["capvol"]`
	kind, _ := classify(stderr)
	if kind != ErrVolumeInUse {
		t.Errorf("classify = %q, want volume_in_use", kind)
	}
}

func TestImagePullStream(t *testing.T) {
	bin := writeScriptBin(t, `
echo "[1/2] Fetching image [0s]" >&2
echo "[2/2] Unpacking image [1s]" >&2
`)
	var phases []string
	var done string
	for u := range NewCLIEngine(bin).ImagePull(context.Background(), "docker.io/library/busybox") {
		switch u.Kind {
		case "progress":
			phases = append(phases, u.Phase)
		case "created":
			done = u.ID
		case "error":
			t.Fatalf("unexpected error: %v", u.Err)
		}
	}
	if done != "docker.io/library/busybox" {
		t.Errorf("created id = %q", done)
	}
	if len(phases) != 2 || phases[0] != "Fetching image" {
		t.Errorf("phases = %v", phases)
	}
}

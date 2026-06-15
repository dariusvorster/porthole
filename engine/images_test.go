package engine

import (
	"context"
	"testing"
)

func TestImageList(t *testing.T) {
	// Fake `container image ls --format json` output (captured shape, §8.3).
	bin := writeScriptBin(t, `cat <<'EOF'
[
 {"configuration":{"name":"docker.io/library/alpine:latest","creationDate":"2026-06-09T20:11:09Z","descriptor":{"digest":"sha256:aaa","mediaType":"x","size":9218}},"id":"aaa","variants":[]},
 {"configuration":{"name":"docker.io/library/nginx:latest","creationDate":"2026-06-08T10:00:00Z","descriptor":{"digest":"sha256:bbb","mediaType":"x","size":1234}},"id":"bbb","variants":[]}
]
EOF`)
	imgs, err := NewCLIEngine(bin).ImageList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d", len(imgs))
	}
	if imgs[0].Reference != "docker.io/library/alpine:latest" {
		t.Errorf("ref = %q", imgs[0].Reference)
	}
	if imgs[0].Digest != "sha256:aaa" || imgs[0].Size != 9218 {
		t.Errorf("digest/size = %q/%d", imgs[0].Digest, imgs[0].Size)
	}
	if imgs[0].Created.Year() != 2026 {
		t.Errorf("created = %v", imgs[0].Created)
	}
}

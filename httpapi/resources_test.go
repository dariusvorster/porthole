package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/resources"
)

type fakeResources struct {
	images   []engine.Image
	volumes  []engine.Volume
	networks []engine.Network
	pull     []engine.RunUpdate

	volRemoveErr error
	prune        engine.PruneResult

	prunedKinds []string
	imgRemoved  []string
}

func (f *fakeResources) ImageList(context.Context) ([]engine.Image, error)   { return f.images, nil }
func (f *fakeResources) VolumeList(context.Context) ([]engine.Volume, error) { return f.volumes, nil }
func (f *fakeResources) ListNetworks(context.Context) ([]engine.Network, error) {
	return f.networks, nil
}
func (f *fakeResources) ImageRemove(_ context.Context, _ bool, refs ...string) error {
	f.imgRemoved = append(f.imgRemoved, refs...)
	return nil
}
func (f *fakeResources) ImagePrune(context.Context, bool) (engine.PruneResult, error) {
	f.prunedKinds = append(f.prunedKinds, "images")
	return f.prune, nil
}
func (f *fakeResources) ImageTag(context.Context, string, string) error { return nil }
func (f *fakeResources) ImagePull(_ context.Context, _ string) <-chan engine.RunUpdate {
	ch := make(chan engine.RunUpdate, len(f.pull)+1)
	for _, u := range f.pull {
		ch <- u
	}
	close(ch)
	return ch
}
func (f *fakeResources) VolumeRemove(context.Context, string) error { return f.volRemoveErr }
func (f *fakeResources) VolumePrune(context.Context) (engine.PruneResult, error) {
	f.prunedKinds = append(f.prunedKinds, "volumes")
	return f.prune, nil
}
func (f *fakeResources) NetworkPrune(context.Context) (engine.PruneResult, error) {
	f.prunedKinds = append(f.prunedKinds, "networks")
	return f.prune, nil
}
func (f *fakeResources) ContainerPrune(context.Context) (engine.PruneResult, error) {
	f.prunedKinds = append(f.prunedKinds, "containers")
	return f.prune, nil
}

var _ ResourceEngine = (*fakeResources)(nil)

func resServer(eng *fakeEngine, res *fakeResources) *Server {
	return New(eng, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Resources:      res,
	})
}

func mountVol(c engine.Container, vol string) engine.Container {
	m := engine.Mount{}
	m.Type.Volume = &engine.VolumeMount{Name: vol}
	c.Configuration.Mounts = append(c.Configuration.Mounts, m)
	return c
}

func TestResourcesBundleAnnotated(t *testing.T) {
	eng := upEngine()
	// stopped container mounts "data"; running container uses nginx.
	stoppedC := engine.Container{ID: "old"}
	stoppedC.Status.State = engine.StateStopped
	stoppedC = mountVol(stoppedC, "data")
	runC := engine.Container{ID: "web"}
	runC.Status.State = engine.StateRunning
	runC.Configuration.Image.Reference = "docker.io/library/nginx:latest"
	eng.containers = []engine.Container{stoppedC, runC}

	res := &fakeResources{
		images:  []engine.Image{{Reference: "docker.io/library/nginx:latest"}},
		volumes: []engine.Volume{{Name: "data"}, {Name: "39dbb510-6757-4514-8b00-26b49116003d"}},
		networks: func() []engine.Network {
			n := engine.Network{}
			n.Configuration.Name = "default"
			n.Configuration.Labels = map[string]string{"com.apple.container.resource.role": "builtin"}
			return []engine.Network{n}
		}(),
	}
	srv := resServer(eng, res)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/resources"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var b resourceBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Images) != 1 || len(b.Images[0].InUseByRunning) != 1 || b.Images[0].InUseByRunning[0] != "web" {
		t.Errorf("image in-use = %+v", b.Images)
	}
	var data, anon resources.AnnotatedVolume
	for _, v := range b.Volumes {
		if v.Name == "data" {
			data = v
		} else {
			anon = v
		}
	}
	if !data.InUse { // stopped container counts for volumes
		t.Error("data volume should be in use (stopped container)")
	}
	if !anon.Anonymous {
		t.Error("UUID volume should be flagged anonymous")
	}
	if len(b.Networks) != 1 || !b.Networks[0].Protected {
		t.Errorf("default network should be protected: %+v", b.Networks)
	}
}

func TestPrunePreviewReadOnly(t *testing.T) {
	eng := upEngine()
	res := &fakeResources{volumes: []engine.Volume{{Name: "unused"}}}
	srv := resServer(eng, res)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/prune/volumes?preview=true", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var plan resources.PrunePlan
	json.Unmarshal(rec.Body.Bytes(), &plan)
	if len(plan.Items) != 1 || plan.Items[0] != "unused" {
		t.Errorf("preview items = %v", plan.Items)
	}
	if len(res.prunedKinds) != 0 {
		t.Errorf("preview must NOT mutate: %v", res.prunedKinds)
	}
}

func TestPruneApply(t *testing.T) {
	eng := upEngine()
	res := &fakeResources{prune: engine.PruneResult{Reclaimed: "138,8 MB", Removed: []string{"unused"}}}
	srv := resServer(eng, res)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/prune/volumes", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var pr engine.PruneResult
	json.Unmarshal(rec.Body.Bytes(), &pr)
	if pr.Reclaimed != "138,8 MB" {
		t.Errorf("reclaimed = %q", pr.Reclaimed)
	}
	if len(res.prunedKinds) != 1 || res.prunedKinds[0] != "volumes" {
		t.Errorf("apply should prune volumes: %v", res.prunedKinds)
	}
}

func TestVolumeDeleteInUse(t *testing.T) {
	eng := upEngine()
	res := &fakeResources{volRemoveErr: &engine.CLIError{Kind: engine.ErrVolumeInUse, Message: "volume 'data' is currently in use"}}
	srv := resServer(eng, res)

	r := httptest.NewRequest("DELETE", "/api/volumes/data", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var env errorEnvelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Kind != string(engine.ErrVolumeInUse) {
		t.Errorf("kind = %q", env.Error.Kind)
	}
}

func TestImageDeleteSucceeds(t *testing.T) {
	eng := upEngine()
	res := &fakeResources{}
	srv := resServer(eng, res)

	r := httptest.NewRequest("DELETE", "/api/images?ref="+
		"docker.io%2Flibrary%2Fnginx%3Alatest", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202 (no in-use block)", rec.Code)
	}
	if len(res.imgRemoved) != 1 || res.imgRemoved[0] != "docker.io/library/nginx:latest" {
		t.Errorf("removed = %v", res.imgRemoved)
	}
}

func TestImagePullStreamsSSE(t *testing.T) {
	eng := upEngine()
	res := &fakeResources{pull: []engine.RunUpdate{
		{Kind: "progress", Index: 1, Total: 2, Phase: "Fetching image"},
		{Kind: "created", ID: "docker.io/library/busybox"},
	}}
	srv := resServer(eng, res)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/images/pull", `{"ref":"docker.io/library/busybox"}`))
	out := rec.Body.String()
	if !strings.Contains(out, "event: progress") || !strings.Contains(out, "Fetching image") {
		t.Errorf("missing progress: %s", out)
	}
	if !strings.Contains(out, "event: created") {
		t.Errorf("missing created: %s", out)
	}
}

func TestResourcesForeignOriginRejected(t *testing.T) {
	srv := resServer(upEngine(), &fakeResources{})
	r := httptest.NewRequest("POST", "/api/prune/volumes", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestResourcesGatedWhenDaemonDown(t *testing.T) {
	eng := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false, Detail: "down"}}
	srv := resServer(eng, &fakeResources{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/resources"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

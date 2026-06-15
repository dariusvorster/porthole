package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
	"github.com/porthole/porthole/reconcile"
	"github.com/porthole/porthole/stacks"
)

// sawEvent drains ch briefly, returning true if a named event arrives.
func sawEvent(ch <-chan reconcile.Event, name string) bool {
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case e := <-ch:
			if e.Name == name {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// TestMutationsEmitResourceEpoch verifies the PF1 fix: container create and stack
// up emit a `resource` SSE event so the Resources / network views refetch live
// (the F8 stale-Resources seam).
func TestMutationsEmitResourceEpoch(t *testing.T) {
	eng := newStackFake() // satisfies engine.Engine + stacks.Engine
	hub := reconcile.NewHub(eng, time.Second, time.Second)
	cr := &fakeCreator{updates: []engine.RunUpdate{{Kind: "created", ID: "x"}}}
	mgr := stacks.NewManager(stacks.NewMemStore(), eng, idlock.New())
	srv := New(eng, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Creator:        cr,
		Stacks:         mgr,
		Resources:      &fakeResources{},
		Supervision:    &recordingSup{},
		Hub:            hub,
	})

	// container create → resource event
	sub1, ch1 := hub.Subscribe()
	srv.ServeHTTP(httptest.NewRecorder(), postJSON("/api/containers", `{"image":"nginx"}`))
	if !sawEvent(ch1, "resource") {
		t.Error("container create did not emit a resource event")
	}
	hub.Unsubscribe(sub1)

	// stack up → resource event (alongside the stack event)
	importStack(t, srv, "s1", "services:\n  a:\n    image: nginx\n")
	sub2, ch2 := hub.Subscribe()
	srv.ServeHTTP(httptest.NewRecorder(), postJSON("/api/stacks/s1/up", ""))
	if !sawEvent(ch2, "resource") {
		t.Error("stack up did not emit a resource event")
	}
	hub.Unsubscribe(sub2)
}

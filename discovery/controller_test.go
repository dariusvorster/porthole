package discovery

import (
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHosts is an in-memory /etc/hosts store. failWrites names ids whose
// WriteHosts errors (to test best-effort retry). It counts writes per id.
type fakeHosts struct {
	mu         sync.Mutex
	files      map[string]string
	writes     map[string]int
	failWrites map[string]bool
}

func newFakeHosts() *fakeHosts {
	return &fakeHosts{files: map[string]string{}, writes: map[string]int{}, failWrites: map[string]bool{}}
}

func (f *fakeHosts) ReadHosts(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.files[id]; ok {
		return v, nil
	}
	return "127.0.0.1 localhost\n" + "192.168.64.99 " + id + "\n", nil
}

func (f *fakeHosts) WriteHosts(_ context.Context, id, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWrites[id] {
		return io.ErrClosedPipe
	}
	f.files[id] = content
	f.writes[id]++
	return nil
}

func (f *fakeHosts) get(id string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.files[id] }
func (f *fakeHosts) writeCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[id]
}

// fakeLocker records which keys were locked.
type fakeLocker struct {
	mu     sync.Mutex
	locked map[string]int
}

func newFakeLocker() *fakeLocker { return &fakeLocker{locked: map[string]int{}} }
func (l *fakeLocker) Lock(key string) func() {
	l.mu.Lock()
	l.locked[key]++
	l.mu.Unlock()
	return func() {}
}
func (l *fakeLocker) was(key string) bool { l.mu.Lock(); defer l.mu.Unlock(); return l.locked[key] > 0 }

func quietController(eng HostsRW, locks Locker) *Controller {
	return NewController(eng, locks, log.New(io.Discard, "", 0), 0) // synchronous
}

func member(id, svc, ip string, running bool) Snapshot {
	return Snapshot{ID: id, Stack: "shop", Service: svc, Discovery: true, IP: ip, Running: running}
}

func TestReconcile_InjectsPeersUnderLock(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	c.reconcile(context.Background(), []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	})
	if !strings.Contains(h.get("shop-web"), "192.168.64.3  api  shop-api") {
		t.Errorf("web didn't get api peer:\n%s", h.get("shop-web"))
	}
	if !strings.Contains(h.get("shop-api"), "192.168.64.5  web  shop-web") {
		t.Errorf("api didn't get web peer:\n%s", h.get("shop-api"))
	}
	if !l.was("shop-api") || !l.was("shop-web") {
		t.Errorf("injection must take the per-id lock for each member")
	}
	// self not in own file
	if strings.Contains(h.get("shop-api"), "api  shop-api") {
		t.Errorf("api file should not list itself:\n%s", h.get("shop-api"))
	}
}

func TestReconcile_IdempotentNoRedundantWrites(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	snaps := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	}
	c.reconcile(context.Background(), snaps)
	c.reconcile(context.Background(), snaps) // nothing changed
	if n := h.writeCount("shop-api"); n != 1 {
		t.Errorf("expected 1 write to api (no redundant), got %d", n)
	}
}

func TestReconcile_FailingInjectRetriedNextCycle(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	h.failWrites["shop-web"] = true
	c := quietController(h, l)
	snaps := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	}
	c.reconcile(context.Background(), snaps)
	if h.get("shop-web") != "" {
		t.Errorf("web write should have failed, got:\n%s", h.get("shop-web"))
	}
	if !strings.Contains(h.get("shop-api"), "web  shop-web") {
		t.Errorf("api should be unaffected by web's failure:\n%s", h.get("shop-api"))
	}
	// recover and re-run: web is retried (lastDesired was never set for it)
	h.failWrites["shop-web"] = false
	c.reconcile(context.Background(), snaps)
	if !strings.Contains(h.get("shop-web"), "api  shop-api") {
		t.Errorf("web should be injected on retry:\n%s", h.get("shop-web"))
	}
}

func TestReconcile_NonDiscoveryAndStandaloneIgnored(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	c.reconcile(context.Background(), []Snapshot{
		{ID: "shop-api", Stack: "shop", Service: "api", Discovery: false, IP: "192.168.64.3", Running: true}, // off
		{ID: "loner", Stack: "", Service: "", Discovery: false, IP: "192.168.64.7", Running: true},            // standalone
	})
	if h.writeCount("shop-api") != 0 || h.writeCount("loner") != 0 {
		t.Errorf("non-discovery / standalone containers must never be written")
	}
}

func TestReconcile_StopReinjectsRemainingPeers(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	up := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
		member("shop-db", "db", "192.168.64.6", true),
	}
	c.reconcile(context.Background(), up)
	// db stops: no IP, not running
	down := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
		member("shop-db", "db", "", false),
	}
	c.reconcile(context.Background(), down)
	if strings.Contains(h.get("shop-api"), "db  shop-db") {
		t.Errorf("stopped db should be dropped from api's peers:\n%s", h.get("shop-api"))
	}
	if !strings.Contains(h.get("shop-api"), "web  shop-web") {
		t.Errorf("web should still be present:\n%s", h.get("shop-api"))
	}
}

func TestReconcile_IPChurnReinjects(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	c.reconcile(context.Background(), []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	})
	// api restarts with a new IP
	c.reconcile(context.Background(), []Snapshot{
		member("shop-api", "api", "192.168.64.9", true),
		member("shop-web", "web", "192.168.64.5", true),
	})
	web := h.get("shop-web")
	if strings.Contains(web, "192.168.64.3") {
		t.Errorf("stale api IP survived churn:\n%s", web)
	}
	if !strings.Contains(web, "192.168.64.9  api  shop-api") {
		t.Errorf("web should learn api's new IP:\n%s", web)
	}
}

func TestReconcile_StripsWhenDiscoveryDisabled(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	on := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	}
	c.reconcile(context.Background(), on)
	// discovery turned OFF (still running)
	off := []Snapshot{
		{ID: "shop-api", Stack: "shop", Service: "api", Discovery: false, IP: "192.168.64.3", Running: true},
		{ID: "shop-web", Stack: "shop", Service: "web", Discovery: false, IP: "192.168.64.5", Running: true},
	}
	c.reconcile(context.Background(), off)
	if strings.Contains(h.get("shop-api"), beginPrefix) {
		t.Errorf("managed block should be stripped when discovery off:\n%s", h.get("shop-api"))
	}
	if !strings.Contains(h.get("shop-api"), "127.0.0.1 localhost") {
		t.Errorf("user lines must survive strip:\n%s", h.get("shop-api"))
	}
}

func TestReconcile_RestartReinjectsEvenIfBlockUnchanged(t *testing.T) {
	// The load-bearing case: a member restarts, its /etc/hosts is wiped to self,
	// but its desired block is identical (peers kept their IPs). The controller
	// must NOT assume the prior write survived — it must re-inject on the restart.
	h, l := newFakeHosts(), newFakeLocker()
	c := quietController(h, l)
	at := func(started string) []Snapshot {
		return []Snapshot{
			{ID: "shop-api", Stack: "shop", Service: "api", Discovery: true, IP: "192.168.64.3", Running: true, Started: "t1"},
			{ID: "shop-web", Stack: "shop", Service: "web", Discovery: true, IP: "192.168.64.5", Running: true, Started: started},
		}
	}
	c.reconcile(context.Background(), at("t1"))
	before := h.writeCount("shop-web")

	// simulate a restart: hosts wiped to self, startedDate bumped, peers unchanged
	h.mu.Lock()
	h.files["shop-web"] = "127.0.0.1 localhost\n192.168.64.5 shop-web\n"
	h.mu.Unlock()
	c.reconcile(context.Background(), at("t2"))

	if h.writeCount("shop-web") != before+1 {
		t.Errorf("restart must re-inject even with an unchanged block (writes %d -> %d)", before, h.writeCount("shop-web"))
	}
	if !strings.Contains(h.get("shop-web"), "192.168.64.3  api  shop-api") {
		t.Errorf("wiped hosts not re-injected after restart:\n%s", h.get("shop-web"))
	}
}

func TestOnCycle_DebounceCoalesces(t *testing.T) {
	h, l := newFakeHosts(), newFakeLocker()
	c := NewController(h, l, log.New(io.Discard, "", 0), 40*time.Millisecond)
	snaps := []Snapshot{
		member("shop-api", "api", "192.168.64.3", true),
		member("shop-web", "web", "192.168.64.5", true),
	}
	// rapid burst — should collapse to a single reconcile
	for i := 0; i < 5; i++ {
		c.OnCycle(context.Background(), snaps)
	}
	// wait past the debounce window for the single pass to run
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.writeCount("shop-api") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := h.writeCount("shop-api"); n != 1 {
		t.Errorf("debounce should coalesce the burst to 1 write, got %d", n)
	}
}

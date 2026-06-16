package stacks

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// remEngine is a fake stacks.Engine for the recreate/rollback tests: an in-memory
// container set, an ordered op log, and per-image/-id failure injection.
type remEngine struct {
	mu         sync.Mutex
	containers map[string]engine.Container
	ops        []string
	runSpecs   []engine.RunSpec // every spec passed to RunContainer, in order
	failImage  map[string]bool  // RunContainer fails if spec.Image is here
	failStart  map[string]bool  // StartContainer fails if id is here
	onRun      func()           // hook fired inside RunContainer (lock-span test)
}

func newRemEngine() *remEngine {
	return &remEngine{containers: map[string]engine.Container{}, failImage: map[string]bool{}, failStart: map[string]bool{}}
}

func (e *remEngine) log(s string) { e.mu.Lock(); e.ops = append(e.ops, s); e.mu.Unlock() }

func (e *remEngine) ListContainers(context.Context, bool) ([]engine.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]engine.Container, 0, len(e.containers))
	for _, c := range e.containers {
		out = append(out, c)
	}
	return out, nil
}
func (e *remEngine) ListNetworks(context.Context) ([]engine.Network, error)  { return nil, nil }
func (e *remEngine) CreateNetwork(context.Context, string) error             { return nil }
func (e *remEngine) RemoveNetwork(context.Context, string) error             { return nil }
func (e *remEngine) StopContainer(_ context.Context, id string) error        { e.log("stop:" + id); return nil }

func (e *remEngine) RunContainer(_ context.Context, spec engine.RunSpec) (string, error) {
	e.log("run:" + spec.Name + "@" + spec.Image)
	if e.onRun != nil {
		e.onRun()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runSpecs = append(e.runSpecs, spec)
	if e.failImage[spec.Image] {
		return "", &engine.CLIError{Kind: engine.ErrImagePullFailed, Message: "image not found"}
	}
	c := engine.Container{ID: spec.Name}
	c.Configuration.ID = spec.Name
	c.Configuration.Image.Reference = spec.Image
	c.Configuration.Labels = spec.Labels
	c.Status.State = engine.StateStopped // started separately
	e.containers[spec.Name] = c
	return spec.Name, nil
}

func (e *remEngine) StartContainer(_ context.Context, id string) error {
	e.log("start:" + id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failStart[id] {
		return &engine.CLIError{Kind: engine.ErrUnknown, Message: "won't start"}
	}
	if c, ok := e.containers[id]; ok {
		c.Status.State = engine.StateRunning
		e.containers[id] = c
	}
	return nil
}

func (e *remEngine) DeleteContainer(_ context.Context, id string, _ bool) error {
	e.log("delete:" + id)
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.containers, id)
	return nil
}

func (e *remEngine) opLog() []string { e.mu.Lock(); defer e.mu.Unlock(); return append([]string(nil), e.ops...) }

// drifted seeds a running member whose image differs from the declared service,
// so PlanReconcile flags it as a recreate.
func (e *remEngine) seed(c engine.Container) { e.mu.Lock(); e.containers[c.ID] = c; e.mu.Unlock() }

func member1(id, svc, image string) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.ID = id
	c.Configuration.Image.Reference = image
	c.Configuration.Labels = map[string]string{LabelStack: "shop", LabelService: svc}
	c.Configuration.Networks = []engine.NetworkAttach{{Network: "shop"}}
	c.Status.State = engine.StateRunning
	return c
}

func oneSvcStack(image string) Stack {
	return Stack{Name: "shop", Services: []Service{{Name: "api", Image: image}}}
}

func TestRemediate_HappyRecreate(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0")) // running old image
	ex := NewExecutor(eng, idlock.New())

	res, err := ex.Remediate(context.Background(), oneSvcStack("nginx:2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Outcome != OutcomeRecreated {
		t.Fatalf("expected one recreated, got %+v", res.Applied)
	}
	// order: delete old → run new → start new
	ops := strings.Join(eng.opLog(), ",")
	if ops != "delete:shop-api,run:shop-api@nginx:2.0,start:shop-api" {
		t.Errorf("unexpected sequence: %s", ops)
	}
}

func TestRemediate_CreateFailsRollsBack(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	eng.failImage["nginx:bogus"] = true // the NEW image fails to create
	ex := NewExecutor(eng, idlock.New())

	res, err := ex.Remediate(context.Background(), oneSvcStack("nginx:bogus"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back, got %+v", res.Applied)
	}
	if !strings.Contains(res.Applied[0].Message, "restored previous") {
		t.Errorf("rollback message should be loud: %q", res.Applied[0].Message)
	}
	// the OLD image was re-created and started
	ops := strings.Join(eng.opLog(), ",")
	if !strings.Contains(ops, "run:shop-api@nginx:1.0") || !strings.HasSuffix(ops, "start:shop-api") {
		t.Errorf("rollback should re-create+start the OLD snapshot: %s", ops)
	}
	if _, ok := eng.containers["shop-api"]; !ok {
		t.Error("service must not be left removed after rollback")
	}
}

func TestRemediate_RollbackAlsoFailsIsFailedDown(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	eng.failImage["nginx:bogus"] = true // new create fails
	eng.failImage["nginx:1.0"] = true   // rollback re-create ALSO fails
	ex := NewExecutor(eng, idlock.New())

	res, _ := ex.Remediate(context.Background(), oneSvcStack("nginx:bogus"))
	if len(res.Applied) != 1 || res.Applied[0].Outcome != OutcomeFailedDown {
		t.Fatalf("expected failed_down, got %+v", res.Applied)
	}
}

func TestRemediate_RollingIsolation(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	eng.seed(member1("shop-web", "web", "nginx:1.0"))
	eng.failImage["nginx:bad"] = true // web's new image fails → web rolls back
	ex := NewExecutor(eng, idlock.New())

	stack := Stack{Name: "shop", Services: []Service{
		{Name: "api", Image: "nginx:2.0"}, // good
		{Name: "web", Image: "nginx:bad"}, // fails → rollback
	}}
	res, err := ex.Remediate(context.Background(), stack)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RemediateOutcome{}
	for _, r := range res.Applied {
		got[r.Service] = r.Outcome
	}
	if got["api"] != OutcomeRecreated {
		t.Errorf("api should recreate independently, got %q", got["api"])
	}
	if got["web"] != OutcomeRolledBack {
		t.Errorf("web should roll back without blocking api, got %q", got["web"])
	}
}

func TestRemediate_NeverRemovesVolumes(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	ex := NewExecutor(eng, idlock.New())
	ex.Remediate(context.Background(), oneSvcStack("nginx:2.0"))
	for _, op := range eng.opLog() {
		if strings.Contains(op, "volume") {
			t.Errorf("recreate must never touch volumes, saw: %s", op)
		}
	}
}

func TestRemediate_LockHeldAcrossWholeSequence(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	locks := idlock.New()
	ex := NewExecutor(eng, locks)

	// A competitor that wants the same per-id lock; it releases immediately on
	// acquire. It must NOT acquire until the whole recreate sequence releases.
	var competitorGot int32
	started := make(chan struct{})
	go func() {
		<-started // released from inside the held sequence (onRun)
		unlock := locks.Lock("shop-api")
		atomic.StoreInt32(&competitorGot, 1)
		unlock()
	}()

	// onRun fires inside RunContainer — i.e. WHILE recreateService holds the per-id
	// lock. Signal the competitor to try, then confirm it's still blocked.
	var once sync.Once
	eng.onRun = func() {
		once.Do(func() { close(started) })
		time.Sleep(20 * time.Millisecond)
		if atomic.LoadInt32(&competitorGot) != 0 {
			t.Error("per-id lock was NOT held across the sequence — competitor acquired mid-recreate")
		}
	}
	ex.Remediate(context.Background(), oneSvcStack("nginx:2.0"))

	// once the sequence releases, the competitor acquires.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&competitorGot) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&competitorGot) != 1 {
		t.Error("competitor should acquire the lock once the sequence releases")
	}
}

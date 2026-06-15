package stacks

import (
	"context"
	"testing"

	"github.com/porthole/porthole/idlock"
)

const discoCompose = "services:\n  api:\n    image: nginx\n  web:\n    image: nginx\n"

func discoManager() (*Manager, *recEngine) {
	eng := &recEngine{}
	return NewManager(NewMemStore(), eng, idlock.New()), eng
}

func TestSetDiscovery_WritesLabelOnUp(t *testing.T) {
	m, eng := discoManager()
	if _, _, err := m.Import("shop", []byte(discoCompose)); err != nil {
		t.Fatal(err)
	}

	// Default OFF: no discovery label at up.
	if _, _, err := m.Up(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	for _, rc := range eng.runCalls {
		if _, has := rc.Labels[LabelDiscovery]; has {
			t.Errorf("discovery label written while OFF: %v", rc.Labels)
		}
	}

	// Toggle ON, re-up (fresh engine): every member gets porthole.discovery=on.
	if ok, err := m.SetDiscovery("shop", true); err != nil || !ok {
		t.Fatalf("SetDiscovery: ok=%v err=%v", ok, err)
	}
	eng.runCalls = nil
	eng.containers = nil // force re-create so labels are re-emitted
	if _, _, err := m.Up(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if len(eng.runCalls) == 0 {
		t.Fatal("expected members to be (re)created")
	}
	for _, rc := range eng.runCalls {
		if rc.Labels[LabelDiscovery] != "on" {
			t.Errorf("member %s missing discovery label: %v", rc.Name, rc.Labels)
		}
	}
}

func TestSetDiscovery_FlagsAndUnknown(t *testing.T) {
	m, _ := discoManager()
	if _, _, err := m.Import("shop", []byte(discoCompose)); err != nil {
		t.Fatal(err)
	}
	if m.DiscoveryFlags()["shop"] {
		t.Error("discovery should default off")
	}
	if _, _ = m.SetDiscovery("shop", true); !m.DiscoveryFlags()["shop"] {
		t.Error("flag should be on after SetDiscovery(true)")
	}
	if ok, _ := m.SetDiscovery("ghost", true); ok {
		t.Error("SetDiscovery on unknown stack should report ok=false")
	}
}

func TestImport_PreservesDiscovery(t *testing.T) {
	m, _ := discoManager()
	m.Import("shop", []byte(discoCompose))
	m.SetDiscovery("shop", true)
	// Re-import (edit the file) must not reset the discovery toggle.
	m.Import("shop", []byte(discoCompose+"  cache:\n    image: redis\n"))
	if !m.DiscoveryFlags()["shop"] {
		t.Error("re-import wiped the discovery toggle")
	}
}

func TestStackView_ExposesDiscovery(t *testing.T) {
	m, _ := discoManager()
	m.Import("shop", []byte(discoCompose))
	m.SetDiscovery("shop", true)
	v, ok, err := m.Get(context.Background(), "shop")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !v.Discovery {
		t.Error("StackView.Discovery should reflect the toggle")
	}
}

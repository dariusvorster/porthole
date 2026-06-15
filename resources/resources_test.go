package resources

import (
	"reflect"
	"testing"

	"github.com/porthole/porthole/engine"
)

func running(id, image string) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.Image.Reference = image
	c.Status.State = engine.StateRunning
	return c
}

func stopped(id, image string) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.Image.Reference = image
	c.Status.State = engine.StateStopped
	return c
}

func withVolume(c engine.Container, vol string) engine.Container {
	m := engine.Mount{Destination: "/data"}
	m.Type.Volume = &engine.VolumeMount{Name: vol}
	c.Configuration.Mounts = append(c.Configuration.Mounts, m)
	return c
}

func withNetwork(c engine.Container, net string) engine.Container {
	c.Configuration.Networks = append(c.Configuration.Networks, engine.NetworkAttach{Network: net})
	return c
}

func img(ref string) engine.Image  { return engine.Image{Reference: ref} }
func vol(name string) engine.Volume { return engine.Volume{Name: name} }

func network(name string, builtin bool) engine.Network {
	n := engine.Network{}
	n.Configuration.Name = name
	if builtin {
		n.Configuration.Labels = map[string]string{roleLabel: "builtin"}
	}
	return n
}

func findVol(a Annotated, name string) AnnotatedVolume {
	for _, v := range a.Volumes {
		if v.Name == name {
			return v
		}
	}
	return AnnotatedVolume{}
}

const anonUUID = "39dbb510-6757-4514-8b00-26b49116003d"

func TestImageInUseAdvisoryRunningOnly(t *testing.T) {
	containers := []engine.Container{
		running("web", "docker.io/library/nginx:latest"),
		stopped("old", "docker.io/library/nginx:latest"), // stopped does NOT count for images
	}
	// Declared image without explicit tag must match the :latest container ref.
	a := Annotate([]engine.Image{img("docker.io/library/nginx")}, nil, nil, containers)
	if len(a.Images) != 1 {
		t.Fatal("want 1 image")
	}
	if !reflect.DeepEqual(a.Images[0].InUseByRunning, []string{"web"}) {
		t.Errorf("inUseByRunning = %v, want [web] (stopped excluded, :latest normalized)", a.Images[0].InUseByRunning)
	}
}

func TestVolumeInUseInclStopped(t *testing.T) {
	containers := []engine.Container{withVolume(stopped("c1", "x"), "data")}
	a := Annotate(nil, []engine.Volume{vol("data"), vol("unused")}, nil, containers)
	data := findVol(a, "data")
	if !data.InUse || !reflect.DeepEqual(data.UsedBy, []string{"c1"}) {
		t.Errorf("data: inUse=%v usedBy=%v (stopped must count)", data.InUse, data.UsedBy)
	}
	if findVol(a, "unused").InUse {
		t.Error("unused volume should not be in use")
	}
}

func TestAnonymousOrphanDetection(t *testing.T) {
	// anon UUID, unreferenced → anonymous orphan.
	a := Annotate(nil, []engine.Volume{vol(anonUUID), vol("named-keep")}, nil, nil)
	anon := findVol(a, anonUUID)
	if !anon.Anonymous {
		t.Error("UUID + unreferenced should be flagged anonymous")
	}
	if findVol(a, "named-keep").Anonymous {
		t.Error("named volume must not be flagged anonymous")
	}
}

func TestAnonymousNotFlaggedWhenInUse(t *testing.T) {
	// A UUID volume still mounted is anonymous-but-not-orphaned → Anonymous=false.
	containers := []engine.Container{withVolume(running("c", "x"), anonUUID)}
	a := Annotate(nil, []engine.Volume{vol(anonUUID)}, nil, containers)
	if findVol(a, anonUUID).Anonymous {
		t.Error("in-use UUID volume is not an orphan")
	}
}

func TestNamedUUIDFalsePositiveSurfacedInPreview(t *testing.T) {
	// A user who named a volume a UUID gets flagged anonymous — but the prune
	// preview LISTS it, so the user can veto (the backstop, §5).
	a := Annotate(nil, []engine.Volume{vol(anonUUID)}, nil, nil)
	if !findVol(a, anonUUID).Anonymous {
		t.Fatal("setup: expected flag")
	}
	plan := PrunePreview("volumes", a, nil, false)
	if !reflect.DeepEqual(plan.Items, []string{anonUUID}) {
		t.Errorf("preview should list the volume for veto: %v", plan.Items)
	}
}

func TestNetworkProtectionAndMembers(t *testing.T) {
	containers := []engine.Container{withNetwork(running("web", "x"), "shop")}
	a := Annotate(nil, nil, []engine.Network{network("default", true), network("shop", false)}, containers)
	var def, shop AnnotatedNetwork
	for _, n := range a.Networks {
		if n.Configuration.Name == "default" {
			def = n
		} else {
			shop = n
		}
	}
	if !def.Protected {
		t.Error("default (builtin label) should be protected")
	}
	if shop.Protected || shop.MemberCount != 1 {
		t.Errorf("shop: protected=%v members=%d", shop.Protected, shop.MemberCount)
	}
}

func TestPrunePreviewVolumes(t *testing.T) {
	// volume prune removes ALL unreferenced — incl. the named one (over-reach).
	containers := []engine.Container{withVolume(running("c", "x"), "in-use")}
	a := Annotate(nil, []engine.Volume{vol("in-use"), vol("named-unused"), vol(anonUUID)}, nil, containers)
	plan := PrunePreview("volumes", a, nil, false)
	want := []string{anonUUID, "named-unused"} // sorted; in-use excluded
	if !reflect.DeepEqual(plan.Items, want) {
		t.Errorf("volume prune preview = %v, want %v", plan.Items, want)
	}
}

func TestPrunePreviewNetworksProtects(t *testing.T) {
	a := Annotate(nil, nil, []engine.Network{network("default", true), network("empty", false)}, nil)
	plan := PrunePreview("networks", a, nil, false)
	if !reflect.DeepEqual(plan.Items, []string{"empty"}) {
		t.Errorf("network prune preview = %v, want [empty] (builtin protected)", plan.Items)
	}
}

func TestPrunePreviewImagesAllVsDangling(t *testing.T) {
	containers := []engine.Container{running("web", "nginx:latest")}
	images := []engine.Image{img("nginx:latest"), img("redis:latest"), img("<none>:<none>")}
	a := Annotate(images, nil, nil, containers)

	all := PrunePreview("images", a, nil, true)
	if !reflect.DeepEqual(all.Items, []string{"<none>:<none>", "redis:latest"}) {
		t.Errorf("--all preview = %v (nginx in-use excluded)", all.Items)
	}
	dangling := PrunePreview("images", a, nil, false)
	if !reflect.DeepEqual(dangling.Items, []string{"<none>:<none>"}) {
		t.Errorf("dangling preview = %v, want [<none>:<none>]", dangling.Items)
	}
}

func TestPrunePreviewContainers(t *testing.T) {
	containers := []engine.Container{running("up", "x"), stopped("down1", "x"), stopped("down2", "x")}
	plan := PrunePreview("containers", Annotated{}, containers, false)
	if !reflect.DeepEqual(plan.Items, []string{"down1", "down2"}) {
		t.Errorf("container prune preview = %v, want stopped only", plan.Items)
	}
}

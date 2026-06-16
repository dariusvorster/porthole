package stacks

import (
	"testing"

	"github.com/porthole/porthole/engine"
)

func TestClassifyVolume(t *testing.T) {
	cases := []struct {
		src  string
		want VolumeKind
	}{
		{"dr-data", VolNamed},
		{"/host/path", VolBind},
		{"./rel", VolBind},
		{"", VolAnonymous},
		{"fa9802fb-50b4-43fb-bd4e-40f678851373", VolAnonymous}, // bare-UUID = anon
		{"FA9802FB-50B4-43FB-BD4E-40F678851373", VolAnonymous}, // case-insensitive
	}
	for _, c := range cases {
		if got := classifyVolume(engine.RunVolume{Source: c.src, Target: "/x"}); got != c.want {
			t.Errorf("classifyVolume(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestPlanRecreate_VolumeClassificationAndNewSpec(t *testing.T) {
	old := engine.RunSpec{
		Name:  "shop-api",
		Image: "nginx:1.0",
		Volumes: []engine.RunVolume{
			{Source: "shop-data", Target: "/data"},                                  // named → preserved
			{Source: "/etc/conf", Target: "/etc/app"},                               // bind → host
			{Source: "fa9802fb-50b4-43fb-bd4e-40f678851373", Target: "/cache"},      // anon → orphan
		},
	}
	newSpec := engine.RunSpec{Name: "shop-api", Image: "nginx:2.0", Volumes: old.Volumes}

	plan := planRecreate(old, newSpec)
	if plan.New.Image != "nginx:2.0" {
		t.Errorf("new spec image = %q, want nginx:2.0", plan.New.Image)
	}
	if plan.Noop {
		t.Error("changed image should not be a noop")
	}
	if len(plan.Volumes) != 3 {
		t.Fatalf("expected 3 volume plans, got %d", len(plan.Volumes))
	}
	kinds := map[string]VolumePlan{}
	for _, v := range plan.Volumes {
		kinds[v.Target] = v
	}
	if kinds["/data"].Kind != VolNamed || kinds["/data"].Orphaned {
		t.Errorf("/data should be named+preserved: %+v", kinds["/data"])
	}
	if kinds["/etc/app"].Kind != VolBind || kinds["/etc/app"].Orphaned {
		t.Errorf("/etc/app should be bind+preserved: %+v", kinds["/etc/app"])
	}
	if kinds["/cache"].Kind != VolAnonymous || !kinds["/cache"].Orphaned {
		t.Errorf("/cache should be anonymous+orphaned: %+v", kinds["/cache"])
	}
}

func TestPlanRecreate_Noop(t *testing.T) {
	s := engine.RunSpec{Name: "x", Image: "nginx", Volumes: []engine.RunVolume{{Source: "v", Target: "/v"}}}
	if !planRecreate(s, s).Noop {
		t.Error("identical specs should be a noop")
	}
}

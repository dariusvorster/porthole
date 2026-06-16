package engine

import (
	"strings"
	"testing"
)

func TestNetworkCreateArgs(t *testing.T) {
	// name only
	if got := strings.Join(networkCreateArgs(NetworkSpec{Name: "nw"}), " "); got != "network create nw" {
		t.Errorf("name-only = %q", got)
	}
	// full surface (set flags only, sorted maps, name last)
	got := strings.Join(networkCreateArgs(NetworkSpec{
		Name: "nw", Subnet: "10.88.0.0/24", SubnetV6: "fd00::/64", Internal: true,
		Labels: map[string]string{"env": "dev"}, Options: map[string]string{"mtu": "1400"},
	}), " ")
	for _, want := range []string{
		"--subnet 10.88.0.0/24", "--subnet-v6 fd00::/64", "--internal",
		"--label env=dev", "--option mtu=1400",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, " nw") {
		t.Errorf("name must come last: %s", got)
	}
}

func TestNetworkCreateArgs_OmitsUnset(t *testing.T) {
	got := strings.Join(networkCreateArgs(NetworkSpec{Name: "nw"}), " ")
	for _, absent := range []string{"--subnet", "--subnet-v6", "--internal", "--label", "--option"} {
		if strings.Contains(got, absent) {
			t.Errorf("unset spec should not emit %q: %s", absent, got)
		}
	}
}

func TestClassify_NetworkInUse(t *testing.T) {
	stderr := `failed to delete network: ["id": nw-use, "error": invalidState: "cannot delete subnet nw-use with referring containers: nw-c"]
Error: delete failed for one or more networks: ["nw-use"]`
	kind, msg := classify(stderr)
	if kind != ErrNetworkInUse {
		t.Fatalf("classify = %q, want network_in_use", kind)
	}
	if !strings.Contains(msg, "nw-c") {
		t.Errorf("message should name the referring container, got %q", msg)
	}
}

func TestClassify_NetworkNameConflict(t *testing.T) {
	if kind, _ := classify("Error: network nw-use already exists"); kind != ErrNameConflict {
		t.Errorf("classify = %q, want name_conflict", kind)
	}
}

func TestNetworkInUseMessage_Fallback(t *testing.T) {
	if got := networkInUseMessage("some unrelated error"); got != "network is in use by a container" {
		t.Errorf("fallback = %q", got)
	}
}

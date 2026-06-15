package discovery

import (
	"strings"
	"testing"
)

func blockOf(s string) string {
	// extract the managed block (begin..end inclusive) for focused assertions
	i := strings.Index(s, beginPrefix)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	j := strings.Index(rest, endMarker)
	if j < 0 {
		return rest
	}
	return rest[:j+len(endMarker)]
}

func TestComputeHostsBlock_BothNameFormsAndSorted(t *testing.T) {
	peers := []Member{
		{ID: "shop-web", Service: "web", Namespaced: "shop-web", IP: "192.168.64.5"},
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
	}
	got := computeHostsBlock("shop", peers)
	want := "# >>> porthole-managed (stack: shop)\n" +
		"192.168.64.3  api  shop-api\n" +
		"192.168.64.5  web  shop-web\n" +
		"# <<<"
	if got != want {
		t.Fatalf("block mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestComputeHostsBlock_SkipsPeersWithoutIP(t *testing.T) {
	peers := []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
		{ID: "shop-db", Service: "db", Namespaced: "shop-db", IP: ""}, // not running yet
	}
	got := computeHostsBlock("shop", peers)
	if strings.Contains(got, "db") {
		t.Errorf("peer without IP must be skipped, got:\n%s", got)
	}
	if !strings.Contains(got, "192.168.64.3  api  shop-api") {
		t.Errorf("running peer missing:\n%s", got)
	}
}

func TestMergeHosts_PreservesUserLinesAndAppends(t *testing.T) {
	existing := "127.0.0.1 localhost\n192.168.64.4 shop-web\n"
	block := computeHostsBlock("shop", []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
	})
	got := mergeHosts(existing, block)
	if !strings.Contains(got, "127.0.0.1 localhost") || !strings.Contains(got, "192.168.64.4 shop-web") {
		t.Errorf("user/system lines not preserved:\n%s", got)
	}
	if !strings.Contains(got, "192.168.64.3  api  shop-api") {
		t.Errorf("managed entry missing:\n%s", got)
	}
	// managed block must come after the user lines
	if strings.Index(got, "localhost") > strings.Index(got, beginPrefix) {
		t.Errorf("managed block should follow user lines:\n%s", got)
	}
}

func TestMergeHosts_Idempotent(t *testing.T) {
	existing := "127.0.0.1 localhost\n192.168.64.4 shop-web\n"
	block := computeHostsBlock("shop", []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
	})
	once := mergeHosts(existing, block)
	twice := mergeHosts(once, block)
	if once != twice {
		t.Fatalf("not idempotent:\n once=%q\ntwice=%q", once, twice)
	}
	// exactly one managed block — no growth
	if n := strings.Count(twice, beginPrefix); n != 1 {
		t.Errorf("expected 1 managed block, got %d:\n%s", n, twice)
	}
}

func TestMergeHosts_ChurnedIPOverwrites_NoStaleLine(t *testing.T) {
	existing := "127.0.0.1 localhost\n"
	old := computeHostsBlock("shop", []Member{{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"}})
	afterOld := mergeHosts(existing, old)
	// api restarts with a new IP
	newB := computeHostsBlock("shop", []Member{{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.9"}})
	afterNew := mergeHosts(afterOld, newB)
	if strings.Contains(afterNew, "192.168.64.3") {
		t.Errorf("stale IP survived re-injection:\n%s", afterNew)
	}
	if !strings.Contains(afterNew, "192.168.64.9  api  shop-api") {
		t.Errorf("new IP missing:\n%s", afterNew)
	}
	if n := strings.Count(afterNew, "api  shop-api"); n != 1 {
		t.Errorf("expected exactly one api line, got %d:\n%s", n, afterNew)
	}
}

func TestMergeHosts_EmptyBlockStrips(t *testing.T) {
	existing := "127.0.0.1 localhost\n"
	withBlock := mergeHosts(existing, computeHostsBlock("shop", []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
	}))
	stripped := mergeHosts(withBlock, "")
	if strings.Contains(stripped, beginPrefix) || strings.Contains(stripped, "shop-api") {
		t.Errorf("managed block not stripped:\n%s", stripped)
	}
	if !strings.Contains(stripped, "127.0.0.1 localhost") {
		t.Errorf("user lines lost on strip:\n%s", stripped)
	}
	// strip is idempotent too
	if again := mergeHosts(stripped, ""); again != stripped {
		t.Errorf("strip not idempotent:\n once=%q\ntwice=%q", stripped, again)
	}
}

func TestMergeHosts_ToleratesMissingEndMarker(t *testing.T) {
	// a half-written prior block (no end marker) must still be stripped, not kept
	corrupt := "127.0.0.1 localhost\n# >>> porthole-managed (stack: shop)\n10.0.0.1 api\n"
	block := computeHostsBlock("shop", []Member{{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"}})
	got := mergeHosts(corrupt, block)
	if strings.Contains(got, "10.0.0.1") {
		t.Errorf("half-written block survived:\n%s", got)
	}
	if n := strings.Count(got, beginPrefix); n != 1 {
		t.Errorf("expected 1 managed block, got %d:\n%s", n, got)
	}
}

func TestPlanInjections_SelfExcluded_AllRunningTargeted(t *testing.T) {
	members := []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
		{ID: "shop-web", Service: "web", Namespaced: "shop-web", IP: "192.168.64.5"},
	}
	plan := planInjections("shop", members, "shop-web")
	if len(plan) != 2 {
		t.Fatalf("expected 2 injections, got %d", len(plan))
	}
	// triggering member ordered first
	if plan[0].MemberID != "shop-web" {
		t.Errorf("started member should be first, got %q", plan[0].MemberID)
	}
	for _, inj := range plan {
		if strings.Contains(inj.Block, inj.MemberID) {
			t.Errorf("block for %s contains itself:\n%s", inj.MemberID, inj.Block)
		}
	}
	// api's block must contain web and vice-versa
	byID := map[string]string{}
	for _, inj := range plan {
		byID[inj.MemberID] = inj.Block
	}
	if !strings.Contains(byID["shop-api"], "web  shop-web") {
		t.Errorf("api missing web peer:\n%s", byID["shop-api"])
	}
	if !strings.Contains(byID["shop-web"], "api  shop-api") {
		t.Errorf("web missing api peer:\n%s", byID["shop-web"])
	}
}

func TestPlanInjections_PartialSet_NoIPSkippedNotErrored(t *testing.T) {
	members := []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
		{ID: "shop-db", Service: "db", Namespaced: "shop-db", IP: ""}, // still coming up
	}
	plan := planInjections("shop", members, "shop-api")
	if len(plan) != 1 || plan[0].MemberID != "shop-api" {
		t.Fatalf("only the running member should be a target, got %+v", plan)
	}
	if strings.Contains(plan[0].Block, "db") {
		t.Errorf("a peer with no IP must not appear in a block:\n%s", plan[0].Block)
	}
}

func TestPlanInjections_EmptyStack(t *testing.T) {
	if plan := planInjections("shop", nil, ""); len(plan) != 0 {
		t.Errorf("empty stack should yield no injections, got %+v", plan)
	}
	// single lone member: it's a target but its block has no peers
	plan := planInjections("shop", []Member{
		{ID: "shop-api", Service: "api", Namespaced: "shop-api", IP: "192.168.64.3"},
	}, "shop-api")
	if len(plan) != 1 {
		t.Fatalf("lone member should still be a target, got %d", len(plan))
	}
	if got := blockOf(plan[0].Block); got != "# >>> porthole-managed (stack: shop)\n# <<<" {
		t.Errorf("lone member block should be empty managed region, got %q", got)
	}
}

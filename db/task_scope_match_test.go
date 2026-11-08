package db

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestTaskScopeAssetMatchIPLiteralDomainAcrossViews guards the single
// taskScopeAssetMatchSQL predicate. An ip/cidr scope must match an asset whose
// domain column holds the IP literal while ip is empty, and every consumer
// (coverage counts, untested backlog, host candidates, coverage graph, scoped
// DSL queries) must agree on the same in-scope set. It also pins direct-source
// inclusion (a grandparent scope must not leak) and the $1=taskID / DSL-args-
// start-at-$2 placeholder convention.
func TestTaskScopeAssetMatchIPLiteralDomainAcrossViews(t *testing.T) {
	d, assets, _ := testSetup(t)
	t.Cleanup(func() { _ = d.Close() })

	grand, err := d.CreateTask("scope match grand", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("scope match source", "goal",
		TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("scope match current", "goal",
		TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(current.ID)
		_ = d.DeleteTask(source.ID)
		_ = d.DeleteTask(grand.ID)
	})

	// Build unique, /30-aligned networks inside 10.0.0.0/8 so stale rows from a
	// previous run cannot silently join this task's denominator.
	stamp := time.Now().UnixNano()
	pickBase := func(seed uint32, used ...uint32) uint32 {
		v := seed & 0x00FFFFF0
		for {
			dup := false
			for _, u := range used {
				if v == u {
					dup = true
					break
				}
			}
			if !dup {
				return v
			}
			v = (v + 0x10) & 0x00FFFFF0
		}
	}
	ipAt := func(base, off uint32) string {
		v := (base + off) & 0x00FFFFFF
		return fmt.Sprintf("10.%d.%d.%d", byte(v>>16), byte(v>>8), byte(v))
	}
	curBase := pickBase(uint32(stamp))
	srcBase := pickBase(uint32(stamp>>21), curBase)
	grandBase := pickBase(uint32(stamp>>42), curBase, srcBase)
	curScope := ipAt(curBase, 0) + "/30"
	srcScope := ipAt(srcBase, 0) + "/30"
	grandScope := ipAt(grandBase, 0) + "/30"

	inDomainIP := ipAt(curBase, 1) // in CIDR, but only domain carries the address
	inIPColumn := ipAt(curBase, 2) // in CIDR via the ip column
	inDomainUntested := ipAt(curBase, 3)
	outsideCIDR := ipAt(curBase, 4)
	srcIP := ipAt(srcBase, 1)
	grandIP := ipAt(grandBase, 1)

	for _, sc := range []struct {
		taskID int64
		value  string
	}{
		{current.ID, curScope},
		{source.ID, srcScope},
		{grand.ID, grandScope},
	} {
		if _, err := assets.AddAgentScope(sc.taskID, "cidr", sc.value, "scope match test", "agent"); err != nil {
			t.Fatalf("add scope %s: %v", sc.value, err)
		}
	}

	marker := fmt.Sprintf("scopematch%d", stamp)
	normalRoot := fmt.Sprintf("scope-match-%d.invalid", stamp)
	normalHost := "app." + normalRoot
	foreignTask := stamp // every row is owned by an unrelated task, never current/source
	insertAsset := func(typ, domain, rootDom, ip, rawURL, method, title string, port int) int64 {
		var id int64
		if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain,ip,port,url,method,page_title,task_ids,last_seen)
			VALUES ($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),NULLIF($5,0),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),ARRAY[$9]::bigint[],now())
			RETURNING id`,
			typ, domain, rootDom, ip, port, rawURL, method, title, foreignTask).Scan(&id); err != nil {
			t.Fatalf("insert %s %q: %v", typ, domain, err)
		}
		return id
	}
	inDomainID := insertAsset("endpoint", inDomainIP, inDomainIP, "", "http://"+inDomainIP+"/"+marker, "GET", marker, 8080)
	inIPColumnID := insertAsset("ip", "", "", inIPColumn, "", "", marker, 22)
	inDomainUntestedID := insertAsset("endpoint", inDomainUntested, inDomainUntested, "", "http://"+inDomainUntested+"/"+marker, "GET", marker, 9090)
	outsideID := insertAsset("endpoint", outsideCIDR, outsideCIDR, "", "http://"+outsideCIDR+"/"+marker, "GET", marker, 8080)
	normalID := insertAsset("subdomain", normalHost, normalRoot, "", "", "", marker, 0)
	srcID := insertAsset("endpoint", srcIP, srcIP, "", "http://"+srcIP+"/"+marker, "GET", marker, 8443)
	grandID := insertAsset("endpoint", grandIP, grandIP, "", "http://"+grandIP+"/"+marker, "GET", marker, 8080)
	allAssetIDs := []int64{inDomainID, inIPColumnID, inDomainUntestedID, outsideID, normalID, srcID, grandID}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs(allAssetIDs) })

	// Anchor only in-scope assets: one in the current exploration, one in the
	// direct source, and one in the grandparent (which must never count here).
	for _, anchor := range []struct {
		explorationID int64
		assetID       int64
	}{
		{current.ExplorationID, inDomainID},
		{source.ExplorationID, srcID},
		{grand.ExplorationID, grandID},
	} {
		if _, err := d.Exploration(anchor.explorationID).AddNode(KindFact,
			map[string]any{"summary": "scope match anchor"}, 0, "confirmed", "worker",
			[]int64{anchor.assetID}); err != nil {
			t.Fatal(err)
		}
	}

	assetIDSet := func(list []*Asset) map[int64]bool {
		set := make(map[int64]bool, len(list))
		for _, a := range list {
			set[a.ID] = true
		}
		return set
	}
	assertSet := func(step string, got map[int64]bool, want ...int64) {
		t.Helper()
		wantSet := make(map[int64]bool, len(want))
		for _, id := range want {
			wantSet[id] = true
		}
		if !reflect.DeepEqual(got, wantSet) {
			t.Fatalf("%s: got asset ids %v, want %v", step, got, wantSet)
		}
	}
	assertCoverage := func(step string, cov *Coverage, total, tested int, byType []CoverageByType) {
		t.Helper()
		if cov.Denominator != total || cov.Tested != tested {
			t.Fatalf("%s: denominator=%d tested=%d, want %d/%d (%+v)",
				step, cov.Denominator, cov.Tested, total, tested, cov.ByType)
		}
		if !reflect.DeepEqual(cov.ByType, byType) {
			t.Fatalf("%s: by_type=%+v, want %+v", step, cov.ByType, byType)
		}
		if total == 0 {
			if cov.Pct != nil {
				t.Fatalf("%s: empty denominator must have nil pct, got %v", step, *cov.Pct)
			}
		} else if cov.Pct == nil || *cov.Pct != float64(tested)/float64(total) {
			t.Fatalf("%s: pct=%v, want %d/%d", step, cov.Pct, tested, total)
		}
	}
	untestedIDSet := func(list []CoverageAsset) map[int64]bool {
		set := make(map[int64]bool, len(list))
		for _, a := range list {
			set[a.ID] = true
		}
		return set
	}
	wantUntested := func(step string, list []CoverageAsset, total int, want ...int64) {
		t.Helper()
		wantSet := make(map[int64]bool, len(want))
		for _, id := range want {
			wantSet[id] = true
		}
		if total != len(wantSet) || !reflect.DeepEqual(untestedIDSet(list), wantSet) {
			t.Fatalf("%s: total=%d list=%+v, want total %d ids %v", step, total, list, len(wantSet), wantSet)
		}
	}

	// Current task only: the CIDR matches both endpoints by domain (ip empty)
	// and the ip asset by column. The ordinary subdomain, the out-of-CIDR
	// endpoint, the direct source and the grandparent assets are all absent.
	cov, err := assets.TaskCoverage(current.ID, current.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage("TaskCoverage", cov, 3, 1, []CoverageByType{
		{Type: "endpoint", Total: 2, Tested: 1},
		{Type: "ip", Total: 1, Tested: 0},
	})

	untested, total, err := assets.ListUntestedAssets(current.ID, current.ExplorationID, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantUntested("ListUntestedAssets", untested, total, inDomainUntestedID, inIPColumnID)
	for _, tc := range []struct {
		typ   string
		total int
		ids   []int64
	}{
		{"ip", 1, []int64{inIPColumnID}},
		{"endpoint", 1, []int64{inDomainUntestedID}},
	} {
		list, n, err := assets.ListUntestedAssets(current.ID, current.ExplorationID, tc.typ, 10, 0)
		if err != nil {
			t.Fatalf("ListUntestedAssets(%s): %v", tc.typ, err)
		}
		if n != tc.total || len(list) != len(tc.ids) {
			t.Fatalf("ListUntestedAssets(%s): total=%d list=%+v, want total=%d ids=%v",
				tc.typ, n, list, tc.total, tc.ids)
		}
		for i, id := range tc.ids {
			if list[i].ID != id {
				t.Fatalf("ListUntestedAssets(%s): got id %d, want %d", tc.typ, list[i].ID, id)
			}
		}
	}

	// Direct-source view: source scope and source fact anchor join; the
	// grandparent scope and anchor do not.
	covSrc, err := assets.TaskCoverageWithSources(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage("TaskCoverageWithSources", covSrc, 4, 2, []CoverageByType{
		{Type: "endpoint", Total: 3, Tested: 2},
		{Type: "ip", Total: 1, Tested: 0},
	})

	srcUntested, srcTotal, err := assets.ListUntestedAssetsWithSources(current.ID, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantUntested("ListUntestedAssetsWithSources", srcUntested, srcTotal, inDomainUntestedID, inIPColumnID)
	if list, n, err := assets.ListUntestedAssetsWithSources(current.ID, "endpoint", 10, 0); err != nil || n != 1 || len(list) != 1 || list[0].ID != inDomainUntestedID {
		t.Fatalf("ListUntestedAssetsWithSources(endpoint): total=%d list=%+v err=%v, want only %d", n, list, err, inDomainUntestedID)
	}
	if list, n, err := assets.ListUntestedAssetsWithSources(current.ID, "ip", 10, 0); err != nil || n != 1 || len(list) != 1 || list[0].ID != inIPColumnID {
		t.Fatalf("ListUntestedAssetsWithSources(ip): total=%d list=%+v err=%v, want only %d", n, list, err, inIPColumnID)
	}

	// Host candidates must surface the IP-literal domain rows, the ip column and
	// the direct source row, and nothing out of scope.
	hosts, err := assets.HostsByTaskWithSources(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantHosts := []string{inDomainIP, inDomainUntested, inIPColumn, srcIP}
	sort.Strings(wantHosts)
	if !reflect.DeepEqual(hosts, wantHosts) {
		t.Fatalf("HostsByTaskWithSources: got %v, want %v", hosts, wantHosts)
	}

	// Coverage graph: the same three rows are in scope; only current/source fact
	// anchors are tested.
	graph, err := assets.BuildCoverageGraph(current.ID, current.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	graphIDs := map[int64]bool{}
	testedByID := map[int64]bool{}
	inScopeByID := map[int64]bool{}
	for _, node := range graph.Nodes {
		if node.AssetID == 0 {
			continue
		}
		graphIDs[node.AssetID] = true
		if node.Tested {
			testedByID[node.AssetID] = true
		}
		if node.InScope {
			inScopeByID[node.AssetID] = true
		}
	}
	assertSet("BuildCoverageGraph assets", graphIDs, inDomainID, inDomainUntestedID, inIPColumnID, srcID)
	assertSet("BuildCoverageGraph in-scope assets", inScopeByID, inDomainID, inDomainUntestedID, inIPColumnID, srcID)
	if !testedByID[inDomainID] || !testedByID[srcID] || testedByID[inDomainUntestedID] || testedByID[inIPColumnID] {
		t.Fatalf("BuildCoverageGraph tested flags: %v", testedByID)
	}

	// Scoped list_assets path: membership and $1=taskID with DSL args from $2.
	scopeAssets, err := assets.QueryDSLInScope(marker, "", current.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertSet("QueryDSLInScope marker", assetIDSet(scopeAssets), inDomainID, inDomainUntestedID, inIPColumnID, srcID)
	// The scoped list path, coverage denominator and graph must describe the
	// same direct-source membership.
	if len(scopeAssets) != covSrc.Denominator || len(graphIDs) != covSrc.Denominator {
		t.Fatalf("cross-view membership differs: dsl=%d graph=%d coverage=%d",
			len(scopeAssets), len(graphIDs), covSrc.Denominator)
	}
	portAssets, err := assets.QueryDSLInScope("port>=8000", "", current.ID, 50, 0)
	if err != nil {
		t.Fatalf("QueryDSLInScope port filter: %v", err)
	}
	assertSet("QueryDSLInScope port filter", assetIDSet(portAssets), inDomainID, inDomainUntestedID, srcID)
	portEndpointAssets, err := assets.QueryDSLInScope("port>=8000", "endpoint", current.ID, 50, 0)
	if err != nil {
		t.Fatalf("QueryDSLInScope port+type filter: %v", err)
	}
	assertSet("QueryDSLInScope port+type filter", assetIDSet(portEndpointAssets), inDomainID, inDomainUntestedID, srcID)
	ipAssets, err := assets.QueryDSLInScope(marker, "ip", current.ID, 50, 0)
	if err != nil {
		t.Fatalf("QueryDSLInScope type filter: %v", err)
	}
	assertSet("QueryDSLInScope type filter", assetIDSet(ipAssets), inIPColumnID)

	byIDs, err := assets.GetByIDsInScope(current.ID, allAssetIDs)
	if err != nil {
		t.Fatal(err)
	}
	assertSet("GetByIDsInScope", assetIDSet(byIDs), inDomainID, inDomainUntestedID, inIPColumnID, srcID)
}

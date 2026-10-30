package db

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildTaskTopologyEntriesDoNotInventRelations(t *testing.T) {
	assets := []*Asset{
		{ID: 1, Type: "ip", IP: "10.0.0.1", OpenPorts: []map[string]any{{"port": 80, "service": "http"}}},
		{ID: 2, Type: "ip", IP: "10.0.0.2"},
		{ID: 3, Type: "ip", IP: "203.0.113.10"},
		{ID: 4, Type: "subdomain", Domain: "www.example.test", RecordType: "A", RecordValue: []string{"203.0.113.10"}},
		{ID: 5, Type: "ip", IP: "198.51.100.20"},
	}
	findings := []*DBFinding{{ID: 9, VulnClass: "RCE", Severity: "high", AssetIDs: []int64{4}}}
	g := buildTaskTopology(assets, findings, map[string]TaskTopologyOverride{}, nil)
	if len(g.Nodes) != 3 || g.RootKey != "203.0.113.10" {
		t.Fatalf("public non-entry must be omitted and entry retained: %+v", g)
	}
	for _, n := range g.Nodes {
		if n.IP == "203.0.113.10" && (n.EntryStatus != "suspected" || len(n.EvidenceRefs) != 1 || n.EvidenceRefs[0] != "finding:9") {
			t.Fatalf("entry evidence missing: %+v", n)
		}
	}
	if len(g.Edges) != 0 {
		t.Fatalf("an entry/root must not invent relations to unrelated nodes: %+v", g.Edges)
	}
}

func TestBuildTaskTopologyWithoutEntryKeepsIndependentRealNodes(t *testing.T) {
	g := buildTaskTopology([]*Asset{{ID: 1, Type: "ip", IP: "10.0.0.1"}, {ID: 2, Type: "ip", IP: "10.0.0.2"}}, nil, nil, nil)
	if g.RootKey != "" || len(g.Edges) != 0 || len(g.Nodes) != 2 {
		t.Fatalf("missing entry evidence must not invent a root or relations: %+v", g)
	}
	for _, node := range g.Nodes {
		if node.IP == "entry:unknown" {
			t.Fatalf("virtual node must never appear in topology data: %+v", g.Nodes)
		}
	}
}

func TestTaskTopologyManualRelationAndNotePersistence(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	tk, err := d.CreateTask("拓扑持久化测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	taskIDs := []int64{tk.ID}
	a := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.23.0.1", "task_ids": taskIDs})
	b := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.23.0.2", "task_ids": taskIDs})
	defer d.Assets().DeleteByIDs([]int64{a, b}) //nolint:errcheck
	g, err := d.UpdateTaskTopologyNode(tk.ID, TaskTopologyOverride{
		NodeIP: "10.23.0.2", RelationLabel: "  通过共享运维终端访问  ", ParentIP: "10.23.0.1", DisplayLabel: "  核心数据库  ",
	}, "ARTEX")
	if err != nil {
		t.Fatal(err)
	}
	manual := 0
	for _, edge := range g.Edges {
		if edge.Manual && edge.Source == "10.23.0.1" && edge.Target == "10.23.0.2" &&
			edge.Label == "通过共享运维终端访问" && edge.EvidenceRef == "" {
			manual++
		}
	}
	if manual != 1 {
		t.Fatalf("free-text manual edge must not require human evidence: %+v", g.Edges)
	}
	labelFound := false
	for _, node := range g.Nodes {
		if node.IP == "10.23.0.2" && node.DisplayLabel == "核心数据库" {
			labelFound = true
		}
	}
	if !labelFound {
		t.Fatalf("display label was not trimmed and persisted: %+v", g.Nodes)
	}
	if _, err := d.UpdateTaskTopologyNode(tk.ID, TaskTopologyOverride{NodeIP: "10.23.0.2", DisplayLabel: strings.Repeat("x", 121)}, "ARTEX"); err == nil {
		t.Fatal("display label longer than 120 bytes must fail")
	}
	if _, err := d.UpdateTaskTopologyNode(tk.ID, TaskTopologyOverride{NodeIP: "10.23.0.2", RelationLabel: strings.Repeat("x", 201), ParentIP: "10.23.0.1"}, "ARTEX"); err == nil {
		t.Fatal("relation label longer than 200 bytes must fail")
	}
	if _, err := d.UpdateTaskTopologyNode(tk.ID, TaskTopologyOverride{NodeIP: "10.23.0.2", RelationLabel: "缺少上游"}, "ARTEX"); err == nil {
		t.Fatal("relation text without a parent must fail")
	}
	if _, err := d.UpdateTaskTopologyNode(tk.ID, TaskTopologyOverride{NodeIP: "10.23.0.1", RelationLabel: "反向连接", ParentIP: "10.23.0.2"}, "ARTEX"); err == nil {
		t.Fatal("cycle must be rejected")
	}
	g, err = d.AddTaskTopologyNote(tk.ID, "10.23.0.2", "人工确认关系", "ARTEX")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Notes["10.23.0.2"]) != 1 || g.Notes["10.23.0.2"][0].Body != "人工确认关系" {
		t.Fatalf("note not persisted and returned for node rendering: %+v", g.Notes)
	}
}

func TestBuildTaskTopologyKeepsOnlyExplicitRelations(t *testing.T) {
	assets := []*Asset{{ID: 1, Type: "ip", IP: "10.0.0.1"}, {ID: 2, Type: "ip", IP: "10.0.0.2"}, {ID: 3, Type: "ip", IP: "10.0.0.3"}}
	findings := []*DBFinding{{ID: 9, VulnClass: "getshell", Severity: "critical", AssetIDs: []int64{1}}}
	overrides := map[string]TaskTopologyOverride{"10.0.0.2": {NodeIP: "10.0.0.2", ParentIP: "10.0.0.1", RelationKind: "unknown", RelationLabel: "人工确认的管理通道"}}
	g := buildTaskTopology(assets, findings, overrides, nil)
	if len(g.Edges) != 1 || g.Edges[0].Target != "10.0.0.2" || g.Edges[0].Label != "人工确认的管理通道" {
		t.Fatalf("only the explicit relation may be present: %+v", g.Edges)
	}
}

func TestTaskTopologyAgentRelationRequiresEvidence(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	if _, err := d.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	category, err := d.GetTaskCategoryByNKey("内网渗透")
	if err != nil || category == nil {
		t.Fatalf("internal category: %v", err)
	}
	tk, err := d.CreateTaskWithOptions("Agent拓扑关系测试", "目标", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	taskIDs := []int64{tk.ID}
	a := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.24.0.1", "task_ids": taskIDs})
	b := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.24.0.2", "task_ids": taskIDs})
	c := seedTreeAsset(t, d, "ip", map[string]any{"ip": "10.24.0.3", "task_ids": taskIDs})
	defer d.Assets().DeleteByIDs([]int64{a, b, c}) //nolint:errcheck
	factID, err := d.Exploration(tk.ExplorationID).AddNode(KindFact, map[string]any{"summary": "路由实测"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := d.RecordTaskTopologyRelation(tk.ID, TaskTopologyOverride{
		ParentIP: "10.24.0.1", NodeIP: "10.24.0.2", RelationLabel: "经实测路由到达", EvidenceRef: "fact:" + fmt.Sprint(factID),
	}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 1 || g.Edges[0].Manual || g.Edges[0].Kind != "evidence" || g.Edges[0].EvidenceRef != "fact:"+fmt.Sprint(factID) {
		t.Fatalf("agent evidence relation: %+v", g.Edges)
	}
	if _, err := d.RecordTaskTopologyRelation(tk.ID, TaskTopologyOverride{
		ParentIP: "10.24.0.1", NodeIP: "10.24.0.3", RelationLabel: "无证据猜测", EvidenceRef: "fact:999999999",
	}, "worker"); err == nil {
		t.Fatal("missing evidence must be rejected")
	}
}

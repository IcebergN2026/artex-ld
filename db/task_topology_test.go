package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeTopologyRelationRejectsIncompletePayload(t *testing.T) {
	valid := func() TopologyRelationInput {
		return TopologyRelationInput{SrcIP: " 10.30.0.1 ", DstIP: " 10.30.0.2 ", Label: "  经实测路由到达  ", EvidenceRef: " fact:7 "}
	}
	got, err := normalizeTopologyRelation(valid())
	if err != nil {
		t.Fatal(err)
	}
	if got.SrcIP != "10.30.0.1" || got.DstIP != "10.30.0.2" || got.Label != "经实测路由到达" || got.EvidenceRef != "fact:7" {
		t.Fatalf("fields must be trimmed: %+v", got)
	}
	for name, mutate := range map[string]func(*TopologyRelationInput){
		"same endpoint": func(r *TopologyRelationInput) { r.DstIP = r.SrcIP },
		"not an ip":     func(r *TopologyRelationInput) { r.DstIP = "internal-host" },
		"empty label":   func(r *TopologyRelationInput) { r.Label = "   " },
		"long label":    func(r *TopologyRelationInput) { r.Label = strings.Repeat("x", topologyMaxLabelRunes+1) },
		"no evidence":   func(r *TopologyRelationInput) { r.EvidenceRef = "" },
		"long evidence": func(r *TopologyRelationInput) { r.EvidenceRef = strings.Repeat("x", topologyMaxEvidenceRunes+1) },
	} {
		rel := valid()
		mutate(&rel)
		if _, err := normalizeTopologyRelation(rel); err == nil {
			t.Fatalf("%s must be rejected: %+v", name, rel)
		}
	}
}

func TestTaskTopologyAgentRelationWritesSolidEvidenceEdge(t *testing.T) {
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
	factID, err := d.Exploration(tk.ExplorationID).AddNode(KindFact, map[string]any{"summary": "路由实测"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	evidence := "fact:" + fmt.Sprint(factID)
	g, err := d.RecordTaskTopologyRelation(tk.ID, TopologyRelationInput{
		SrcIP: "10.24.0.1", DstIP: "10.24.0.2", Label: "经实测路由到达", EvidenceRef: evidence,
	}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	// The edge itself puts both endpoints in the graph (no orphan invariant).
	for _, ip := range []string{"10.24.0.1", "10.24.0.2"} {
		if !topologyHasNode(g.Nodes, ip) {
			t.Fatalf("endpoint %s missing from graph: %+v", ip, g.Nodes)
		}
	}
	if len(g.Edges) != 1 {
		t.Fatalf("agent evidence relation: %+v", g.Edges)
	}
	edge := g.Edges[0]
	if edge.SrcIP != "10.24.0.1" || edge.DstIP != "10.24.0.2" || edge.Kind != topologyKindSolid ||
		edge.Label != "经实测路由到达" || edge.EvidenceRef != evidence {
		t.Fatalf("agent evidence relation: %+v", edge)
	}
	if _, err := d.RecordTaskTopologyRelation(tk.ID, TopologyRelationInput{
		SrcIP: "10.24.0.1", DstIP: "10.24.0.3", Label: "无证据猜测", EvidenceRef: "fact:999999999",
	}, "worker"); err == nil {
		t.Fatal("missing evidence must be rejected")
	}
	if _, err := d.RecordTaskTopologyRelation(tk.ID, TopologyRelationInput{
		SrcIP: "10.24.0.2", DstIP: "10.24.0.1", Label: "反向连接", EvidenceRef: evidence,
	}, "worker"); err == nil {
		t.Fatal("cycle must be rejected")
	}
}

func TestMigrateTaskTopologyOverridesMovesLegacyRows(t *testing.T) {
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
	tk, err := d.CreateTaskWithOptions("旧拓扑迁移测试", "目标", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	// Recreate the legacy projection table exactly as the old schema.sql did.
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS task_topology_overrides (
    task_id       BIGINT NOT NULL,
    node_ip       TEXT NOT NULL,
    relation_kind TEXT NOT NULL DEFAULT '',
    relation_label TEXT NOT NULL DEFAULT '',
    parent_ip     TEXT NOT NULL DEFAULT '',
    evidence_ref  TEXT NOT NULL DEFAULT '',
    entry_status  TEXT NOT NULL DEFAULT '',
    display_label TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, node_ip)
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_topology_overrides(task_id,node_ip,parent_ip,entry_status,display_label,relation_label) VALUES
	($1,'10.31.0.2','10.31.0.1','confirmed','核心数据库',''),
	($1,'10.31.0.3','','','',''),
	($1,'10.31.0.4','10.31.0.9','','','闲置主机')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck
	if err := migrateTaskTopologyOverrides(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var legacy bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('task_topology_overrides') IS NOT NULL`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("legacy table must be dropped after the migration")
	}
	if err := migrateTaskTopologyOverrides(ctx, conn); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
	g, err := d.TaskTopologyEditor(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"10.31.0.1", "10.31.0.2", "10.31.0.3", "10.31.0.4", "10.31.0.9"} {
		if !topologyHasNode(g.Nodes, ip) {
			t.Fatalf("migrated node %s missing: %+v", ip, g.Nodes)
		}
	}
	findEdge := func(src, dst string) *TopologyEdge {
		for i := range g.Edges {
			if g.Edges[i].SrcIP == src && g.Edges[i].DstIP == dst {
				return &g.Edges[i]
			}
		}
		return nil
	}
	if edge := findEdge("10.31.0.1", "10.31.0.2"); edge == nil || edge.Kind != topologyKindSolid {
		t.Fatalf("entry row must migrate to a solid edge: %+v", g.Edges)
	}
	if edge := findEdge("10.31.0.9", "10.31.0.4"); edge == nil || edge.Kind != topologyKindReachable || edge.Label != "闲置主机" {
		t.Fatalf("non-entry row must migrate to a reachable edge keeping its label: %+v", g.Edges)
	}
	// A parent-less legacy row must not stay orphaned in the editor model.
	if edge := findEdge("10.31.0.2", "10.31.0.3"); edge == nil || edge.Kind != topologyKindReachable || edge.Label != TopologyFallbackLabel {
		t.Fatalf("orphan repair must add the fallback reachable edge: %+v", g.Edges)
	}
}

// TestMigrateTaskTopologyOverridesFillsMissingLegacyColumns covers a database that
// created task_topology_overrides before relation_label/display_label existed:
// schema.sql used to add those columns, so the migration must restore them itself
// instead of aborting startup on a missing column.
func TestMigrateTaskTopologyOverridesFillsMissingLegacyColumns(t *testing.T) {
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
	tk, err := d.CreateTaskWithOptions("旧拓扑缺列迁移测试", "目标", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	// The pre-relation_label/display_label shape of the legacy table.
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS task_topology_overrides (
    task_id      BIGINT NOT NULL,
    node_ip      TEXT NOT NULL,
    relation_kind TEXT NOT NULL DEFAULT '',
    parent_ip    TEXT NOT NULL DEFAULT '',
    evidence_ref TEXT NOT NULL DEFAULT '',
    entry_status TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, node_ip)
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_topology_overrides(task_id,node_ip,parent_ip,entry_status) VALUES
	($1,'10.32.0.2','10.32.0.1','confirmed')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck
	if err := migrateTaskTopologyOverrides(ctx, conn); err != nil {
		t.Fatal(err)
	}
	g, err := d.TaskTopologyEditor(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"10.32.0.1", "10.32.0.2"} {
		if !topologyHasNode(g.Nodes, ip) {
			t.Fatalf("migrated node %s missing: %+v", ip, g.Nodes)
		}
	}
	if len(g.Edges) != 1 || g.Edges[0].SrcIP != "10.32.0.1" || g.Edges[0].DstIP != "10.32.0.2" || g.Edges[0].Kind != topologyKindSolid {
		t.Fatalf("entry row must migrate to a solid edge: %+v", g.Edges)
	}
}

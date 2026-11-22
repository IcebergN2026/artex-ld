package db

import (
	"fmt"
	"testing"
)

func TestParseTopologyFact(t *testing.T) {
	if got := ParseTopologyFact(map[string]any{"summary": "x"}); got != nil {
		t.Fatalf("no topology key must be nil: %+v", got)
	}
	if got := ParseTopologyFact(map[string]any{"topology": map[string]any{"dst_ips": []any{"10.0.0.2"}}}); got != nil {
		t.Fatalf("missing src_ip must be nil: %+v", got)
	}
	if got := ParseTopologyFact(map[string]any{"topology": map[string]any{"src_ip": "10.0.0.1"}}); got != nil {
		t.Fatalf("empty dst_ips must be nil: %+v", got)
	}
	got := ParseTopologyFact(map[string]any{"topology": map[string]any{
		"discovery": " nmap 扫到 445 开放 ",
		"src_ip":    " 192.168.10.1 ",
		"dst_ips":   []any{"192.168.10.2", "  ", "192.168.10.3"},
		"kind":      "solid",
	}})
	if got == nil {
		t.Fatal("valid topology must parse")
	}
	if got.SrcIP != "192.168.10.1" || got.Discovery != " nmap 扫到 445 开放 " || got.Kind != "solid" {
		t.Fatalf("fields: %+v", got)
	}
	if len(got.DstIPs) != 2 || got.DstIPs[0] != "192.168.10.2" || got.DstIPs[1] != "192.168.10.3" {
		t.Fatalf("dst_ips must be trimmed and non-empty only: %+v", got.DstIPs)
	}
}

func TestBuildEdgesFromFact(t *testing.T) {
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
	tk, err := d.CreateTaskWithOptions("fact拓扑建边测试", "目标", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	payload := map[string]any{
		"summary": "eth0 侧发现两台存活主机",
		"topology": map[string]any{
			"discovery": "nmap 扫到 445 开放",
			"src_ip":    "192.168.10.1",
			// .2/.3 正常建边；.1 自环跳过；8.8.8.8 非内网跳过
			"dst_ips": []any{"192.168.10.2", "192.168.10.3", "192.168.10.1", "8.8.8.8"},
		},
	}
	factID, err := d.Exploration(tk.ExplorationID).AddNode(KindFact, payload, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.BuildEdgesFromFact(tk.ID, factID, payload, "worker")

	g, err := d.TaskTopologyEditor(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence := "fact:" + fmt.Sprint(factID)
	want := map[string]bool{"192.168.10.2": false, "192.168.10.3": false}
	for _, e := range g.Edges {
		if e.SrcIP != "192.168.10.1" {
			t.Fatalf("unexpected src: %+v", e)
		}
		if _, ok := want[e.DstIP]; !ok {
			t.Fatalf("self-loop/public dst must be skipped, got: %+v", e)
		}
		if e.Label != "nmap 扫到 445 开放" || e.Kind != "reachable" || e.EvidenceRef != evidence {
			t.Fatalf("edge fields must derive from fact topology: %+v", e)
		}
		if e.SystemLabel != evidence {
			t.Fatalf("system_label must be marked at insert so boot rewrite skips it: %+v", e)
		}
		want[e.DstIP] = true
	}
	for dst, seen := range want {
		if !seen {
			t.Fatalf("missing edge to %s; edges=%+v", dst, g.Edges)
		}
	}
	// 节点随边 upsert（无游离节点）。
	for _, ip := range []string{"192.168.10.1", "192.168.10.2", "192.168.10.3"} {
		if !topologyHasNode(g.Nodes, ip) {
			t.Fatalf("node %s must exist; nodes=%+v", ip, g.Nodes)
		}
	}

	// 无 topology 的纯 fact 不建边。
	plainID, err := d.Exploration(tk.ExplorationID).AddNode(KindFact, map[string]any{"summary": "普通事实"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.BuildEdgesFromFact(tk.ID, plainID, map[string]any{"summary": "普通事实"}, "worker")
	g2, err := d.TaskTopologyEditor(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g2.Edges) != len(g.Edges) {
		t.Fatalf("plain fact must not create edges: before=%d after=%d", len(g.Edges), len(g2.Edges))
	}
}

package db

import (
	"sort"
	"strings"
	"testing"
)

func topologyTestEdges(pairs ...[3]string) []TopologyEdge {
	out := make([]TopologyEdge, 0, len(pairs))
	for _, p := range pairs {
		kind := p[2]
		if kind == "" {
			kind = topologyKindReachable
		}
		out = append(out, TopologyEdge{SrcIP: p[0], DstIP: p[1], Kind: kind})
	}
	return out
}

func topologyTestEdgeKeys(edges []TopologyEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.SrcIP+"->"+e.DstIP+":"+e.Kind)
	}
	sort.Strings(out)
	return out
}

func TestTopologyWouldCycle(t *testing.T) {
	cases := []struct {
		name  string
		edges []TopologyEdge
		src   string
		dst   string
		want  bool
	}{
		{name: "自环", edges: nil, src: "10.0.0.1", dst: "10.0.0.1", want: true},
		{name: "反向直连成环", edges: topologyTestEdges([3]string{"10.0.0.2", "10.0.0.1", "solid"}), src: "10.0.0.1", dst: "10.0.0.2", want: true},
		{name: "多路径成环", edges: topologyTestEdges(
			[3]string{"10.0.0.2", "10.0.0.3", ""},
			[3]string{"10.0.0.3", "10.0.0.4", ""},
			[3]string{"10.0.0.4", "10.0.0.1", ""},
		), src: "10.0.0.1", dst: "10.0.0.2", want: true},
		{name: "菱形不成环", edges: topologyTestEdges(
			[3]string{"a", "b", ""},
			[3]string{"a", "c", ""},
			[3]string{"b", "d", ""},
			[3]string{"c", "d", ""},
		), src: "b", dst: "c", want: false},
		{name: "顺向延伸不成环", edges: topologyTestEdges([3]string{"10.0.0.1", "10.0.0.2", "solid"}), src: "10.0.0.2", dst: "10.0.0.3", want: false},
		{name: "空图不成环", edges: nil, src: "10.0.0.1", dst: "10.0.0.2", want: false},
		{name: "缺端点不成环", edges: nil, src: "", dst: "10.0.0.2", want: false},
	}
	for _, c := range cases {
		if got := topologyWouldCycle(c.edges, c.src, c.dst); got != c.want {
			t.Errorf("%s: topologyWouldCycle(%v, %s→%s)=%v, want %v", c.name, topologyTestEdgeKeys(c.edges), c.src, c.dst, got, c.want)
		}
	}
}

func TestTopologyEntryIP(t *testing.T) {
	nodes := []TopologyNode{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}, {IP: "10.0.0.3"}}
	if got := topologyEntryIP(nil, nil); got != "" {
		t.Errorf("空图入口=%q, want empty", got)
	}
	// 显式入口节点优先，即使它排在后面。
	withEntry := []TopologyNode{{IP: "10.0.0.1"}, {IP: "10.0.0.2", EntryStatus: TopologyEntrySuspected}, {IP: "10.0.0.3"}}
	if got := topologyEntryIP(withEntry, nil); got != "10.0.0.2" {
		t.Errorf("入口节点=%q, want 10.0.0.2", got)
	}
	// 无入口时取第一个没有任何边指向的节点。
	edges := topologyTestEdges([3]string{"10.0.0.2", "10.0.0.1", "solid"}, [3]string{"10.0.0.2", "10.0.0.3", "solid"})
	if got := topologyEntryIP(nodes, edges); got != "10.0.0.2" {
		t.Errorf("根候选=%q, want 10.0.0.2", got)
	}
	// 全部都有入边时退回第一个节点。
	cycle := topologyTestEdges([3]string{"10.0.0.1", "10.0.0.2", ""}, [3]string{"10.0.0.2", "10.0.0.3", ""}, [3]string{"10.0.0.3", "10.0.0.1", ""})
	if got := topologyEntryIP(nodes, cycle); got != "10.0.0.1" {
		t.Errorf("兜底入口=%q, want 10.0.0.1", got)
	}
}

func TestTopologyOrphanRepairs(t *testing.T) {
	cases := []struct {
		name  string
		nodes []TopologyNode
		edges []TopologyEdge
		want  []string
	}{
		{
			name:  "删最后一条边改成待确认虚线连入口",
			nodes: []TopologyNode{{IP: "10.0.0.1", EntryStatus: TopologyEntrySuspected}, {IP: "10.0.0.2"}},
			edges: nil,
			want:  []string{"10.0.0.1->10.0.0.2:reachable"},
		},
		{
			name:  "无入口时连第一个节点",
			nodes: []TopologyNode{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}},
			edges: nil,
			want:  []string{"10.0.0.1->10.0.0.2:reachable"},
		},
		{
			name:  "仍有连线的节点不修补",
			nodes: []TopologyNode{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}},
			edges: topologyTestEdges([3]string{"10.0.0.1", "10.0.0.2", "solid"}),
			want:  nil,
		},
		{
			name:  "多个孤儿各自连入口",
			nodes: []TopologyNode{{IP: "10.0.0.1", EntryStatus: TopologyEntrySuspected}, {IP: "10.0.0.2"}, {IP: "10.0.0.3"}},
			edges: nil,
			want:  []string{"10.0.0.1->10.0.0.2:reachable", "10.0.0.1->10.0.0.3:reachable"},
		},
		{
			name:  "入口自己成孤儿时连到剩余节点",
			nodes: []TopologyNode{{IP: "10.0.0.1", EntryStatus: TopologyEntryConfirmed}, {IP: "10.0.0.2"}, {IP: "10.0.0.3"}},
			edges: topologyTestEdges([3]string{"10.0.0.2", "10.0.0.3", "solid"}),
			want:  []string{"10.0.0.1->10.0.0.2:reachable"},
		},
		{
			name:  "单节点图无第二个端点",
			nodes: []TopologyNode{{IP: "10.0.0.1", EntryStatus: TopologyEntrySuspected}},
			edges: nil,
			want:  nil,
		},
		{
			name:  "已有同款待确认边时不重复补齐",
			nodes: []TopologyNode{{IP: "10.0.0.1", EntryStatus: TopologyEntrySuspected}, {IP: "10.0.0.2"}},
			edges: topologyTestEdges([3]string{"10.0.0.1", "10.0.0.2", topologyKindReachable}),
			want:  nil,
		},
	}
	for _, c := range cases {
		got := topologyOrphanRepairs(c.nodes, c.edges)
		keys := topologyTestEdgeKeys(got)
		if len(keys) != len(c.want) {
			t.Fatalf("%s: 修补边=%v, want %v", c.name, keys, c.want)
		}
		for i := range keys {
			if keys[i] != c.want[i] {
				t.Fatalf("%s: 修补边=%v, want %v", c.name, keys, c.want)
			}
		}
		for _, e := range got {
			if e.Label != TopologyFallbackLabel {
				t.Errorf("%s: 修补边标签=%q, want %q", c.name, e.Label, TopologyFallbackLabel)
			}
			if topologyWouldCycle(c.edges, e.SrcIP, e.DstIP) {
				t.Errorf("%s: 修补边 %s→%s 会成环", c.name, e.SrcIP, e.DstIP)
			}
		}
	}
}

func TestTopologyInternalIPv4(t *testing.T) {
	internal := []string{"10.0.0.1", "10.255.255.254", "172.16.0.1", "172.31.255.254", "192.168.0.1", "192.168.255.254", " 10.1.2.3 "}
	for _, ip := range internal {
		if !topologyInternalIPv4(ip) {
			t.Errorf("%q 应判定为内网", ip)
		}
	}
	external := []string{
		"8.8.8.8", "172.15.255.255", "172.32.0.1", "192.169.0.1", "11.0.0.1",
		"::1", "2001:db8::1", "fd00::1", "", "not-an-ip", "10.0.0.256",
	}
	for _, ip := range external {
		if topologyInternalIPv4(ip) {
			t.Errorf("%q 不应判定为内网", ip)
		}
	}
}

func TestPlanTopologySeed(t *testing.T) {
	ips := []string{"10.0.0.2", "10.0.0.1", "8.8.8.8", "2001:db8::1", "192.168.1.10", "10.0.0.1"}
	findings := []topologySeedFinding{
		{ID: 7, Class: "getshell", Name: "命令执行", Severity: "critical", IPs: []string{"10.0.0.2", "10.0.0.1"}},
		{ID: 8, Class: "xss", Name: "无关漏洞", Severity: "high", IPs: []string{"10.0.0.2", "192.168.1.10"}},
		// 同一对节点上的第二条入口类 finding：推导出同一 (src,dst,solid) 三元组，必须去重。
		{ID: 9, Class: "rce", Severity: "critical", IPs: []string{"10.0.0.1", "10.0.0.2"}},
	}
	nodes, edges := planTopologySeed(ips, findings)

	gotIPs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		gotIPs = append(gotIPs, n.IP)
	}
	wantIPs := []string{"10.0.0.1", "10.0.0.2", "192.168.1.10"}
	if len(gotIPs) != len(wantIPs) {
		t.Fatalf("种子节点=%v, want %v", gotIPs, wantIPs)
	}
	for i := range wantIPs {
		if gotIPs[i] != wantIPs[i] {
			t.Fatalf("种子节点=%v, want %v", gotIPs, wantIPs)
		}
	}
	for _, n := range nodes {
		wantSuspected := n.IP != "192.168.1.10"
		if wantSuspected && n.EntryStatus != TopologyEntrySuspected {
			t.Errorf("节点 %s 入口状态=%q, want suspected", n.IP, n.EntryStatus)
		}
		if !wantSuspected && n.EntryStatus != "" {
			t.Errorf("节点 %s 入口状态=%q, want empty", n.IP, n.EntryStatus)
		}
	}

	keys := topologyTestEdgeKeys(edges)
	wantKeys := []string{
		"10.0.0.1->10.0.0.2:solid",
		"10.0.0.1->192.168.1.10:reachable",
	}
	if len(keys) != len(wantKeys) {
		t.Fatalf("种子边=%v, want %v", keys, wantKeys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("种子边=%v, want %v", keys, wantKeys)
		}
	}
	// 种子不允许出现游离节点（单节点图除外）。
	for _, n := range nodes {
		if topologyDegree(edges, n.IP) == 0 {
			t.Errorf("种子节点 %s 是游离节点", n.IP)
		}
	}
	for _, e := range edges {
		if e.SrcIP == e.DstIP {
			t.Errorf("种子出现自环 %s", e.SrcIP)
		}
	}
	if got := topologyEntryIP(nodes, edges); got != "10.0.0.1" {
		t.Errorf("种子入口=%q, want 10.0.0.1", got)
	}
}

func TestNormalizeTopologyColor(t *testing.T) {
	got, err := normalizeTopologyColor("  #2563eb  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "#2563eb" {
		t.Fatalf("颜色未去空白: %q", got)
	}
	// 空串 = 回到默认配色，合法。
	if got, err := normalizeTopologyColor("   "); err != nil || got != "" {
		t.Fatalf("空颜色应规范为空串: %q, %v", got, err)
	}
	// 恰好 32 个字符允许，33 个拒绝；按 rune 计数。
	if got, err := normalizeTopologyColor(strings.Repeat("x", topologyMaxColorRunes)); err != nil || len(got) != topologyMaxColorRunes {
		t.Fatalf("32 字符颜色应通过: %q, %v", got, err)
	}
	if _, err := normalizeTopologyColor(strings.Repeat("x", topologyMaxColorRunes+1)); err == nil {
		t.Fatal("33 字符颜色必须被拒绝")
	}
}

func TestPatchTopologyColorRoundtrip(t *testing.T) {
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
	tk, err := d.CreateTaskWithOptions("拓扑颜色测试", "目标", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	// 首节点独占空图；次节点带一条 solid 边。
	if _, err := d.AddTopologyNode(tk.ID, "ARTEX", TopologyNode{IP: "10.40.0.1"}, TopologyEdge{}); err != nil {
		t.Fatal(err)
	}
	g, err := d.AddTopologyNode(tk.ID, "ARTEX", TopologyNode{IP: "10.40.0.2"}, TopologyEdge{SrcIP: "10.40.0.1", DstIP: "10.40.0.2", Kind: topologyKindSolid})
	if err != nil {
		t.Fatal(err)
	}
	// 新建节点/连线默认颜色为空串。
	for _, n := range g.Nodes {
		if n.Color != "" {
			t.Fatalf("节点 %s 默认颜色=%q, want empty", n.IP, n.Color)
		}
	}
	if len(g.Edges) != 1 || g.Edges[0].Color != "" {
		t.Fatalf("连线默认颜色应为空: %+v", g.Edges)
	}
	edgeID := g.Edges[0].ID

	// PATCH 节点颜色（带空白）→ GET 图回读。
	nodeColor := " #e11d48 "
	g, err = d.PatchTopologyNode(tk.ID, "10.40.0.1", "ARTEX", TopologyNodePatch{Color: &nodeColor})
	if err != nil {
		t.Fatal(err)
	}
	g, err = d.TaskTopologyEditor(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range g.Nodes {
		if n.IP == "10.40.0.1" {
			found = true
			if n.Color != "#e11d48" {
				t.Fatalf("节点颜色=%q, want #e11d48", n.Color)
			}
		}
	}
	if !found {
		t.Fatal("节点 10.40.0.1 不在图中")
	}

	// PATCH 连线颜色；随后只改标签，颜色必须保留（整行 UPDATE 不回退）。
	edgeColor := "#2563eb"
	if _, err := d.PatchTopologyEdge(tk.ID, edgeID, "ARTEX", TopologyEdgePatch{Color: &edgeColor}); err != nil {
		t.Fatal(err)
	}
	label := "经实测路由到达"
	g, err = d.PatchTopologyEdge(tk.ID, edgeID, "ARTEX", TopologyEdgePatch{Label: &label})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 1 || g.Edges[0].Color != "#2563eb" || g.Edges[0].Label != label {
		t.Fatalf("连线颜色/标签=%+v, want color=#2563eb label=%q", g.Edges[0], label)
	}

	// PATCH color="" 重置回默认配色。
	reset := ""
	g, err = d.PatchTopologyNode(tk.ID, "10.40.0.1", "ARTEX", TopologyNodePatch{Color: &reset})
	if err != nil {
		t.Fatal(err)
	}
	g, err = d.PatchTopologyEdge(tk.ID, edgeID, "ARTEX", TopologyEdgePatch{Color: &reset})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.IP == "10.40.0.1" && n.Color != "" {
			t.Fatalf("重置后节点颜色=%q, want empty", n.Color)
		}
	}
	if len(g.Edges) != 1 || g.Edges[0].Color != "" {
		t.Fatalf("重置后连线颜色=%+v, want empty", g.Edges)
	}

	// 超长颜色拒绝。
	long := strings.Repeat("x", topologyMaxColorRunes+1)
	if _, err := d.PatchTopologyNode(tk.ID, "10.40.0.1", "ARTEX", TopologyNodePatch{Color: &long}); err == nil {
		t.Fatal("超长节点颜色必须被拒绝")
	}
	if _, err := d.PatchTopologyEdge(tk.ID, edgeID, "ARTEX", TopologyEdgePatch{Color: &long}); err == nil {
		t.Fatal("超长连线颜色必须被拒绝")
	}
}

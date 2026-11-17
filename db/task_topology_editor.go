// task_topology_editor.go：任务拓扑图编辑（节点/边/分组/备注）的持久化与规范化。
// 主要函数/类型：
//   - TopologyNode / TopologyEdge / TopologyGroup / TopologyGraph：拓扑图数据结构。
//   - TaskTopologyEditor：读取任务拓扑图。
//   - AddTopologyNode / PatchTopologyNode / DeleteTopologyNode：节点增改删。
//   - AddTopologyEdge / PatchTopologyEdge / DeleteTopologyEdge：边增改删。
//   - UpsertTopologyGroup / DeleteTopologyGroup / AddTopologyNote：分组与节点备注维护。
//   - SeedTopologyFromAssets：按任务资产自动播种拓扑。

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// 内网拓扑编辑器（阶段 A）。
//
// 图 = task_topology_nodes / task_topology_edges / task_topology_groups 的忠实投影，
// 备注沿用 task_topology_notes（node_ip 键语义不变）。旧读时投影接口
// （task_topology_overrides + 读时计算）已下线，存量数据由
// db/migrate_topology.go 一次性搬入这三张表。
//
// 这里强制三条不变式：
//  1. 无游离节点：新增节点与首条边同事务写入；删边/删节点后对失去全部连线的节点
//     自动降级为到入口的 pending 虚线（标签"发现方式待确认"）。
//  2. 无自环、无环：写边前用 topologyWouldCycle 在事务内校验。
//  3. 节点必须在图中：改边/删边/加备注前校验 IP 属于该任务拓扑。
const (
	topologyKindSolid     = "solid"
	topologyKindReachable = "reachable"

	TopologyEntrySuspected = "suspected"
	TopologyEntryConfirmed = "confirmed"

	// TopologyFallbackLabel 是自动降级虚线（发现方式待确认）的固定文案。
	TopologyFallbackLabel = "发现方式待确认"

	topologyMaxHostnameRunes    = 255
	topologyMaxDisplayRunes     = 120
	topologyMaxDescriptionRunes = 500
	topologyMaxLabelRunes       = 200
	topologyMaxEvidenceRunes    = 500
	topologyMaxGroupRunes       = 120
	topologyMaxNoteRunes        = 4000
	topologyMaxColorRunes       = 32
)

// TopologyGroup is one group box (网段/区域大框) in the editable graph.
type TopologyGroup struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	X         float64   `json:"x"`
	Y         float64   `json:"y"`
	Width     float64   `json:"width"`
	Height    float64   `json:"height"`
	Color     string    `json:"color"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TopologyNode is one host in the editable graph. GroupID is nil when the node
// is not inside a group box.
type TopologyNode struct {
	IP           string    `json:"ip"`
	Hostname     string    `json:"hostname"`
	DisplayLabel string    `json:"display_label"`
	GroupID      *int64    `json:"group_id"`
	X            float64   `json:"x"`
	Y            float64   `json:"y"`
	EntryStatus  string    `json:"entry_status"`
	Color        string    `json:"color"`
	Description  string    `json:"description"`
	UpdatedBy    string    `json:"updated_by"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TopologyEdge is one relation between two nodes. Kind is one of solid /
// reachable (see topologyKind* constants).
type TopologyEdge struct {
	ID          int64     `json:"id"`
	SrcIP       string    `json:"src_ip"`
	DstIP       string    `json:"dst_ip"`
	Label       string    `json:"label"`
	Kind        string    `json:"kind"`
	EvidenceRef string    `json:"evidence_ref"`
	Color       string    `json:"color"`
	SystemLabel string    `json:"system_label"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TopologyGraph is the whole editable graph of one task.
type TopologyGraph struct {
	Nodes  []TopologyNode                `json:"nodes"`
	Edges  []TopologyEdge                `json:"edges"`
	Groups []TopologyGroup               `json:"groups"`
	Notes  map[string][]TaskTopologyNote `json:"notes"`
}

// TaskTopologyNote is one note attached to a node IP of a task.
type TaskTopologyNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// TopologyNodePatch carries the editable node fields. A nil pointer means
// "leave unchanged"; ClearGroup moves the node out of its group box.
type TopologyNodePatch struct {
	Hostname     *string
	DisplayLabel *string
	X            *float64
	Y            *float64
	GroupID      *int64
	ClearGroup   bool
	EntryStatus  *string
	Color        *string
	Description  *string
}

// TopologyEdgePatch carries the editable edge fields. A nil pointer means
// "leave unchanged".
type TopologyEdgePatch struct {
	SrcIP *string
	DstIP *string
	Label *string
	Kind  *string
	Color *string
}

// topologyStore is the subset of *sql.DB / *sql.Tx the editor needs, so every
// read used for validation runs in the same transaction as the write it guards.
type topologyStore interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// ---------------------------------------------------------------------------
// 纯函数：成环检测 / 入口选择 / 孤儿修补 / 私网过滤
// ---------------------------------------------------------------------------

// topologyWouldCycle reports whether adding src→dst to edges would close a
// cycle (a self-loop always does). Incomplete endpoints are the caller's
// problem: they cannot close a cycle, so they report false.
func topologyWouldCycle(edges []TopologyEdge, src, dst string) bool {
	if src == "" || dst == "" {
		return false
	}
	if src == dst {
		return true
	}
	next := make(map[string][]string, len(edges))
	for _, e := range edges {
		next[e.SrcIP] = append(next[e.SrcIP], e.DstIP)
	}
	// src→dst closes a cycle exactly when dst can already reach src.
	seen := map[string]bool{dst: true}
	stack := []string{dst}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == src {
			return true
		}
		for _, n := range next[cur] {
			if !seen[n] {
				seen[n] = true
				stack = append(stack, n)
			}
		}
	}
	return false
}

// topologyEdgeKey identifies an edge by its unique-column triple.
func topologyEdgeKey(e TopologyEdge) string {
	return e.SrcIP + "\x00" + e.DstIP + "\x00" + e.Kind
}

// topologyDegree counts edges incident to ip.
func topologyDegree(edges []TopologyEdge, ip string) int {
	n := 0
	for _, e := range edges {
		if e.SrcIP == ip || e.DstIP == ip {
			n++
		}
	}
	return n
}

// topologyEntryIP picks the node a fallback edge starts from: the first explicit
// entry node (entry_status 非空), else the first node nothing points at, else
// the first node.
func topologyEntryIP(nodes []TopologyNode, edges []TopologyEdge) string {
	if len(nodes) == 0 {
		return ""
	}
	for _, n := range nodes {
		if n.EntryStatus != "" {
			return n.IP
		}
	}
	targeted := make(map[string]bool, len(edges))
	for _, e := range edges {
		targeted[e.DstIP] = true
	}
	for _, n := range nodes {
		if !targeted[n.IP] {
			return n.IP
		}
	}
	return nodes[0].IP
}

// topologyOrphanRepairs returns the pending fallback edges needed to keep every
// node connected: one entry→node pending edge per node without any incident edge.
// An orphaned entry node connects to another node instead (a single-node graph
// has no second endpoint and stays alone).
func topologyOrphanRepairs(nodes []TopologyNode, edges []TopologyEdge) []TopologyEdge {
	degree := make(map[string]int, len(nodes))
	existing := make(map[string]bool, len(edges))
	for _, e := range edges {
		degree[e.SrcIP]++
		degree[e.DstIP]++
		existing[topologyEdgeKey(e)] = true
	}
	entry := topologyEntryIP(nodes, edges)
	out := []TopologyEdge{}
	add := func(src, dst string) bool {
		if src == "" || dst == "" || src == dst {
			return false
		}
		edge := TopologyEdge{SrcIP: src, DstIP: dst, Kind: topologyKindReachable, Label: TopologyFallbackLabel}
		if existing[topologyEdgeKey(edge)] {
			return false
		}
		existing[topologyEdgeKey(edge)] = true
		out = append(out, edge)
		degree[src]++
		degree[dst]++
		return true
	}
	// 先修普通孤儿：目标节点本来无边，加 entry→node 不可能成环。
	for _, n := range nodes {
		if degree[n.IP] != 0 || n.IP == entry || entry == "" {
			continue
		}
		add(entry, n.IP)
	}
	// 再修入口自身：入口无边，加 entry→any 也不可能成环。
	if entry != "" && degree[entry] == 0 {
		for _, n := range nodes {
			if n.IP != entry && add(entry, n.IP) {
				break
			}
		}
	}
	return out
}

// topologyInternalRanges 是种子阶段认定的内网私网段（RFC1918）。
var topologyInternalRanges = []*net.IPNet{
	{IP: net.IPv4(10, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(172, 16, 0, 0).To4(), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0).To4(), Mask: net.CIDRMask(16, 32)},
}

// topologyInternalIPv4 reports whether ip is in 10/8, 172.16/12 or 192.168/16.
// Public addresses and IPv6 return false: seeds only materialise internal IPv4 hosts.
func topologyInternalIPv4(ip string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	v4 := parsed.To4()
	if v4 == nil {
		return false
	}
	for _, r := range topologyInternalRanges {
		if r.Contains(v4) {
			return true
		}
	}
	return false
}

// topologySeedFinding is the finding slice the seed planner needs.
type topologySeedFinding struct {
	ID       int64
	Class    string
	Name     string
	Severity string
	IPs      []string
}

func topologySeedRelevant(f topologySeedFinding) bool {
	return topologyEntryClass(f.Class) && (f.Severity == "high" || f.Severity == "critical")
}

// topologyEntryClass reports whether a vuln class can be an entry point.
func topologyEntryClass(class string) bool {
	switch strings.ToLower(strings.ReplaceAll(class, "-", "_")) {
	case "getshell", "rce", "file_upload", "deserialization":
		return true
	default:
		return false
	}
}

// planTopologySeed builds the initial graph: internal IPv4 assets become nodes,
// high/critical getshell/RCE findings link their IPs with solid edges, and every
// remaining node gets the pending fallback edge to the entry (no orphan).
func planTopologySeed(ips []string, findings []topologySeedFinding) ([]TopologyNode, []TopologyEdge) {
	internal := make([]string, 0, len(ips))
	seenIP := make(map[string]bool, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" || seenIP[ip] || !topologyInternalIPv4(ip) {
			continue
		}
		seenIP[ip] = true
		internal = append(internal, ip)
	}
	sort.Strings(internal)
	if len(internal) == 0 {
		return []TopologyNode{}, []TopologyEdge{}
	}
	solid := []TopologyEdge{}
	seenEdge := map[string]bool{}
	for _, f := range findings {
		if !topologySeedRelevant(f) {
			continue
		}
		targets := make([]string, 0, len(f.IPs))
		for _, ip := range f.IPs {
			ip = strings.TrimSpace(ip)
			if seenIP[ip] {
				targets = append(targets, ip)
			}
		}
		sort.Strings(targets)
		if len(targets) < 2 {
			continue
		}
		label := strings.TrimSpace(f.Name)
		if label == "" {
			label = strings.TrimSpace(f.Class)
		}
		for _, dst := range targets[1:] {
			e := TopologyEdge{SrcIP: targets[0], DstIP: dst, Kind: topologyKindSolid, Label: label, EvidenceRef: fmt.Sprintf("finding:%d", f.ID)}
			key := topologyEdgeKey(e)
			if seenEdge[key] || e.SrcIP == e.DstIP {
				continue
			}
			seenEdge[key] = true
			solid = append(solid, e)
		}
	}
	nodes := make([]TopologyNode, 0, len(internal))
	for _, ip := range internal {
		nodes = append(nodes, TopologyNode{IP: ip})
	}
	for _, f := range findings {
		if !topologySeedRelevant(f) {
			continue
		}
		for _, ip := range f.IPs {
			ip = strings.TrimSpace(ip)
			for i := range nodes {
				if nodes[i].IP == ip && nodes[i].EntryStatus == "" {
					nodes[i].EntryStatus = TopologyEntrySuspected
				}
			}
		}
	}
	edges := append(solid, topologyOrphanRepairs(nodes, solid)...)
	return nodes, edges
}

// ---------------------------------------------------------------------------
// 校验小工具
// ---------------------------------------------------------------------------

func topologyFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// normalizeTopologyColor 规范节点/连线的自定义颜色：去首尾空白、封顶 32 个字符。
// 空串表示回到默认配色（节点灰蓝边框、连线退回线型颜色），故不做格式校验——
// 前端约束为 hex，后端只保证长度与留白卫生。
func normalizeTopologyColor(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > topologyMaxColorRunes {
		return "", fmt.Errorf("颜色最多 %d 个字符", topologyMaxColorRunes)
	}
	return s, nil
}

func validTopologyEdgeKind(kind string) bool {
	switch kind {
	case topologyKindSolid, topologyKindReachable:
		return true
	}
	return false
}

func topologyHasNode(nodes []TopologyNode, ip string) bool {
	for _, n := range nodes {
		if n.IP == ip {
			return true
		}
	}
	return false
}

func topologyHasEdge(edges []TopologyEdge, e TopologyEdge) bool {
	key := topologyEdgeKey(e)
	for _, existing := range edges {
		if topologyEdgeKey(existing) == key {
			return true
		}
	}
	return false
}

func normalizeTopologyNode(n TopologyNode) (TopologyNode, error) {
	n.IP = strings.TrimSpace(n.IP)
	if net.ParseIP(n.IP) == nil {
		return n, fmt.Errorf("节点 IP 无效")
	}
	n.Hostname = strings.TrimSpace(n.Hostname)
	n.DisplayLabel = strings.TrimSpace(n.DisplayLabel)
	n.EntryStatus = strings.TrimSpace(n.EntryStatus)
	if utf8.RuneCountInString(n.Hostname) > topologyMaxHostnameRunes {
		return n, fmt.Errorf("主机名最多 %d 个字符", topologyMaxHostnameRunes)
	}
	if utf8.RuneCountInString(n.DisplayLabel) > topologyMaxDisplayRunes {
		return n, fmt.Errorf("显示名最多 %d 个字符", topologyMaxDisplayRunes)
	}
	n.Description = strings.TrimSpace(n.Description)
	if utf8.RuneCountInString(n.Description) > topologyMaxDescriptionRunes {
		return n, fmt.Errorf("节点描述最多 %d 个字符", topologyMaxDescriptionRunes)
	}
	switch n.EntryStatus {
	case "", TopologyEntrySuspected, TopologyEntryConfirmed:
	default:
		return n, fmt.Errorf("入口状态只支持 suspected / confirmed / 空")
	}
	if !topologyFinite(n.X) || !topologyFinite(n.Y) {
		return n, fmt.Errorf("节点坐标无效")
	}
	return n, nil
}

// topologyBreakLabel 是发现漏洞突破边的固定格式（种子与存量迁移共用）。
func topologyBreakLabel(vulnName string) string {
	name := strings.TrimSpace(vulnName)
	if name == "" {
		return "通过漏洞突破"
	}
	return "通过" + truncateTopologyRunes(name, 20) + "突破"
}

func truncateTopologyRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func normalizeTopologyEdge(e TopologyEdge) (TopologyEdge, error) {
	e.SrcIP = strings.TrimSpace(e.SrcIP)
	e.DstIP = strings.TrimSpace(e.DstIP)
	e.Label = strings.TrimSpace(e.Label)
	e.Kind = strings.TrimSpace(e.Kind)
	e.EvidenceRef = strings.TrimSpace(e.EvidenceRef)
	if e.Kind == "" {
		e.Kind = topologyKindReachable
	}
	if !validTopologyEdgeKind(e.Kind) {
		return e, fmt.Errorf("线型只支持 solid / reachable")
	}
	if utf8.RuneCountInString(e.Label) > topologyMaxLabelRunes {
		return e, fmt.Errorf("连线标签最多 %d 个字符", topologyMaxLabelRunes)
	}
	if utf8.RuneCountInString(e.EvidenceRef) > topologyMaxEvidenceRunes {
		return e, fmt.Errorf("证据引用最多 %d 个字符", topologyMaxEvidenceRunes)
	}
	if e.Kind == topologyKindReachable && e.Label == "" {
		e.Label = TopologyFallbackLabel
	}
	return e, nil
}

func topologyUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

func loadTopologyGroups(q topologyStore, taskID int64) ([]TopologyGroup, error) {
	rows, err := q.Query(`SELECT id,name,x,y,width,height,color,updated_by,updated_at
FROM task_topology_groups WHERE task_id=$1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopologyGroup{}
	for rows.Next() {
		var g TopologyGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.X, &g.Y, &g.Width, &g.Height, &g.Color, &g.UpdatedBy, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func loadTopologyNodes(q topologyStore, taskID int64) ([]TopologyNode, error) {
	rows, err := q.Query(`SELECT ip,hostname,display_label,group_id,x,y,entry_status,color,description,updated_by,updated_at
FROM task_topology_nodes WHERE task_id=$1 ORDER BY ip`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopologyNode{}
	for rows.Next() {
		var n TopologyNode
		var groupID sql.NullInt64
		if err := rows.Scan(&n.IP, &n.Hostname, &n.DisplayLabel, &groupID, &n.X, &n.Y, &n.EntryStatus, &n.Color, &n.Description, &n.UpdatedBy, &n.UpdatedAt); err != nil {
			return nil, err
		}
		if groupID.Valid {
			id := groupID.Int64
			n.GroupID = &id
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func loadTopologyEdges(q topologyStore, taskID int64) ([]TopologyEdge, error) {
	rows, err := q.Query(`SELECT id,src_ip,dst_ip,label,kind,evidence_ref,color,system_label,updated_by,updated_at
FROM task_topology_edges WHERE task_id=$1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopologyEdge{}
	for rows.Next() {
		var e TopologyEdge
		if err := rows.Scan(&e.ID, &e.SrcIP, &e.DstIP, &e.Label, &e.Kind, &e.EvidenceRef, &e.Color, &e.SystemLabel, &e.UpdatedBy, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func loadTopologyNotes(q topologyStore, taskID int64) (map[string][]TaskTopologyNote, error) {
	rows, err := q.Query(`SELECT id,node_ip,body,created_by,created_at
FROM task_topology_notes WHERE task_id=$1 ORDER BY created_at DESC,id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := map[string][]TaskTopologyNote{}
	for rows.Next() {
		var ip string
		var n TaskTopologyNote
		if err := rows.Scan(&n.ID, &ip, &n.Body, &n.CreatedBy, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes[ip] = append(notes[ip], n)
	}
	return notes, rows.Err()
}

func loadTopologyGraph(q topologyStore, taskID int64) (*TopologyGraph, error) {
	g := &TopologyGraph{Nodes: []TopologyNode{}, Edges: []TopologyEdge{}, Groups: []TopologyGroup{}}
	var err error
	if g.Groups, err = loadTopologyGroups(q, taskID); err != nil {
		return nil, err
	}
	if g.Nodes, err = loadTopologyNodes(q, taskID); err != nil {
		return nil, err
	}
	if g.Edges, err = loadTopologyEdges(q, taskID); err != nil {
		return nil, err
	}
	if g.Notes, err = loadTopologyNotes(q, taskID); err != nil {
		return nil, err
	}
	return g, nil
}

// TaskTopologyEditor reads the whole editable graph of one task.
func (d *DB) TaskTopologyEditor(taskID int64) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	return loadTopologyGraph(d, taskID)
}

// insertTopologyEdge writes one edge, mapping the (task_id,src_ip,dst_ip,kind)
// unique constraint to a readable Chinese error.
func insertTopologyEdge(q topologyStore, taskID int64, actor string, e TopologyEdge) error {
	_, err := q.Exec(`INSERT INTO task_topology_edges(task_id,src_ip,dst_ip,label,kind,evidence_ref,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7)`, taskID, e.SrcIP, e.DstIP, e.Label, e.Kind, e.EvidenceRef, actor)
	if err != nil && topologyUniqueViolation(err) {
		return fmt.Errorf("该联系已存在（同一来源/目标/线型）")
	}
	return err
}

func insertTopologyRepairs(q topologyStore, taskID int64, actor string, repairs []TopologyEdge) error {
	for _, e := range repairs {
		if err := insertTopologyEdge(q, taskID, actor, e); err != nil {
			return err
		}
	}
	return nil
}

// AddTopologyNode inserts a node together with its first edge in one
// transaction. An empty firstEdge.SrcIP resolves to the graph entry (an entry
// node, else the first node); the very first node of an empty graph is created
// alone because no other endpoint exists yet.
func (d *DB) AddTopologyNode(taskID int64, actor string, node TopologyNode, firstEdge TopologyEdge) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	node, err := normalizeTopologyNode(node)
	if err != nil {
		return nil, err
	}
	edge, err := normalizeTopologyEdge(firstEdge)
	if err != nil {
		return nil, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	if topologyHasNode(g.Nodes, node.IP) {
		return nil, fmt.Errorf("节点 %s 已在拓扑中", node.IP)
	}
	if node.GroupID != nil && !topologyGroupKnown(g, *node.GroupID) {
		return nil, fmt.Errorf("分组不存在")
	}
	if _, err := tx.Exec(`INSERT INTO task_topology_nodes(task_id,ip,hostname,display_label,group_id,x,y,entry_status,color,description,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, taskID, node.IP, node.Hostname, node.DisplayLabel, node.GroupID, node.X, node.Y, node.EntryStatus, node.Color, node.Description, actor); err != nil {
		return nil, err
	}
	// 首条边固定指向新节点；来源留空时兜底连入口（pending 虚线）。
	if edge.DstIP == "" {
		edge.DstIP = node.IP
	}
	if edge.DstIP != node.IP {
		return nil, fmt.Errorf("首条边必须连到新增节点")
	}
	if edge.SrcIP == "" {
		edge.SrcIP = topologyEntryIP(g.Nodes, g.Edges)
	}
	if edge.SrcIP != "" {
		if !topologyHasNode(g.Nodes, edge.SrcIP) {
			return nil, fmt.Errorf("来源节点 %s 不在拓扑中", edge.SrcIP)
		}
		if edge.SrcIP == edge.DstIP {
			return nil, fmt.Errorf("不能连接到自身")
		}
		if topologyWouldCycle(g.Edges, edge.SrcIP, edge.DstIP) {
			return nil, fmt.Errorf("该联系会形成环")
		}
		if err := insertTopologyEdge(tx, taskID, actor, edge); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// PatchTopologyNode edits node content, position, group membership or the
// entry confirmation state (suspected→confirmed, or cleared).
func (d *DB) PatchTopologyNode(taskID int64, ip, actor string, patch TopologyNodePatch) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("节点 IP 无效")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	current := -1
	for i, n := range g.Nodes {
		if n.IP == ip {
			current = i
			break
		}
	}
	if current < 0 {
		return nil, fmt.Errorf("节点 %s 不在拓扑中", ip)
	}
	sets := []string{}
	args := []any{taskID, ip}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	if patch.Hostname != nil {
		hostname := strings.TrimSpace(*patch.Hostname)
		if utf8.RuneCountInString(hostname) > topologyMaxHostnameRunes {
			return nil, fmt.Errorf("主机名最多 %d 个字符", topologyMaxHostnameRunes)
		}
		set("hostname", hostname)
	}
	if patch.DisplayLabel != nil {
		label := strings.TrimSpace(*patch.DisplayLabel)
		if utf8.RuneCountInString(label) > topologyMaxDisplayRunes {
			return nil, fmt.Errorf("显示名最多 %d 个字符", topologyMaxDisplayRunes)
		}
		set("display_label", label)
	}
	if patch.X != nil {
		if !topologyFinite(*patch.X) {
			return nil, fmt.Errorf("节点坐标无效")
		}
		set("x", *patch.X)
	}
	if patch.Y != nil {
		if !topologyFinite(*patch.Y) {
			return nil, fmt.Errorf("节点坐标无效")
		}
		set("y", *patch.Y)
	}
	if patch.ClearGroup {
		sets = append(sets, "group_id=NULL")
	} else if patch.GroupID != nil {
		if !topologyGroupKnown(g, *patch.GroupID) {
			return nil, fmt.Errorf("分组不存在")
		}
		set("group_id", *patch.GroupID)
	}
	if patch.EntryStatus != nil {
		next := strings.TrimSpace(*patch.EntryStatus)
		cur := g.Nodes[current].EntryStatus
		switch {
		case next == cur:
		case next == TopologyEntryConfirmed && cur == TopologyEntrySuspected:
		case next == "":
		default:
			return nil, fmt.Errorf("入口状态只允许从疑似改为确认，或清空")
		}
		set("entry_status", next)
	}
	if patch.Color != nil {
		color, err := normalizeTopologyColor(*patch.Color)
		if err != nil {
			return nil, err
		}
		set("color", color)
	}
	if patch.Description != nil {
		desc := strings.TrimSpace(*patch.Description)
		if utf8.RuneCountInString(desc) > topologyMaxDescriptionRunes {
			return nil, fmt.Errorf("节点描述最多 %d 个字符", topologyMaxDescriptionRunes)
		}
		set("description", desc)
	}
	if len(sets) == 0 {
		return g, nil
	}
	args = append(args, actor)
	sets = append(sets, fmt.Sprintf("updated_by=$%d", len(args)))
	sets = append(sets, "updated_at=now()")
	query := `UPDATE task_topology_nodes SET ` + strings.Join(sets, ",") + ` WHERE task_id=$1 AND ip=$2`
	if _, err := tx.Exec(query, args...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// DeleteTopologyNode removes a node, its edges and its notes, then repairs every
// node that lost its last edge.
func (d *DB) DeleteTopologyNode(taskID int64, ip, actor string) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("节点 IP 无效")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	if !topologyHasNode(g.Nodes, ip) {
		return nil, fmt.Errorf("节点 %s 不在拓扑中", ip)
	}
	if _, err := tx.Exec(`DELETE FROM task_topology_edges WHERE task_id=$1 AND (src_ip=$2 OR dst_ip=$2)`, taskID, ip); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM task_topology_nodes WHERE task_id=$1 AND ip=$2`, taskID, ip); err != nil {
		return nil, err
	}
	// 节点备注以节点存在为前提（AddTopologyNote 校验），节点删除后一并清理，
	// 避免同一 IP 再次上图时带回历史备注。
	if _, err := tx.Exec(`DELETE FROM task_topology_notes WHERE task_id=$1 AND node_ip=$2`, taskID, ip); err != nil {
		return nil, err
	}
	if err := repairTopologyOrphans(tx, taskID, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// repairTopologyOrphans re-reads the graph inside tx and inserts the pending
// fallback edges for nodes that lost every edge.
func repairTopologyOrphans(tx *sql.Tx, taskID int64, actor string) error {
	nodes, err := loadTopologyNodes(tx, taskID)
	if err != nil {
		return err
	}
	edges, err := loadTopologyEdges(tx, taskID)
	if err != nil {
		return err
	}
	return insertTopologyRepairs(tx, taskID, actor, topologyOrphanRepairs(nodes, edges))
}

// AddTopologyEdge inserts one relation after validating both endpoints, the
// self-loop ban, the cycle ban and the unique (src,dst,kind) triple.
func (d *DB) AddTopologyEdge(taskID int64, actor string, edge TopologyEdge) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	edge, err := normalizeTopologyEdge(edge)
	if err != nil {
		return nil, err
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("图中还没有节点，无法建立联系")
	}
	if edge.SrcIP == "" {
		edge.SrcIP = topologyEntryIP(g.Nodes, g.Edges)
	}
	if !topologyHasNode(g.Nodes, edge.SrcIP) {
		return nil, fmt.Errorf("来源节点 %s 不在拓扑中", edge.SrcIP)
	}
	if !topologyHasNode(g.Nodes, edge.DstIP) {
		return nil, fmt.Errorf("目标节点 %s 不在拓扑中", edge.DstIP)
	}
	if edge.SrcIP == edge.DstIP {
		return nil, fmt.Errorf("不能连接到自身")
	}
	if topologyWouldCycle(g.Edges, edge.SrcIP, edge.DstIP) {
		return nil, fmt.Errorf("该联系会形成环")
	}
	if topologyHasEdge(g.Edges, edge) {
		return nil, fmt.Errorf("该联系已存在（同一来源/目标/线型）")
	}
	if err := insertTopologyEdge(tx, taskID, actor, edge); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// PatchTopologyEdge edits one edge. Endpoint changes re-run the self-loop,
// cycle and uniqueness checks; an edge that removes a node's last connection
// triggers the orphan repair.
func (d *DB) PatchTopologyEdge(taskID, edgeID int64, actor string, patch TopologyEdgePatch) (*TopologyGraph, error) {
	if taskID <= 0 || edgeID <= 0 {
		return nil, fmt.Errorf("task id and edge id are required")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	current := -1
	for i, e := range g.Edges {
		if e.ID == edgeID {
			current = i
			break
		}
	}
	if current < 0 {
		return nil, fmt.Errorf("联系 %d 不在拓扑中", edgeID)
	}
	next := g.Edges[current]
	if patch.SrcIP != nil {
		next.SrcIP = strings.TrimSpace(*patch.SrcIP)
	}
	if patch.DstIP != nil {
		next.DstIP = strings.TrimSpace(*patch.DstIP)
	}
	if patch.Label != nil {
		next.Label = strings.TrimSpace(*patch.Label)
	}
	if patch.Kind != nil {
		next.Kind = strings.TrimSpace(*patch.Kind)
	}
	if patch.Color != nil {
		color, err := normalizeTopologyColor(*patch.Color)
		if err != nil {
			return nil, err
		}
		next.Color = color
	}
	normalized, err := normalizeTopologyEdge(next)
	if err != nil {
		return nil, err
	}
	next = normalized
	if !topologyHasNode(g.Nodes, next.SrcIP) {
		return nil, fmt.Errorf("来源节点 %s 不在拓扑中", next.SrcIP)
	}
	if !topologyHasNode(g.Nodes, next.DstIP) {
		return nil, fmt.Errorf("目标节点 %s 不在拓扑中", next.DstIP)
	}
	if next.SrcIP == next.DstIP {
		return nil, fmt.Errorf("不能连接到自身")
	}
	others := make([]TopologyEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if e.ID != edgeID {
			others = append(others, e)
		}
	}
	if topologyWouldCycle(others, next.SrcIP, next.DstIP) {
		return nil, fmt.Errorf("该联系会形成环")
	}
	if topologyHasEdge(others, next) {
		return nil, fmt.Errorf("该联系已存在（同一来源/目标/线型）")
	}
	if _, err := tx.Exec(`UPDATE task_topology_edges SET src_ip=$2,dst_ip=$3,label=$4,kind=$5,color=$6,updated_by=$7,updated_at=now()
WHERE task_id=$1 AND id=$8`, taskID, next.SrcIP, next.DstIP, next.Label, next.Kind, next.Color, actor, edgeID); err != nil {
		if topologyUniqueViolation(err) {
			return nil, fmt.Errorf("该联系已存在（同一来源/目标/线型）")
		}
		return nil, err
	}
	if err := repairTopologyOrphans(tx, taskID, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// DeleteTopologyEdge removes one edge and repairs the endpoints that lost their
// last connection (pending dashed edge to the entry, not a deletion).
func (d *DB) DeleteTopologyEdge(taskID, edgeID int64, actor string) (*TopologyGraph, error) {
	if taskID <= 0 || edgeID <= 0 {
		return nil, fmt.Errorf("task id and edge id are required")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	edges, err := loadTopologyEdges(tx, taskID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, e := range edges {
		if e.ID == edgeID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("联系 %d 不在拓扑中", edgeID)
	}
	if _, err := tx.Exec(`DELETE FROM task_topology_edges WHERE task_id=$1 AND id=$2`, taskID, edgeID); err != nil {
		return nil, err
	}
	if err := repairTopologyOrphans(tx, taskID, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

func topologyGroupKnown(g *TopologyGraph, groupID int64) bool {
	if groupID <= 0 {
		return false
	}
	for _, group := range g.Groups {
		if group.ID == groupID {
			return true
		}
	}
	return false
}

// UpsertTopologyGroup creates a group when group.ID is 0, otherwise updates the
// named existing group.
func (d *DB) UpsertTopologyGroup(taskID int64, actor string, group TopologyGroup) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	group.Name = strings.TrimSpace(group.Name)
	group.Color = strings.TrimSpace(group.Color)
	if utf8.RuneCountInString(group.Name) > topologyMaxGroupRunes {
		return nil, fmt.Errorf("分组名最多 %d 个字符", topologyMaxGroupRunes)
	}
	if group.Width <= 0 || group.Height <= 0 {
		return nil, fmt.Errorf("分组宽高必须大于 0")
	}
	if !topologyFinite(group.X) || !topologyFinite(group.Y) || !topologyFinite(group.Width) || !topologyFinite(group.Height) {
		return nil, fmt.Errorf("分组坐标或尺寸无效")
	}
	if group.Color == "" {
		group.Color = "#94a3b8"
	}
	if group.ID > 0 {
		result, err := d.Exec(`UPDATE task_topology_groups SET name=$3,x=$4,y=$5,width=$6,height=$7,color=$8,updated_by=$9,updated_at=now()
WHERE task_id=$1 AND id=$2`, taskID, group.ID, group.Name, group.X, group.Y, group.Width, group.Height, group.Color, actor)
		if err != nil {
			return nil, err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return nil, fmt.Errorf("分组 %d 不在该任务中", group.ID)
		}
		return d.TaskTopologyEditor(taskID)
	}
	if _, err := d.Exec(`INSERT INTO task_topology_groups(task_id,name,x,y,width,height,color,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, taskID, group.Name, group.X, group.Y, group.Width, group.Height, group.Color, actor); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// DeleteTopologyGroup deletes one group box; its nodes stay and lose group_id.
func (d *DB) DeleteTopologyGroup(taskID, groupID int64, actor string) (*TopologyGraph, error) {
	if taskID <= 0 || groupID <= 0 {
		return nil, fmt.Errorf("task id and group id are required")
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`UPDATE task_topology_nodes SET group_id=NULL,updated_by=$3,updated_at=now()
WHERE task_id=$1 AND group_id=$2`, taskID, groupID, actor); err != nil {
		return nil, err
	}
	result, err := tx.Exec(`DELETE FROM task_topology_groups WHERE task_id=$1 AND id=$2`, taskID, groupID)
	if err != nil {
		return nil, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return nil, fmt.Errorf("分组 %d 不在该任务中", groupID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// AddTopologyNote appends one manual note to an existing topology node.
func (d *DB) AddTopologyNote(taskID int64, nodeIP, body, actor string) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	nodeIP = strings.TrimSpace(nodeIP)
	body = strings.TrimSpace(body)
	if net.ParseIP(nodeIP) == nil {
		return nil, fmt.Errorf("节点 IP 无效")
	}
	if body == "" || utf8.RuneCountInString(body) > topologyMaxNoteRunes {
		return nil, fmt.Errorf("备注需要 1 到 %d 个字符", topologyMaxNoteRunes)
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	nodes, err := loadTopologyNodes(tx, taskID)
	if err != nil {
		return nil, err
	}
	if !topologyHasNode(nodes, nodeIP) {
		return nil, fmt.Errorf("节点 %s 不在拓扑中", nodeIP)
	}
	if _, err := tx.Exec(`INSERT INTO task_topology_notes(task_id,node_ip,body,created_by) VALUES($1,$2,$3,$4)`, taskID, nodeIP, body, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// latestFactSummaries returns, per planned node IP, the newest confirmed fact
// summary anchored to that asset (same source as backfillTopologyDescriptionSQL).
// One small query per IP: seed node counts are in the tens, and a plain scalar
// parameter keeps the query driver-agnostic (no array-typed argument needed).
func latestFactSummaries(d *DB, taskID int64, nodes []TopologyNode) (map[string]string, error) {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		var summary string
		err := d.QueryRow(`SELECT COALESCE(NULLIF(en.payload->>'summary',''), en.payload->>'text','')
FROM assets a
JOIN exploration_anchors ea ON ea.asset_id = a.id
JOIN exploration_nodes en ON en.id = ea.node_id AND en.kind='fact' AND en.state='confirmed'
WHERE $1 = ANY(a.task_ids) AND a.ip = $2
ORDER BY en.id DESC LIMIT 1`, taskID, n.IP).Scan(&summary)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			continue
		}
		runes := []rune(summary)
		if len(runes) > 300 {
			runes = runes[:300]
		}
		out[n.IP] = string(runes)
	}
	return out, nil
}

// SeedTopologyFromAssets builds the initial graph from the task's ip assets
// (internal IPv4 only). Existing nodes are not re-inserted; existing edges are
// not duplicated.
func (d *DB) SeedTopologyFromAssets(taskID int64, actor string) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	assets, err := d.Assets().QueryByTask(taskID, "ip", 0, 0)
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(assets))
	for _, a := range assets {
		ips = append(ips, a.IP)
	}
	rows, err := d.Query(`SELECT `+findingSelectCols+` FROM findings f LEFT JOIN tasks t ON f.task_id=t.id WHERE f.task_id=$1 ORDER BY f.id`, taskID)
	if err != nil {
		return nil, err
	}
	findings, err := scanFindings(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	assetIP := make(map[int64]string, len(assets))
	for _, a := range assets {
		if ip := strings.TrimSpace(a.IP); ip != "" {
			assetIP[a.ID] = ip
		}
	}
	seedFindings := make([]topologySeedFinding, 0, len(findings))
	for _, f := range findings {
		item := topologySeedFinding{ID: f.ID, Class: f.VulnClass, Name: f.Name, Severity: f.Severity}
		for _, id := range f.AssetIDs {
			if ip := assetIP[id]; ip != "" {
				item.IPs = append(item.IPs, ip)
			}
		}
		seedFindings = append(seedFindings, item)
	}
	planNodes, planEdges := planTopologySeed(ips, seedFindings)

	// 新节点预填一句话事实：该 IP 最新 confirmed fact 的 summary（与存量回填
	// backfillTopologyDescriptionSQL 同源），人工清空后 seed 重跑不会盖回——
	// 只插新节点，既有节点不动。
	descByIP, err := latestFactSummaries(d, taskID, planNodes)
	if err != nil {
		return nil, err
	}
	for i := range planNodes {
		if desc := descByIP[planNodes[i].IP]; desc != "" {
			planNodes[i].Description = desc
		}
	}

	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	g, err := loadTopologyGraph(tx, taskID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(g.Nodes)+len(planNodes))
	for _, n := range g.Nodes {
		known[n.IP] = true
	}
	for _, n := range planNodes {
		if known[n.IP] {
			continue
		}
		known[n.IP] = true
		if _, err := tx.Exec(`INSERT INTO task_topology_nodes(task_id,ip,entry_status,updated_by)
VALUES($1,$2,$3,$4)`, taskID, n.IP, n.EntryStatus, actor); err != nil {
			return nil, err
		}
	}
	// 种子边去重 + 防环：plan 只保证"自身无环"，与库里既有边叠加仍可能撞唯一键或成环。
	// 种子是尽力而为的初始化，这类边直接跳过而不是让整次 seed 失败。
	live := make([]TopologyEdge, 0, len(g.Edges)+len(planEdges))
	seen := make(map[string]bool, len(g.Edges)+len(planEdges))
	for _, e := range g.Edges {
		live = append(live, e)
		seen[topologyEdgeKey(e)] = true
	}
	for _, e := range planEdges {
		if !known[e.SrcIP] || !known[e.DstIP] || e.SrcIP == e.DstIP {
			continue
		}
		key := topologyEdgeKey(e)
		if seen[key] || topologyWouldCycle(live, e.SrcIP, e.DstIP) {
			continue
		}
		if err := insertTopologyEdge(tx, taskID, actor, e); err != nil {
			return nil, err
		}
		seen[key] = true
		live = append(live, e)
	}
	if err := repairTopologyOrphans(tx, taskID, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

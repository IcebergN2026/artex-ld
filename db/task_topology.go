package db

import (
	"fmt"
	"net"
	"sort"
	"strconv"

	"strings"
	"time"
)

type TaskTopologyNode struct {
	Key          string           `json:"key"`
	IP           string           `json:"ip"`
	Internal     bool             `json:"internal"`
	Hostname     string           `json:"hostname,omitempty"`
	DisplayLabel string           `json:"display_label,omitempty"`
	OS           string           `json:"os,omitempty"`
	Ports        []map[string]any `json:"ports"`
	Accounts     []string         `json:"accounts"`
	AssetIDs     []int64          `json:"asset_ids"`
	EntryStatus  string           `json:"entry_status,omitempty"`
	EntrySources []string         `json:"entry_sources"`
	EvidenceRefs []string         `json:"evidence_refs"`
}

type TaskTopologyEdge struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
	Manual      bool   `json:"manual"`
}

type TaskTopologyNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type TaskTopology struct {
	Nodes   []TaskTopologyNode            `json:"nodes"`
	Edges   []TaskTopologyEdge            `json:"edges"`
	Notes   map[string][]TaskTopologyNote `json:"notes"`
	RootKey string                        `json:"root_key"`
}

type TaskTopologyOverride struct {
	NodeIP        string
	RelationKind  string
	RelationLabel string
	ParentIP      string
	EvidenceRef   string
	EntryStatus   string
	DisplayLabel  string
}

func topologyPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && (parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast())
}

func topologyEntryClass(class string) bool {
	switch strings.ToLower(strings.ReplaceAll(class, "-", "_")) {
	case "getshell", "rce", "file_upload", "deserialization":
		return true
	default:
		return false
	}
}

func buildTaskTopology(assets []*Asset, findings []*DBFinding, overrides map[string]TaskTopologyOverride, notes map[string][]TaskTopologyNote) *TaskTopology {
	assetByID := make(map[int64]*Asset, len(assets))
	nodes := make(map[string]*TaskTopologyNode)
	assetIPs := make(map[int64]string)
	for _, a := range assets {
		assetByID[a.ID] = a
		ip := strings.TrimSpace(a.IP)
		if a.Type == "subdomain" && ip == "" && strings.EqualFold(a.RecordType, "A") && len(a.RecordValue) > 0 {
			ip = strings.TrimSpace(a.RecordValue[0])
		}
		if ip == "" || net.ParseIP(ip) == nil {
			continue
		}
		assetIPs[a.ID] = ip
		n := nodes[ip]
		if n == nil {
			n = &TaskTopologyNode{Key: "ip:" + ip, IP: ip, Internal: topologyPrivateIP(ip), Ports: []map[string]any{}, Accounts: []string{}, AssetIDs: []int64{}, EntrySources: []string{}, EvidenceRefs: []string{}}
			nodes[ip] = n
		}
		n.AssetIDs = append(n.AssetIDs, a.ID)
		if a.Type == "ip" {
			n.Ports = append(n.Ports, a.OpenPorts...)
			if n.Hostname == "" {
				n.Hostname, _ = a.Extra["hostname"].(string)
			}
			if n.OS == "" {
				n.OS, _ = a.Extra["os"].(string)
			}
			for _, auth := range a.Auth {
				if username := strings.TrimSpace(fmt.Sprint(auth["username"])); username != "" {
					n.Accounts = append(n.Accounts, username)
				}
			}
		}
	}
	for _, f := range findings {
		if !topologyEntryClass(f.VulnClass) || (f.Severity != "high" && f.Severity != "critical") {
			continue
		}
		ips := map[string]bool{}
		for _, id := range f.AssetIDs {
			if ip := assetIPs[id]; ip != "" {
				ips[ip] = true
				continue
			}
			if a := assetByID[id]; a != nil {
				if ip := strings.TrimSpace(a.IP); ip != "" {
					ips[ip] = true
				}
				for _, value := range a.RecordValue {
					if net.ParseIP(value) != nil {
						ips[value] = true
					}
				}
			}
		}
		for ip := range ips {
			n := nodes[ip]
			if n == nil {
				continue
			}
			n.EntryStatus = "suspected"
			n.EntrySources = append(n.EntrySources, fmt.Sprintf("finding:%d", f.ID))
			n.EvidenceRefs = append(n.EvidenceRefs, fmt.Sprintf("finding:%d", f.ID))
		}
	}
	for ip, override := range overrides {
		n := nodes[ip]
		if n == nil {
			continue
		}
		if override.EntryStatus != "" {
			n.EntryStatus = override.EntryStatus
		}
		n.DisplayLabel = override.DisplayLabel
		if override.EvidenceRef != "" {
			n.EvidenceRefs = append(n.EvidenceRefs, override.EvidenceRef)
		}
	}
	out := &TaskTopology{Nodes: []TaskTopologyNode{}, Edges: []TaskTopologyEdge{}, Notes: notes}
	entry := ""
	for _, ip := range sortedTopologyIPs(nodes) {
		n := *nodes[ip]
		if !n.Internal && n.EntryStatus == "" {
			continue
		}
		if n.EntryStatus != "" && (entry == "" || n.IP < entry) {
			entry = n.IP
		}
		n.Ports = uniqueTopologyMaps(n.Ports)
		n.Accounts = uniqueTopologyStrings(n.Accounts)
		n.EntrySources = uniqueTopologyStrings(n.EntrySources)
		n.EvidenceRefs = uniqueTopologyStrings(n.EvidenceRefs)
		sort.Slice(n.AssetIDs, func(i, j int) bool { return n.AssetIDs[i] < n.AssetIDs[j] })
		out.Nodes = append(out.Nodes, n)
	}
	// No entry evidence means no graph root. Keep real nodes independent instead
	// of inventing a virtual node or relationships that evidence does not support.
	out.RootKey = entry
	visible := make(map[string]bool, len(out.Nodes))
	for _, n := range out.Nodes {
		visible[n.IP] = true
	}
	for _, n := range out.Nodes {
		override := overrides[n.IP]
		if override.RelationKind != "" && visible[override.ParentIP] && override.ParentIP != n.IP {
			out.Edges = append(out.Edges, TaskTopologyEdge{Source: override.ParentIP, Target: n.IP, Kind: override.RelationKind, Label: override.RelationLabel, EvidenceRef: override.EvidenceRef, Manual: override.RelationKind != "evidence"})
		}
	}
	if out.Notes == nil {
		out.Notes = map[string][]TaskTopologyNote{}
	}
	return out
}

func sortedTopologyIPs(nodes map[string]*TaskTopologyNode) []string {
	ips := make([]string, 0, len(nodes))
	for ip := range nodes {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips
}
func uniqueTopologyStrings(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
func uniqueTopologyMaps(items []map[string]any) []map[string]any {
	seen := map[string]bool{}
	out := []map[string]any{}
	for _, item := range items {
		key := fmt.Sprint(item["port"], "/", item["service"])
		if key != "/" && !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

func (d *DB) TaskTopology(taskID int64) (*TaskTopology, error) {
	assets, err := d.Assets().QueryByTask(taskID, "", 0, 0)
	if err != nil {
		return nil, err
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
	overrides := map[string]TaskTopologyOverride{}
	rows, err = d.Query(`SELECT node_ip,relation_kind,relation_label,parent_ip,evidence_ref,entry_status,display_label FROM task_topology_overrides WHERE task_id=$1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o TaskTopologyOverride
		if err := rows.Scan(&o.NodeIP, &o.RelationKind, &o.RelationLabel, &o.ParentIP, &o.EvidenceRef, &o.EntryStatus, &o.DisplayLabel); err != nil {
			return nil, err
		}
		overrides[o.NodeIP] = o
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	notes := map[string][]TaskTopologyNote{}
	noteRows, err := d.Query(`SELECT id,node_ip,body,created_by,created_at FROM task_topology_notes WHERE task_id=$1 ORDER BY created_at DESC,id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer noteRows.Close()
	for noteRows.Next() {
		var ip string
		var n TaskTopologyNote
		if err := noteRows.Scan(&n.ID, &ip, &n.Body, &n.CreatedBy, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes[ip] = append(notes[ip], n)
	}
	if err := noteRows.Err(); err != nil {
		return nil, err
	}
	return buildTaskTopology(assets, findings, overrides, notes), nil
}

func (d *DB) UpdateTaskTopologyNode(taskID int64, override TaskTopologyOverride, actor string) (*TaskTopology, error) {
	override.DisplayLabel = strings.TrimSpace(override.DisplayLabel)
	if len(override.DisplayLabel) > 120 {
		return nil, fmt.Errorf("display label is too long")
	}
	if net.ParseIP(override.NodeIP) == nil {
		return nil, fmt.Errorf("invalid node IP")
	}
	override.RelationLabel = strings.TrimSpace(override.RelationLabel)
	if len(override.RelationLabel) > 200 {
		return nil, fmt.Errorf("relation label is too long")
	}
	if (override.RelationLabel == "") != (override.ParentIP == "") {
		return nil, fmt.Errorf("relation label and parent IP must be set together")
	}
	// Human relations are free text. Evidence and entry status remain agent-maintained.
	override.RelationKind = ""
	if override.RelationLabel != "" {
		override.RelationKind = "unknown"
	}
	if override.ParentIP != "" && net.ParseIP(override.ParentIP) == nil {
		return nil, fmt.Errorf("invalid parent IP")
	}
	current, err := d.TaskTopology(taskID)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, n := range current.Nodes {
		visible[n.IP] = true
	}
	if !visible[override.NodeIP] {
		return nil, fmt.Errorf("node is not in this task topology")
	}
	if override.ParentIP != "" && (!visible[override.ParentIP] || override.ParentIP == override.NodeIP) {
		return nil, fmt.Errorf("parent must be another visible node")
	}
	parents := map[string]string{}
	for _, edge := range current.Edges {
		if edge.Manual && edge.Target != override.NodeIP {
			parents[edge.Target] = edge.Source
		}
	}
	for at := override.ParentIP; at != ""; at = parents[at] {
		if at == override.NodeIP {
			return nil, fmt.Errorf("relation would create a cycle")
		}
	}
	_, err = d.Exec(`INSERT INTO task_topology_overrides(task_id,node_ip,relation_kind,relation_label,parent_ip,evidence_ref,entry_status,display_label,updated_by) VALUES($1,$2,$3,$4,$5,'','',$6,$7) ON CONFLICT(task_id,node_ip) DO UPDATE SET relation_kind=EXCLUDED.relation_kind,relation_label=EXCLUDED.relation_label,parent_ip=EXCLUDED.parent_ip,display_label=EXCLUDED.display_label,updated_by=EXCLUDED.updated_by,updated_at=now()`, taskID, override.NodeIP, override.RelationKind, override.RelationLabel, override.ParentIP, override.DisplayLabel, actor)
	if err != nil {
		return nil, err
	}
	return d.TaskTopology(taskID)
}

// RecordTaskTopologyRelation persists an agent-submitted, evidence-backed
// relation. Human free-text edits use UpdateTaskTopologyNode; this separate path
// prevents the UI from forging agent evidence and keeps unsupported nodes disconnected.
func (d *DB) RecordTaskTopologyRelation(taskID int64, override TaskTopologyOverride, actor string) (*TaskTopology, error) {
	override.NodeIP = strings.TrimSpace(override.NodeIP)
	override.ParentIP = strings.TrimSpace(override.ParentIP)
	override.RelationLabel = strings.TrimSpace(override.RelationLabel)
	override.EvidenceRef = strings.TrimSpace(override.EvidenceRef)
	if net.ParseIP(override.NodeIP) == nil || net.ParseIP(override.ParentIP) == nil || override.NodeIP == override.ParentIP {
		return nil, fmt.Errorf("relation requires two different valid IP addresses")
	}
	if override.RelationLabel == "" || len(override.RelationLabel) > 200 {
		return nil, fmt.Errorf("relation label must contain 1 to 200 characters")
	}
	if override.EvidenceRef == "" || len(override.EvidenceRef) > 500 {
		return nil, fmt.Errorf("agent relation requires an evidence reference")
	}
	var explorationID int64
	var categoryNKey string
	if err := d.QueryRow(`SELECT t.exploration_id,COALESCE(c.nkey,'') FROM tasks t
		LEFT JOIN task_categories c ON c.id=t.category_id WHERE t.id=$1`, taskID).Scan(&explorationID, &categoryNKey); err != nil {
		return nil, err
	}
	if categoryNKey != "内网渗透" {
		return nil, fmt.Errorf("topology relations are only available for internal-pentest tasks")
	}
	parts := strings.SplitN(override.EvidenceRef, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("evidence reference must be finding:<id> or fact:<id>")
	}
	evidenceID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || evidenceID <= 0 {
		return nil, fmt.Errorf("invalid evidence reference")
	}
	var evidenceExists bool
	switch parts[0] {
	case "finding":
		err = d.QueryRow(`SELECT EXISTS(SELECT 1 FROM findings WHERE id=$1 AND task_id=$2)`, evidenceID, taskID).Scan(&evidenceExists)
	case "fact":
		err = d.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='fact')`, evidenceID, explorationID).Scan(&evidenceExists)
	default:
		return nil, fmt.Errorf("evidence reference must be finding:<id> or fact:<id>")
	}
	if err != nil {
		return nil, err
	}
	if !evidenceExists {
		return nil, fmt.Errorf("evidence does not belong to this task")
	}
	current, err := d.TaskTopology(taskID)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, n := range current.Nodes {
		visible[n.IP] = true
	}
	if !visible[override.NodeIP] || !visible[override.ParentIP] {
		return nil, fmt.Errorf("relation nodes must be visible in this task topology")
	}
	parents := map[string]string{}
	for _, edge := range current.Edges {
		if edge.Target != override.NodeIP {
			parents[edge.Target] = edge.Source
		}
	}
	for at := override.ParentIP; at != ""; at = parents[at] {
		if at == override.NodeIP {
			return nil, fmt.Errorf("relation would create a cycle")
		}
	}
	_, err = d.Exec(`INSERT INTO task_topology_overrides(task_id,node_ip,relation_kind,relation_label,parent_ip,evidence_ref,entry_status,display_label,updated_by)
		VALUES($1,$2,'evidence',$3,$4,$5,'','',$6)
		ON CONFLICT(task_id,node_ip) DO UPDATE SET relation_kind='evidence',relation_label=EXCLUDED.relation_label,
		parent_ip=EXCLUDED.parent_ip,evidence_ref=EXCLUDED.evidence_ref,updated_by=EXCLUDED.updated_by,updated_at=now()`,
		taskID, override.NodeIP, override.RelationLabel, override.ParentIP, override.EvidenceRef, actor)
	if err != nil {
		return nil, err
	}
	return d.TaskTopology(taskID)
}

func (d *DB) AddTaskTopologyNote(taskID int64, nodeIP, body, actor string) (*TaskTopology, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 4000 {
		return nil, fmt.Errorf("note must contain 1 to 4000 characters")
	}
	current, err := d.TaskTopology(taskID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, n := range current.Nodes {
		if n.IP == nodeIP {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("node is not in this task topology")
	}
	if _, err := d.Exec(`INSERT INTO task_topology_notes(task_id,node_ip,body,created_by) VALUES($1,$2,$3,$4)`, taskID, nodeIP, body, actor); err != nil {
		return nil, err
	}
	return d.TaskTopology(taskID)
}

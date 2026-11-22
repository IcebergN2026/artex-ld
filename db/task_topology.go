// task_topology.go：把证据支撑的关系写入任务拓扑（task_topology_relations 表）。
// 主要函数/类型：
//   - TopologyRelationInput：一条由 fact 派生或迁移录入的拓扑关系输入。
//   - RecordTaskTopologyRelation：规范化并写入关系（含类目门控、成环检测、冲突刷新），返回最新拓扑图。

package db

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TopologyRelationInput is one evidence-backed relation derived from a fact
// payload (BuildEdgesFromFact) or recorded by migrations. Human edits use the
// editor API instead.
type TopologyRelationInput struct {
	SrcIP       string
	DstIP       string
	Label       string
	Kind        string
	EvidenceRef string
}

// RecordTaskTopologyRelation persists an evidence-backed relation into the
// editable graph (task_topology_nodes / task_topology_edges). Callers:
// BuildEdgesFromFact (fact payload derivation) and migrations. Human free-text
// edits use the editor API; this separate path prevents the UI from forging
// agent evidence. An endpoint that is not in the graph yet is
// created in the same transaction — the edge is what puts it there, so the
// no-orphan invariant still holds.
func (d *DB) RecordTaskTopologyRelation(taskID int64, rel TopologyRelationInput, actor string) (*TopologyGraph, error) {
	if taskID <= 0 {
		return nil, fmt.Errorf("task id is required")
	}
	rel, err := normalizeTopologyRelation(rel)
	if err != nil {
		return nil, err
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
	parts := strings.SplitN(rel.EvidenceRef, ":", 2)
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
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	nodes, err := loadTopologyNodes(tx, taskID)
	if err != nil {
		return nil, err
	}
	edges, err := loadTopologyEdges(tx, taskID)
	if err != nil {
		return nil, err
	}
	if topologyWouldCycle(edges, rel.SrcIP, rel.DstIP) {
		return nil, fmt.Errorf("relation would create a cycle")
	}
	for _, ip := range []string{rel.SrcIP, rel.DstIP} {
		if topologyHasNode(nodes, ip) {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO task_topology_nodes(task_id,ip,updated_by) VALUES($1,$2,$3)`, taskID, ip, actor); err != nil {
			return nil, err
		}
	}
	// 同一个观测可能被重复登记：唯一键 (task_id,src_ip,dst_ip,kind) 上覆盖标签与证据。
	// system_label=evidence_ref 在插入时打标：rewriteTopologySystemEdgeLabels 只处理
	// system_label='' 的遗留行，新边不会被启动迁移重写。
	if _, err := tx.Exec(`INSERT INTO task_topology_edges(task_id,src_ip,dst_ip,label,kind,evidence_ref,system_label,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$6,$7)
ON CONFLICT(task_id,src_ip,dst_ip,kind) DO UPDATE SET label=EXCLUDED.label,evidence_ref=EXCLUDED.evidence_ref,
updated_by=EXCLUDED.updated_by,updated_at=now()`,
		taskID, rel.SrcIP, rel.DstIP, rel.Label, rel.Kind, rel.EvidenceRef, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.TaskTopologyEditor(taskID)
}

// normalizeTopologyRelation trims and validates the agent payload before any
// database access, so a malformed tool call never touches the graph.
func normalizeTopologyRelation(rel TopologyRelationInput) (TopologyRelationInput, error) {
	rel.SrcIP = strings.TrimSpace(rel.SrcIP)
	rel.DstIP = strings.TrimSpace(rel.DstIP)
	rel.Label = strings.TrimSpace(rel.Label)
	rel.EvidenceRef = strings.TrimSpace(rel.EvidenceRef)
	if net.ParseIP(rel.SrcIP) == nil || net.ParseIP(rel.DstIP) == nil || rel.SrcIP == rel.DstIP {
		return rel, fmt.Errorf("relation requires two different valid IP addresses")
	}
	if rel.Label == "" || utf8.RuneCountInString(rel.Label) > topologyMaxLabelRunes {
		return rel, fmt.Errorf("relation label must contain 1 to %d characters", topologyMaxLabelRunes)
	}
	// kind 只收两种：solid=发现漏洞或横向移动成功（实线"已突破"），
	// reachable=仅确认可达/存活（虚线）。空值向后兼容为 solid。
	rel.Kind = strings.TrimSpace(rel.Kind)
	if rel.Kind == "" {
		rel.Kind = topologyKindSolid
	}
	switch rel.Kind {
	case topologyKindSolid, topologyKindReachable:
	default:
		return rel, fmt.Errorf("relation kind must be %q (发现漏洞或横向移动成功) or %q (仅可达)", topologyKindSolid, topologyKindReachable)
	}
	if rel.EvidenceRef == "" || utf8.RuneCountInString(rel.EvidenceRef) > topologyMaxEvidenceRunes {
		return rel, fmt.Errorf("agent relation requires an evidence reference")
	}
	return rel, nil
}

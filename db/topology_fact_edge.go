// topology_fact_edge.go：解析 fact 拓扑负载并生成任务拓扑边。
// 主要函数/类型：
//   - TopologyFactPayload：fact payload["topology"] 的解码结构。
//   - ParseTopologyFact：解析并清洗拓扑负载（无效时返回 nil）。
//   - BuildEdgesFromFact：按负载为每个目标 IP 建一条边，跳过非内网/自环/成环，失败仅记日志。

package db

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// TopologyFactPayload 是 fact payload["topology"] 字典的解码结构。
// 「如何发现的」只在 fact 写一次（单一事实源），拓扑边 label 从这里派生。
type TopologyFactPayload struct {
	Discovery string   `json:"discovery"` // 如何发现的，≤20 字，写手段不写结论；空→TopologyFallbackLabel
	SrcIP     string   `json:"src_ip"`    // 父节点/立足点 IP（内网）
	DstIPs    []string `json:"dst_ips"`   // 目标 IP；多台=同方式同父同 kind 的一批
	Kind      string   `json:"kind"`      // solid=已突破 / reachable=仅可达；缺省 reachable
}

// ParseTopologyFact 从 fact payload 取出 topology 字典。
// 无 topology 键、缺 src_ip 或 dst_ips 为空时返回 nil —— 纯事实，不建边。
func ParseTopologyFact(payload map[string]any) *TopologyFactPayload {
	raw, ok := payload["topology"]
	if !ok || raw == nil {
		return nil
	}
	// payload 里的嵌套值是 any（agent 层组装的 map 或 JSON 解码结果），统一走一轮编解码。
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var t TopologyFactPayload
	if err := json.Unmarshal(b, &t); err != nil {
		return nil
	}
	t.SrcIP = strings.TrimSpace(t.SrcIP)
	dsts := t.DstIPs[:0]
	for _, dst := range t.DstIPs {
		if dst = strings.TrimSpace(dst); dst != "" {
			dsts = append(dsts, dst)
		}
	}
	t.DstIPs = dsts
	if t.SrcIP == "" || len(t.DstIPs) == 0 {
		return nil
	}
	return &t
}

// BuildEdgesFromFact 在 fact 落库后由 agent 层调用：按 payload["topology"] 生成拓扑边，
// 每个 dst_ip 一条边，label 取 discovery（空→「发现方式待确认」），evidence_ref=fact:<id>。
//
// 失败语义（已定稿）：仅「非内网 / 自环 / 成环」三种连线不成立的情况跳过，其余照画；
// 任何跳过或写库错误都静默记日志，不回滚已落库的 fact、不上抛错误打扰 agent——
// fact 是已发生的事实，边是派生物。
func (d *DB) BuildEdgesFromFact(taskID, factID int64, payload map[string]any, actor string) {
	t := ParseTopologyFact(payload)
	if t == nil {
		return
	}
	if !topologyInternalIPv4(t.SrcIP) {
		log.Printf("[topology] fact %d 建边跳过：src_ip %s 非内网", factID, t.SrcIP)
		return
	}
	kind := t.Kind
	if kind != topologyKindSolid {
		kind = topologyKindReachable
	}
	label := strings.TrimSpace(t.Discovery)
	if label == "" {
		label = TopologyFallbackLabel
	}
	if r := []rune(label); len(r) > topologyMaxLabelRunes {
		label = string(r[:topologyMaxLabelRunes])
	}
	for _, dst := range t.DstIPs {
		if dst == t.SrcIP {
			log.Printf("[topology] fact %d 建边跳过：%s 自环", factID, dst)
			continue
		}
		if !topologyInternalIPv4(dst) {
			log.Printf("[topology] fact %d 建边跳过：dst_ip %s 非内网", factID, dst)
			continue
		}
		rel := TopologyRelationInput{
			SrcIP:       t.SrcIP,
			DstIP:       dst,
			Label:       label,
			Kind:        kind,
			EvidenceRef: fmt.Sprintf("fact:%d", factID),
		}
		// RecordTaskTopologyRelation 内含：类目门控（仅内网渗透）、证据归属校验、
		// 成环检测（topologyWouldCycle，成环即报错→此处记日志跳过）、
		// 节点 upsert（无游离节点）、(task_id,src_ip,dst_ip,kind) 冲突时刷新 label/证据。
		if _, err := d.RecordTaskTopologyRelation(taskID, rel, actor); err != nil {
			log.Printf("[topology] fact %d 建边 %s→%s 跳过: %v", factID, t.SrcIP, dst, err)
		}
	}
}

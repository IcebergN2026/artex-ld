package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// recordTopologyRelation records only evidence-backed agent observations. It
// deliberately lives on the per-task ToolSet so task and evidence ownership are
// derived from the current exploration rather than an agent-supplied task ID.
func (t *ToolSet) recordTopologyRelation() actool.CoreTool {
	return t.writeExpTool("record_topology_relation",
		"登记内网拓扑中一条有证据的真实关系。仅在本次确认网卡、路由、容器网络或会话连接后使用；label 自定义描述关系，evidence_ref 必须为本任务 finding:<id> 或 fact:<id>。不要因同网段或同任务而猜测连线。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"parent_ip":    map[string]any{"type": "string", "description": "上游/来源主机 IP"},
				"node_ip":      map[string]any{"type": "string", "description": "下游/目标主机 IP"},
				"label":        map[string]any{"type": "string", "description": "自定义关系说明，如‘容器到宿主的 Docker API 通道’"},
				"evidence_ref": map[string]any{"type": "string", "description": "本任务证据引用：finding:<id> 或 fact:<id>"},
			},
			"required": []string{"parent_ip", "node_ip", "label", "evidence_ref"},
		},
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ParentIP    string `json:"parent_ip"`
				NodeIP      string `json:"node_ip"`
				Label       string `json:"label"`
				EvidenceRef string `json:"evidence_ref"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid JSON: " + err.Error()), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("record_topology_relation requires a task context"), nil
			}
			g, err := t.ts.RecordTopologyRelation(t.taskID, db.TaskTopologyOverride{
				ParentIP: strings.TrimSpace(a.ParentIP), NodeIP: strings.TrimSpace(a.NodeIP),
				RelationLabel: strings.TrimSpace(a.Label), EvidenceRef: strings.TrimSpace(a.EvidenceRef),
			}, t.worker)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			for _, edge := range g.Edges {
				if edge.Source == a.ParentIP && edge.Target == a.NodeIP && edge.EvidenceRef == a.EvidenceRef {
					return jsonResult(map[string]any{"status": "recorded", "edge": edge})
				}
			}
			return actool.Errorf("topology relation was not visible after saving"), nil
		})
}

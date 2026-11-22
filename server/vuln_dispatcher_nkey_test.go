package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"artex/agent"
	"artex/db"
)

func TestVulnDispatcherDefaultPromptUsesStableTemplateKeys(t *testing.T) {
	prompt := agent.VulnDispatcherDefaultPrompt
	if strings.Contains(prompt, "planner_template_id=") || strings.Contains(prompt, "【TPL_ID_") {
		t.Fatal("default prompt still contains a numeric ID or legacy placeholder")
	}
	if !strings.Contains(prompt, "default_review_enabled") || strings.Contains(prompt, "一律继承父任务") {
		t.Fatal("dispatcher prompt must describe parent review plus category defaults")
	}
	if !strings.Contains(prompt, "先全面收集主机、容器、网卡、路由") || !strings.Contains(prompt, "当前任务尚未执行就判定完成") {
		t.Fatal("internal child tasks must collect information before claiming completion")
	}
	for _, forbidden := range []string{"可达性不等于授权", "范围外主机只做被动记录", "未获授权前不得对其发起攻击"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("internal dispatcher prompt retained unwanted scope wording %q", forbidden)
		}
	}
	if !strings.Contains(prompt, "内网渗透 · <立足点 host 或目标资产>") || !strings.Contains(prompt, "name：必须填写简短任务名") {
		t.Fatal("dispatcher prompt must separate the short task name from the detailed description")
	}
	if !strings.Contains(prompt, "不复制父 task_constraints") || !strings.Contains(prompt, "不默认生成单资产限制") {
		t.Fatal("dispatcher must inherit evidence without copying single-asset execution constraints")
	}
	// 单场景派发：只允许内网渗透；其余场景不得作为派发目标出现在提示词里。
	if !strings.Contains(prompt, "planner_template_nkey=内网渗透") {
		t.Error("missing fixed internal-pentest template reference")
	}
	for _, scene := range []string{"权限提升", "漏洞利用", "漏洞挖掘", "代码审计"} {
		if strings.Contains(prompt, "planner_template_nkey="+scene) {
			t.Errorf("dispatcher prompt must not dispatch scene %s", scene)
		}
	}
}

func TestResolveVulnDispatcherLegacyPlaceholders(t *testing.T) {
	s := &Server{}
	got := s.resolveVulnDispatcherPrompt("planner_template_id=【TPL_ID_内网渗透】")
	if got != "planner_template_nkey=内网渗透" {
		t.Fatalf("unexpected legacy conversion: %s", got)
	}
}

func TestSpawnTaskRejectsAmbiguousTemplateReference(t *testing.T) {
	s := &Server{}
	result, err := s.toolSpawnTask().Call(context.Background(), json.RawMessage(`{
		"description":"test","goal":"test","planner_template_id":4,"planner_template_nkey":"内网渗透"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Flatten(), "互斥") {
		t.Fatalf("expected mutual-exclusion error, got %+v", result)
	}
}

func TestResolveSpawnReviewUsesNKeyCategoryDefault(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	var previousReview bool
	hadCategory := pg.QueryRow(`SELECT default_review_enabled FROM task_categories WHERE nkey='内网渗透'`).Scan(&previousReview) == nil
	if _, err := pg.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`UPDATE task_categories SET default_review_enabled=true WHERE nkey='内网渗透'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadCategory {
			_, _ = pg.Exec(`UPDATE task_categories SET default_review_enabled=$1 WHERE nkey='内网渗透'`, previousReview)
		} else if cat, _ := pg.GetTaskCategoryByNKey("内网渗透"); cat != nil {
			_, _ = pg.DeleteTaskCategory(cat.ID)
		}
	})
	s := &Server{m: &Manager{pg: pg}}
	for _, tc := range []struct {
		name      string
		inherited bool
		nkey      string
		want      bool
	}{
		{name: "nkey category enables review", nkey: "内网渗透", want: true},
		{name: "parent true remains enabled", inherited: true, nkey: "missing", want: true},
		{name: "unknown category stays disabled", nkey: "missing", want: false},
		{name: "no scenario stays disabled", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.resolveSpawnReview(tc.inherited, tc.nkey)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("resolveSpawnReview(%v,%q)=%v want %v", tc.inherited, tc.nkey, got, tc.want)
			}
		})
	}
}

func TestSpawnTaskRejectsUnknownTemplateNKey(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s := &Server{m: &Manager{pg: pg}}
	result, err := s.toolSpawnTask().Call(context.Background(), json.RawMessage(`{
		"description":"test","goal":"test","planner_template_nkey":"__missing_template__"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Flatten(), "__missing_template__") {
		t.Fatalf("expected named missing-template error, got %+v", result)
	}
}

func TestSpawnTaskCopiesOnlyFindingChain(t *testing.T) {
	m, s := fiFixture(t)
	pg := m.pg
	if _, err := pg.SeedWorkerTemplateIfEmpty(db.WorkerTemplateBuiltinName, db.WorkerTemplateBuiltinNKey, agent.BuiltinPromptSeeds()["worker"], true); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	parent := fiCreateTask(t, m, "独立图复制父任务")
	parentID := mustTaskID(t, parent.ID)
	originID, err := parent.Store.OriginFactID()
	if err != nil || originID <= 0 {
		t.Fatalf("读取父任务 origin 失败: id=%d err=%v", originID, err)
	}

	stamp := time.Now().UnixNano()
	ip := fmt.Sprintf("192.0.2.%d", 1+stamp%200)
	unrelatedIP := fmt.Sprintf("198.51.100.%d", 1+stamp%200)
	assetID, err := pg.Assets().UpsertIP(db.UpsertIPReq{IP: ip, TaskID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	unrelatedAssetID, err := pg.Assets().UpsertIP(db.UpsertIPReq{IP: unrelatedIP, TaskID: parentID})
	if err != nil {
		t.Fatal(err)
	}

	intentID, err := parent.Store.AddNode(db.KindIntent, map[string]any{"summary": "chain-intent-marker"}, 7, "running", "planner", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(originID, db.RelDerivedFrom, intentID); err != nil {
		t.Fatal(err)
	}
	factID, err := parent.Store.AddNode(db.KindFact, map[string]any{"summary": "chain-fact-marker"}, 3, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(intentID, db.RelYields, factID); err != nil {
		t.Fatal(err)
	}
	findingNodeID, err := parent.Store.AddNode(db.KindFinding, map[string]any{
		"summary": "chain-finding-marker", "vulnclass": "getshell", "severity": "critical",
	}, 0, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(factID, db.RelDerivedFrom, findingNodeID); err != nil {
		t.Fatal(err)
	}
	findingID, err := pg.AddFinding(parentID, findingNodeID, "getshell", "SSH 弱口令", "critical",
		"chain-finding-marker", "paramiko 连接成功", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}

	unrelatedIntentID, err := parent.Store.AddNode(db.KindIntent, map[string]any{"summary": "unrelated-intent-marker"}, 1, "open", "planner", []int64{unrelatedAssetID})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(originID, db.RelDerivedFrom, unrelatedIntentID); err != nil {
		t.Fatal(err)
	}
	unrelatedFactID, err := parent.Store.AddNode(db.KindFact, map[string]any{"summary": "unrelated-fact-marker"}, 0, "confirmed", "worker", []int64{unrelatedAssetID})
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(unrelatedIntentID, db.RelYields, unrelatedFactID); err != nil {
		t.Fatal(err)
	}
	goalID, err := parent.Store.AddGoal(map[string]any{"text": "unrelated-goal-marker"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Store.Link(goalID, db.RelProves, findingNodeID); err != nil {
		t.Fatal(err)
	}

	input, err := json.Marshal(map[string]any{
		"finding_id":            findingID,
		"target_asset":          ip,
		"name":                  "独立图复制子任务",
		"description":           "只复制触发 Finding 血缘链",
		"goal":                  "验证独立探索图",
		"parent_ref":            fmt.Sprint(parentID),
		"source_task_ids":       []string{fmt.Sprint(parentID)},
		"planner_template_nkey": "内网渗透",
		"planner_prompt":        "独立图回归测试 planner",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.toolSpawnTask().Call(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("spawn_task 失败: %s", result.Flatten())
	}
	childID := mustTaskID(t, strings.TrimPrefix(result.Flatten(), "task created: "))
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM tasks WHERE id=$1`, childID) })
	child, ok := m.Task(fmt.Sprint(childID))
	if !ok {
		t.Fatal("子任务未注册到 Manager")
	}
	if child.ParentRef != fmt.Sprint(parentID) || len(child.SourceTaskIDs) != 0 {
		t.Fatalf("父子/来源关系不符合契约: parent=%q sources=%v", child.ParentRef, child.SourceTaskIDs)
	}
	var relationCount int
	if err := pg.QueryRow(`SELECT count(*) FROM task_relations WHERE task_id=$1`, childID).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 0 {
		t.Fatalf("spawn_task 仍创建来源关系: %d", relationCount)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/exploration/graph?task="+fmt.Sprint(childID), nil)
	s.explorationGraph(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("读取子任务图失败: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var graph struct {
		Nodes []TaskNodeDTO `json:"nodes"`
		Edges []EdgeDTO     `json:"edges"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 4 || len(graph.Edges) != 3 {
		t.Fatalf("复制图规模错误: nodes=%d edges=%d body=%s", len(graph.Nodes), len(graph.Edges), recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"unrelated-intent-marker", "unrelated-fact-marker", "unrelated-goal-marker", `"inherited":true`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("子任务图包含无关或继承内容 %q: %s", forbidden, body)
		}
	}

	nodeByMarker := func(marker string) TaskNodeDTO {
		t.Helper()
		for _, node := range graph.Nodes {
			if strings.Contains(node.Payload, marker) {
				return node
			}
		}
		t.Fatalf("复制图缺少节点 %q: %s", marker, body)
		return TaskNodeDTO{}
	}
	copiedIntent := nodeByMarker("chain-intent-marker")
	copiedFact := nodeByMarker("chain-fact-marker")
	copiedFinding := nodeByMarker("chain-finding-marker")
	if copiedIntent.State != "done" || copiedFact.State != "confirmed" || copiedFinding.State != "confirmed" {
		t.Fatalf("复制节点状态错误: intent=%s fact=%s finding=%s", copiedIntent.State, copiedFact.State, copiedFinding.State)
	}
	var copiedOrigin *TaskNodeDTO
	copiedIDs := make(map[string]bool, len(graph.Nodes))
	sourceIDs := map[string]bool{fmt.Sprint(originID): true, fmt.Sprint(intentID): true, fmt.Sprint(factID): true, fmt.Sprint(findingNodeID): true}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		copiedIDs[node.ID] = true
		if sourceIDs[node.ID] {
			t.Fatalf("复制节点仍使用来源 id: %s", node.ID)
		}
		if node.State == "origin" {
			copiedOrigin = node
		}
	}
	if copiedOrigin == nil || copiedOrigin.Type != db.KindFact {
		t.Fatal("子任务缺少独立 origin fact")
	}
	for _, edge := range graph.Edges {
		if !copiedIDs[edge.Src] || !copiedIDs[edge.Dst] || (edge.Rel != db.RelYields && edge.Rel != db.RelDerivedFrom) {
			t.Fatalf("复制边未正确重映射: %+v", edge)
		}
	}
	var findingPayload struct {
		SourceFindingID     int64 `json:"source_finding_id"`
		SourceFindingNodeID int64 `json:"source_finding_node_id"`
	}
	if err := json.Unmarshal([]byte(copiedFinding.Payload), &findingPayload); err != nil {
		t.Fatal(err)
	}
	if findingPayload.SourceFindingID != findingID || findingPayload.SourceFindingNodeID != findingNodeID {
		t.Fatalf("Finding 来源键错误: %+v", findingPayload)
	}

	for _, node := range []TaskNodeDTO{copiedIntent, copiedFact, copiedFinding} {
		var anchoredAssetID int64
		var anchors int
		if err := pg.QueryRow(`SELECT min(asset_id), count(*) FROM exploration_anchors WHERE node_id=$1`, node.ID).Scan(&anchoredAssetID, &anchors); err != nil {
			t.Fatal(err)
		}
		if anchors != 1 || anchoredAssetID != assetID {
			t.Fatalf("节点 %s 锚点复制错误: asset=%d count=%d", node.ID, anchoredAssetID, anchors)
		}
	}
	var unrelatedAnchors int
	if err := pg.QueryRow(`SELECT count(*) FROM exploration_anchors anchor
		JOIN exploration_nodes node ON node.id=anchor.node_id
		WHERE node.exploration_id=$1 AND anchor.asset_id=$2`, child.ExpID, unrelatedAssetID).Scan(&unrelatedAnchors); err != nil {
		t.Fatal(err)
	}
	if unrelatedAnchors != 0 {
		t.Fatal("无关分支锚点被复制到子任务")
	}

	var parentFindings, childFindings int
	if err := pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, parentID).Scan(&parentFindings); err != nil {
		t.Fatal(err)
	}
	if err := pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, childID).Scan(&childFindings); err != nil {
		t.Fatal(err)
	}
	var findingTaskID, findingSourceNodeID int64
	if err := pg.QueryRow(`SELECT task_id,node_id FROM findings WHERE id=$1`, findingID).Scan(&findingTaskID, &findingSourceNodeID); err != nil {
		t.Fatal(err)
	}
	if parentFindings != 1 || childFindings != 0 || findingTaskID != parentID || findingSourceNodeID != findingNodeID {
		t.Fatalf("Finding 记录被重复或改指向: parent=%d child=%d task=%d node=%d", parentFindings, childFindings, findingTaskID, findingSourceNodeID)
	}
}

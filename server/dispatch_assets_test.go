package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestDispatcherUsesFindingAssetDedupe(t *testing.T) {
	p := agent.VulnDispatcherDefaultPrompt
	for _, want := range []string{
		"check_internal_asset_task", "产生立足点/Finding 的实际资产",
		"任务 id 等于当前触发任务", "服务端会用规范化资产键和唯一约束",
		"spawn_task 必须传 finding_id",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("Dispatcher 缺少资产去重规则 %q", want)
		}
	}
	if strings.Contains(p, "再用 get_task_graph 查父任务的 child_tasks，确认是否已有同资产子任务") {
		t.Fatal("Dispatcher 仍把直接 child_tasks 当作资产去重依据")
	}
}

func TestCheckInternalAssetTaskUsesFindingAssets(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	parent, err := m.CreateTaskWithOptions("外网父任务", "验证目标", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parentID := mustTaskID(t, parent.ID)
	stamp := time.Now().UnixNano()
	firstIP := fmt.Sprintf("198.51.%d.%d", (stamp/200)%200+1, stamp%200+1)
	lastOctet := stamp%200 + 2
	secondIP := fmt.Sprintf("198.51.%d.%d", (stamp/200)%200+1, lastOctet)
	firstAsset, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: firstIP, TaskID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	secondAsset, err := m.pg.Assets().UpsertIP(db.UpsertIPReq{IP: secondIP, TaskID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := parent.Store.AddNode("finding", map[string]any{
		"summary": "两个入口属于不同主机", "vulnclass": "getshell", "severity": "critical",
	}, 0, "confirmed", "worker", []int64{firstAsset, secondAsset})
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := m.pg.AddFinding(parentID, nodeID, "getshell", "多主机立足点", "critical",
		"summary", "evidence", "worker", []int64{firstAsset, secondAsset})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}
	if _, err := s.findingDispatchAssetKeys(findingID, ""); err == nil || !strings.Contains(err.Error(), "target_asset") {
		t.Fatalf("multiple hosts must require target_asset: %v", err)
	}
	keys, err := s.findingDispatchAssetKeys(findingID, firstIP)
	if err != nil || len(keys) != 1 || keys[0] != "ip:"+firstIP {
		t.Fatalf("selected keys = %v, %v", keys, err)
	}
	if _, err := s.findingDispatchAssetKeys(findingID, "192.0.2.99"); err == nil {
		t.Fatal("target_asset outside the Finding assets must be rejected")
	}
	category, err := m.pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil {
		t.Fatal(err)
	}
	if category == nil {
		if _, err := m.pg.SeedTaskCategoryPresetIfEmpty("内网渗透", true, 0, 0, false, ""); err != nil {
			t.Fatal(err)
		}
		category, err = m.pg.GetTaskCategoryByNKey("内网渗透")
		if err != nil || category == nil {
			t.Fatalf("读取内网分类失败: %v", err)
		}
	}
	legacyNodeID, err := parent.Store.AddNode("finding", map[string]any{
		"summary": "同资产旧 Finding", "vulnclass": "getshell", "severity": "critical",
	}, 0, "confirmed", "worker", []int64{firstAsset})
	if err != nil {
		t.Fatal(err)
	}
	legacyFindingID, err := m.pg.AddFinding(parentID, legacyNodeID, "getshell", "同资产旧 Finding", "critical",
		"summary", "evidence", "worker", []int64{firstAsset})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.CreateTaskWithOptions(fmt.Sprintf("finding_id=%d 的旧内网任务", legacyFindingID), "深化", db.TaskCreateOptions{
		CategoryID: &category.ID, SourceTaskIDs: []int64{parentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.backfillTaskDispatchTargets()
	res, err := s.toolCheckInternalAssetTask().Call(context.Background(), json.RawMessage(
		fmt.Sprintf(`{"finding_id":%d,"target_asset":%q}`, findingID, firstIP)), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Flatten()
	if !strings.Contains(text, `"exists":true`) || !strings.Contains(text, fmt.Sprintf(`"task_id":%d`, mustTaskID(t, child.ID))) {
		t.Fatalf("asset check did not find the existing internal task: %s", text)
	}
}

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
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
	if !strings.Contains(prompt, "内网渗透 · <立足点 host>") || !strings.Contains(prompt, "name：必须填写简短任务名") {
		t.Fatal("dispatcher prompt must separate the short task name from the detailed description")
	}
	if !strings.Contains(prompt, "不复制父 task_constraints") || !strings.Contains(prompt, "不默认生成单资产限制") {
		t.Fatal("dispatcher must inherit evidence without copying single-asset execution constraints")
	}
	for _, scene := range []string{"内网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"} {
		if !strings.Contains(prompt, "planner_template_nkey="+scene) {
			t.Errorf("missing stable template reference for %s", scene)
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

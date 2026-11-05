package agent

// ═══ ARTEX 二开 · [P-TPL·DOMAIN 五领域模板单测] ═══
// 与 server.validatePlannerTemplateBody 同口径：missingkey=error 解析 + 假 PlannerVars 试渲染；
// 并断言名称 ≤120 字符、正文 ≤32KB、nkey（名称归一化）互不重复、每套恰好只引用 {{.Goal}}。

import (
	"strings"
	"testing"
	"text/template"
	"unicode/utf8"
)

func TestPlannerDomainSeedsValid(t *testing.T) {
	seeds := PlannerDomainSeeds()
	if len(seeds) != 5 {
		t.Fatalf("expected 5 domain templates, got %d", len(seeds))
	}
	seen := map[string]string{}
	vars := PlannerVars{Goal: "测试目标", Scope: "example.com", AssetSummary: "1 资产", DataDir: "/tmp/x", Now: "2026-10-24"}
	for name, body := range seeds {
		if utf8.RuneCountInString(name) > 120 {
			t.Errorf("%s: name too long (%d runes)", name, utf8.RuneCountInString(name))
		}
		if len(body) > 32*1024 {
			t.Errorf("%s: body too large (%d bytes)", name, len(body))
		}
		nkey := strings.ToLower(strings.TrimSpace(name))
		if prev, dup := seen[nkey]; dup {
			t.Errorf("nkey %q duplicated by %q and %q", nkey, prev, name)
		}
		seen[nkey] = name
		if !strings.Contains(body, "{{.Goal}}") {
			t.Errorf("%s: body missing {{.Goal}}", name)
		}
		tmpl, err := template.New("p").Option("missingkey=error").Parse(body)
		if err != nil {
			t.Errorf("%s: parse error: %v", name, err)
			continue
		}
		var sb strings.Builder
		if err := tmpl.Execute(&sb, vars); err != nil {
			t.Errorf("%s: render error: %v", name, err)
		}
	}
}

// 公共骨架六条不变量的关键锚点必须在每套模板中出现（防领域层改动骨架）。
func TestPlannerDomainSeedsKeepSkeleton(t *testing.T) {
	anchors := []string{
		"你是规划者、不是执行者",        // ① 角色契约
		"必须】产出至少一个",             // ② 硬底线
		"prove_goal(goal_id",          // ③ 判定目标
		"完整态势已附在本轮用户输入",      // ④ 态势消费
		"recent_done",                  // ⑤ 方向纪律
		"add_intent",                   // ⑥ 提交格式
	}
	for name, body := range PlannerDomainSeeds() {
		for _, a := range anchors {
			if !strings.Contains(body, a) {
				t.Errorf("%s: skeleton anchor %q missing", name, a)
			}
		}
	}
}

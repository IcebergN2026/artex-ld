// planner_guard_prompt_migration_test.go：planner Guard 提示词迁移的纯转换契约测试
// （始终运行）与 PostgreSQL 迁移测试（需 ARTEX_PG_DSN，无则 skip）。
package server

import (
	"os"
	"strings"
	"testing"

	"artex/db"
)

// 构造历史样例正文：判目标 → 探测段 → 决定补方向 → 提交（审核后缀可选）。
func plannerGuardLegacyBody(reviewSuffix bool) string {
	b := "头部\n2. **判目标（核心职责）**：x\n" +
		plannerGuardProbeStartAnchor + "：仅当图里几乎还没有 fact……三条硬边界：\n" +
		"   - a\n   - b\n   - c\n" +
		plannerGuardProbeStopAnchor + "：y\n" +
		"5. **提交**：用【一次】add_intent 批量提交筛出的新方向（intents 数组，最多 4 个最高价值的，不要逐条多次调）"
	if reviewSuffix {
		b += plannerGuardReviewSubmitSuffix
	} else {
		b += "："
	}
	return b + "\n   - summary 行\n尾部"
}

func TestTransformPlannerGuardPromptBodyNonIntranet(t *testing.T) {
	got, changed, residue := transformPlannerGuardPromptBody(plannerGuardLegacyBody(true), false)
	if !changed || residue {
		t.Fatalf("expected changed without residue, got changed=%v residue=%v", changed, residue)
	}
	if strings.Contains(got, plannerGuardProbeStartAnchor) {
		t.Fatalf("probe clause not removed: %q", got)
	}
	for _, want := range []string{
		"3. **决定补哪些新方向**",
		"4. **提交**：用【一次】add_intent 批量提交筛出的新方向（intents 数组，最多 4 个最高价值的，不要逐条多次调）：",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in: %q", want, got)
		}
	}
	if strings.Contains(got, "add_review_intent") {
		t.Fatalf("non-intranet row must drop review submit wording: %q", got)
	}
	// 幂等：二次执行逐字不变。
	again, changed2, _ := transformPlannerGuardPromptBody(got, false)
	if changed2 || again != got {
		t.Fatalf("transform must be idempotent: changed2=%v", changed2)
	}
}

func TestTransformPlannerGuardPromptBodyIntranetKeepsReview(t *testing.T) {
	got, changed, residue := transformPlannerGuardPromptBody(plannerGuardLegacyBody(true), true)
	if !changed || residue {
		t.Fatalf("expected changed without residue, got changed=%v residue=%v", changed, residue)
	}
	if strings.Contains(got, plannerGuardProbeStartAnchor) {
		t.Fatalf("probe clause not removed: %q", got)
	}
	if !strings.Contains(got, plannerGuardReviewSubmitSuffix) {
		t.Fatalf("intranet row must keep review submit wording: %q", got)
	}
	if !strings.Contains(got, "4. **提交**") {
		t.Fatalf("submit line must be renumbered to 4: %q", got)
	}
	again, changed2, _ := transformPlannerGuardPromptBody(got, true)
	if changed2 || again != got {
		t.Fatalf("transform must be idempotent: changed2=%v", changed2)
	}
}

func TestTransformPlannerGuardPromptBodyMissingAnchors(t *testing.T) {
	// 缺起锚：不切割、不改动。
	custom := "自定义正文\n2. **判目标**：x\n3. **决定补哪些新方向**：y"
	got, changed, _ := transformPlannerGuardPromptBody(custom, false)
	if changed || got != custom {
		t.Fatalf("body without anchors must remain unchanged")
	}
	// 有起锚缺止锚：不切割，报告残留供人工核对。
	broken := "头部\n" + plannerGuardProbeStartAnchor + "：残缺段（无止锚）"
	got, changed, residue := transformPlannerGuardPromptBody(broken, false)
	if changed || got != broken {
		t.Fatalf("body with broken anchors must not be cut")
	}
	if !residue {
		t.Fatal("broken-anchor body must report probe residue")
	}
}

func TestTransformPlannerGuardPromptBodyUserModified(t *testing.T) {
	// 用户自定义文本：无锚无审核后缀，两个转换都不生效。
	custom := "## 我的自定义 planner 模板\n随便写的提交说明 add_review_intent 字样但不是历史行"
	got, changed, _ := transformPlannerGuardPromptBody(custom, false)
	if changed || got != custom {
		t.Fatal("user-modified body must remain unchanged")
	}
}

func TestUpgradePlannerGuardPrompts(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("ARTEX_PG_DSN is not set")
	}
	pg, err := db.Open(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pg.Close()

	// 备份现场：内置六行、当前 planner prompt、settings 标记、自定义行。
	type bak struct {
		id   int64
		body string
	}
	var tplBak []bak
	rows, err := pg.Query(`SELECT id,body FROM planner_templates WHERE builtin`)
	if err != nil {
		t.Skipf("builtin planner templates unavailable: %v", err)
	}
	for rows.Next() {
		var b bak
		if err := rows.Scan(&b.id, &b.body); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tplBak = append(tplBak, b)
	}
	rows.Close()
	defer func() {
		for _, b := range tplBak {
			_, _ = pg.Exec(`UPDATE planner_templates SET body=$1 WHERE id=$2`, b.body, b.id)
		}
		_, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey='guardmig-自定义'`)
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, plannerGuardPromptMigrationKey)
	}()

	// 把内置行改回历史形态：非内网带探测段+审核后缀；内网带探测段+审核后缀。
	for _, nkey := range []string{"外网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"} {
		if _, err := pg.Exec(`UPDATE planner_templates SET body=$1 WHERE builtin AND nkey=$2`, plannerGuardLegacyBody(true), nkey); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.Exec(`UPDATE planner_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, plannerGuardLegacyBody(true)); err != nil {
		t.Fatal(err)
	}
	// 非 builtin 自定义行：含历史文字但不得被迁移改动。
	customBody := "custom\n" + plannerGuardLegacyBody(true)
	if _, err := pg.Exec(`INSERT INTO planner_templates(name, nkey, body, builtin, is_default) VALUES ('guardmig-自定义', 'guardmig-自定义', $1, false, false)`, customBody); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, plannerGuardPromptMigrationKey); err != nil {
		t.Fatal(err)
	}

	// 首次迁移。
	if err := upgradePlannerGuardPrompts(pg); err != nil {
		t.Fatal(err)
	}
	for _, nkey := range []string{"外网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"} {
		var body string
		if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey=$1`, nkey).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, plannerGuardProbeStartAnchor) {
			t.Fatalf("%s: probe clause not removed", nkey)
		}
		if strings.Contains(body, "add_review_intent") {
			t.Fatalf("%s: review submit wording must be dropped", nkey)
		}
		if !strings.Contains(body, "4. **提交**") {
			t.Fatalf("%s: submit not renumbered: %q", nkey, body)
		}
	}
	var intranet string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&intranet); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(intranet, plannerGuardProbeStartAnchor) || !strings.Contains(intranet, plannerGuardReviewSubmitSuffix) {
		t.Fatal("intranet row must drop probe clause but keep review submit wording")
	}
	var customAfter string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE nkey='guardmig-自定义'`).Scan(&customAfter); err != nil {
		t.Fatal(err)
	}
	if customAfter != customBody {
		t.Fatal("non-builtin custom template must remain unchanged")
	}

	// guard key 已写；二次执行为 no-op（正文不变）。
	if _, ok, _ := pg.GetSetting(plannerGuardPromptMigrationKey); !ok {
		t.Fatal("guard key must be written after first run")
	}
	var before string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := upgradePlannerGuardPrompts(pg); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("second run must be a no-op")
	}
}

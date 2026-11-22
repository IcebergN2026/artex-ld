// planner_intranet_credential_migration_test.go：内网 Planner 凭据闭环迁移的
// 幂等与边界契约测试（复刻 worker_topology_migration_test.go 的模式）。
package server

import (
	"os"
	"strings"
	"testing"

	"artex/db"
)

func TestInjectIntranetPlannerCredentialLoop(t *testing.T) {
	body := "prefix\n" + intranetPlannerCredentialOldLine + "\nsuffix"
	got := injectIntranetPlannerCredentialLoop(body)
	if !strings.Contains(got, "凭据全程走凭据表闭环") {
		t.Fatalf("credential loop not injected: %s", got)
	}
	if !strings.Contains(got, intranetPlannerCredentialNewLine) || !strings.HasSuffix(got, "\nsuffix") {
		t.Fatalf("old line not replaced in place: %s", got)
	}
	// 幂等：二次注入不重复替换。
	if again := injectIntranetPlannerCredentialLoop(got); again != got {
		t.Fatal("injection must be idempotent")
	}
	// 用户改过、旧行不匹配的正文保持不变。
	custom := "## 自定义内网模板\n     · 据凭据 fact：用户改写的内容"
	if injectIntranetPlannerCredentialLoop(custom) != custom {
		t.Fatal("custom body without the built-in line must remain unchanged")
	}
}

func TestUpgradeIntranetPlannerCredentialLoop(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("ARTEX_PG_DSN is not set")
	}
	pg, err := db.Open(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pg.Close()

	// 备份现场：内置行、一条非 builtin 行、settings 标记。
	var original string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&original); err != nil {
		t.Skipf("builtin intranet planner template unavailable: %v", err)
	}
	defer func() {
		_, _ = pg.Exec(`UPDATE planner_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, original)
		_, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey='credmig-自定义内网'`)
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, plannerIntranetCredentialMigrationKey)
	}()

	oldBody := "header\n" + intranetPlannerCredentialOldLine + "\nfooter"
	if _, err := pg.Exec(`UPDATE planner_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, oldBody); err != nil {
		t.Fatal(err)
	}
	// 非 builtin 行：含旧行但不得被迁移改动。
	customBody := "custom\n" + intranetPlannerCredentialOldLine
	if _, err := pg.Exec(`INSERT INTO planner_templates(name, nkey, body, builtin, is_default) VALUES ('credmig-自定义内网', 'credmig-自定义内网', $1, false, false)`, customBody); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, plannerIntranetCredentialMigrationKey); err != nil {
		t.Fatal(err)
	}

	// 首次执行：内置行注入新文本，非 builtin 行不变。
	if err := upgradeIntranetPlannerCredentialLoop(pg); err != nil {
		t.Fatal(err)
	}
	var upgraded string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&upgraded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upgraded, "凭据全程走凭据表闭环") {
		t.Fatalf("migration did not inject credential loop: %s", upgraded)
	}
	var customAfter string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE nkey='credmig-自定义内网'`).Scan(&customAfter); err != nil {
		t.Fatal(err)
	}
	if customAfter != customBody {
		t.Fatalf("non-builtin template must remain unchanged: %s", customAfter)
	}

	// 幂等标记已写入；二次执行正文不变。
	if err := upgradeIntranetPlannerCredentialLoop(pg); err != nil {
		t.Fatal(err)
	}
	var again string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != upgraded {
		t.Fatal("second run must be a no-op")
	}

	// 用户改过导致旧行不匹配：清除标记后重跑也不覆盖用户文本。
	userBody := "用户自定义的内网模板（旧行已被删除）"
	if _, err := pg.Exec(`UPDATE planner_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, userBody); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, plannerIntranetCredentialMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := upgradeIntranetPlannerCredentialLoop(pg); err != nil {
		t.Fatal(err)
	}
	var untouched string
	if err := pg.QueryRow(`SELECT body FROM planner_templates WHERE builtin AND nkey='内网渗透'`).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != userBody {
		t.Fatal("user-modified builtin template must not be overwritten")
	}
}

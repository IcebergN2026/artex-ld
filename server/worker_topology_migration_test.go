package server

import (
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestInjectInternalWorkerTopologyRule(t *testing.T) {
	body := "## 当前场景：内网渗透\n- 收集事实\n- 获得新主机控制权后登记证据"
	got := injectInternalWorkerTopologyRule(body)
	if !strings.Contains(got, "record_topology_relation") || strings.Count(got, internalWorkerTopologyAnchor) != 1 {
		t.Fatalf("topology rule not injected: %s", got)
	}
	if again := injectInternalWorkerTopologyRule(got); again != got {
		t.Fatal("injection must be idempotent")
	}
	custom := "## 自定义内网模板"
	if injectInternalWorkerTopologyRule(custom) != custom {
		t.Fatal("custom body without the built-in anchor must remain unchanged")
	}
}

func TestUpgradeInternalWorkerTopologyPrompt(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("ARTEX_PG_DSN is not set")
	}
	pg, err := db.Open(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pg.Close()
	var original string
	if err := pg.QueryRow(`SELECT body FROM worker_templates WHERE builtin AND nkey='内网渗透'`).Scan(&original); err != nil {
		t.Skipf("internal worker template unavailable: %v", err)
	}
	defer func() {
		_, _ = pg.Exec(`UPDATE worker_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, original)
		_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, workerTopologyMigrationKey)
	}()
	oldBody := "## 当前场景：内网渗透\n- 发现可复用凭据、配置或拓扑事实时 record_fact；\n- 获得新主机控制权后登记证据"
	if _, err := pg.Exec(`UPDATE worker_templates SET body=$1 WHERE builtin AND nkey='内网渗透'`, oldBody); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, workerTopologyMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := upgradeInternalWorkerTopologyPrompt(pg); err != nil {
		t.Fatal(err)
	}
	var upgraded string
	if err := pg.QueryRow(`SELECT body FROM worker_templates WHERE builtin AND nkey='内网渗透'`).Scan(&upgraded); err != nil {
		t.Fatal(err)
	}
	if strings.Count(upgraded, "record_topology_relation") != 1 {
		t.Fatalf("migration did not add exactly one topology rule: %s", upgraded)
	}
	if err := upgradeInternalWorkerTopologyPrompt(pg); err != nil {
		t.Fatal(err)
	}
}

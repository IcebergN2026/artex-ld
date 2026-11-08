// external_review_migration.go：将外网渗透分类的旧默认审核开关迁移为关闭。
// 主要函数/类型：
//   - upgradeExternalPentestReviewDefault：一次性把「外网渗透」分类的默认审核开关迁到关闭。

package server

import (
	"log"

	"artex/db"
)

// upgradeExternalPentestReviewDefault disables the old external-pentest default
// once. A later manual re-enable is preserved because the migration flag is set.
func upgradeExternalPentestReviewDefault(pg *db.DB) {
	const flag = "external_pentest_review_default_off_v1"
	if value, _, _ := pg.GetSetting(flag); value == "true" {
		return
	}
	if _, err := pg.Exec(`UPDATE task_categories SET default_review_enabled=false,updated_at=now() WHERE nkey='外网渗透' AND default_review_enabled=true`); err != nil {
		log.Printf("[review] 外网渗透默认审核关闭迁移失败: %v", err)
		return
	}
	if err := pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[review] 外网渗透默认审核迁移标记失败: %v", err)
	}
}

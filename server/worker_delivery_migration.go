// worker_delivery_migration.go：向内置 worker 模板注入「投放」fact 规格的一次性迁移。
// 主要函数/类型：
//   - injectWorkerDeliveryFactSpec：仅对含原锚点的内置模板正文注入投放契约（幂等，不重复注入）。
//   - upgradeWorkerDeliveryFacts：扫描内置模板与 worker 当前提示词执行注入，完成后打幂等标记。
package server

import (
	"database/sql"
	"fmt"
	"strings"

	"artex/agent"
	db "artex/db"
)

const workerDeliveryFactMigrationKey = "worker_delivery_facts_v1"
const workerReportFindingMarker = "\n- **确认漏洞 → report_finding"

// injectWorkerDeliveryFactSpec upgrades the original built-in prompt shape only.
// A custom body without that anchor is left alone; an existing [投放] contract is
// never duplicated. Existing task prompt snapshots are intentionally untouched.
func injectWorkerDeliveryFactSpec(body string) string {
	if strings.Contains(body, "[投放]") || !strings.Contains(body, workerReportFindingMarker) {
		return body
	}
	return strings.Replace(body, workerReportFindingMarker, "\n"+agent.WorkerDeliveryFactSpec+workerReportFindingMarker, 1)
}

func upgradeWorkerDeliveryFacts(pg *db.DB) error {
	_, ok, err := pg.GetSetting(workerDeliveryFactMigrationKey)
	if err != nil || ok {
		return err
	}
	rows, err := pg.Query(`SELECT id,body FROM worker_templates WHERE builtin`)
	if err != nil {
		return fmt.Errorf("list built-in worker templates for delivery migration: %w", err)
	}
	type templateBody struct {
		id   int64
		body string
	}
	var templates []templateBody
	for rows.Next() {
		var row templateBody
		if err := rows.Scan(&row.id, &row.body); err != nil {
			rows.Close()
			return err
		}
		templates = append(templates, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, row := range templates {
		body := injectWorkerDeliveryFactSpec(row.body)
		if body == row.body {
			continue
		}
		if _, err := pg.Exec(`UPDATE worker_templates SET body=$1,updated_at=now() WHERE id=$2`, body, row.id); err != nil {
			return fmt.Errorf("upgrade worker template %d delivery facts: %w", row.id, err)
		}
	}
	var current string
	err = pg.QueryRow(`SELECT p.template_text FROM agent_prompts p JOIN agents a ON a.current_prompt_id=p.id WHERE a.key='worker'`).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read current worker prompt for delivery migration: %w", err)
	}
	if err == nil {
		body := injectWorkerDeliveryFactSpec(current)
		if body != current {
			if _, err := pg.Exec(`UPDATE agent_prompts p SET template_text=$1,updated_by='system' FROM agents a WHERE p.id=a.current_prompt_id AND a.key='worker'`, body); err != nil {
				return fmt.Errorf("upgrade current worker delivery facts: %w", err)
			}
		}
	}
	return pg.SetSetting(workerDeliveryFactMigrationKey, "done")
}

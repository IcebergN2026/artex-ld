// planner_intranet_credential_migration.go：向内置「内网渗透」planner 模板注入
// 凭据闭环规则的一次性迁移（幂等，模式复刻 worker_topology_migration.go）。
// 主要函数/类型：
//   - injectIntranetPlannerCredentialLoop：旧「据凭据 fact」行整行替换为凭据闭环版（幂等）。
//   - upgradeIntranetPlannerCredentialLoop：扫描并更新内置内网渗透模板，完成后打幂等标记。
package server

import (
	"fmt"
	"strings"

	"artex/db"
)

const plannerIntranetCredentialMigrationKey = "planner_intranet_credential_loop_v1"

// 旧行与新行均为 plannerDomainIntranetTmpl 内的整行文本；新行不含测试禁用子串。
const intranetPlannerCredentialOldLine = `     · 据凭据 fact：凭据复用与横向移动（SMB 认证/WMI/WinRM/PsExec/计划任务等）——summary 写清"从哪台主机、用哪个凭据/入口、打哪个目标"；`
const intranetPlannerCredentialNewLine = `     · 据凭据 fact：凭据复用与横向移动（SMB 认证/WMI/WinRM/PsExec/计划任务等）——summary 写清"从哪台主机、用哪个凭据/入口、打哪个目标"；凭据全程走凭据表闭环：派意图时要求 Worker 新发现的凭据一律 record_credential 登记、认证成功一律 mark_credential_verified 标记（支持跨主机）；规划横向前先用 list_assets 查看已控主机与目标的 auth 条目——relation=verified 的凭据直接复用、不重复验证，仅有 relation=discovered 的凭据先派一条验证意图再横向；`

// injectIntranetPlannerCredentialLoop 把内置「内网渗透」planner 模板里的旧
// 「据凭据 fact」行替换为凭据闭环版。已含新文本或旧行不匹配（用户改过）时原样返回。
func injectIntranetPlannerCredentialLoop(body string) string {
	if strings.Contains(body, "凭据全程走凭据表闭环") || !strings.Contains(body, intranetPlannerCredentialOldLine) {
		return body
	}
	return strings.Replace(body, intranetPlannerCredentialOldLine, intranetPlannerCredentialNewLine, 1)
}

// upgradeIntranetPlannerCredentialLoop 只迁移 builtin 且 nkey='内网渗透' 的
// planner_templates 行：用户改过导致旧行不匹配的行保持原样，绝不覆盖用户自定义文本。
func upgradeIntranetPlannerCredentialLoop(pg *db.DB) error {
	_, ok, err := pg.GetSetting(plannerIntranetCredentialMigrationKey)
	if err != nil || ok {
		return err
	}
	rows, err := pg.Query(`SELECT id,body FROM planner_templates WHERE builtin AND nkey='内网渗透'`)
	if err != nil {
		return err
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
		body := injectIntranetPlannerCredentialLoop(row.body)
		if body == row.body {
			continue
		}
		if _, err := pg.Exec(`UPDATE planner_templates SET body=$1,updated_at=now() WHERE id=$2`, body, row.id); err != nil {
			return fmt.Errorf("upgrade intranet planner credential loop: %w", err)
		}
	}
	return pg.SetSetting(plannerIntranetCredentialMigrationKey, "done")
}

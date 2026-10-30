package server

import (
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const workerTopologyMigrationKey = "worker_topology_relations_v1"
const internalWorkerTopologyAnchor = "\n- 获得新主机控制权后"
const internalWorkerTopologyRule = "\n- 确认网卡、路由、容器网络或会话连接后，先 record_fact 保存证据，再调用 record_topology_relation(parent_ip, node_ip, label, evidence_ref=fact:<返回的节点 id>)；label 按实际关系自定义。找不到证据就保持独立节点，禁止因同任务、同网段或存在入口而猜测连线。"

func injectInternalWorkerTopologyRule(body string) string {
	if strings.Contains(body, "record_topology_relation") || !strings.Contains(body, "## 当前场景：内网渗透") || !strings.Contains(body, internalWorkerTopologyAnchor) {
		return body
	}
	return strings.Replace(body, internalWorkerTopologyAnchor, internalWorkerTopologyRule+internalWorkerTopologyAnchor, 1)
}

func upgradeInternalWorkerTopologyPrompt(pg *db.DB) error {
	_, ok, err := pg.GetSetting(workerTopologyMigrationKey)
	if err != nil || ok {
		return err
	}
	rows, err := pg.Query(`SELECT id,body FROM worker_templates WHERE builtin AND nkey='内网渗透'`)
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
		body := injectInternalWorkerTopologyRule(row.body)
		if body == row.body {
			continue
		}
		if _, err := pg.Exec(`UPDATE worker_templates SET body=$1,updated_at=now() WHERE id=$2`, body, row.id); err != nil {
			return fmt.Errorf("upgrade internal Worker topology prompt: %w", err)
		}
	}
	return pg.SetSetting(workerTopologyMigrationKey, "done")
}

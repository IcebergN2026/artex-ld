// migrate_topology.go：任务拓扑表（task_topology_nodes/edges）的可重入启动迁移与数据回填。
// 主要函数/类型：
//   - ensureTaskTopologyColorColumns / ensureTaskTopologyDescriptionColumn：按需补齐拓扑颜色列与描述列。
//   - ensureTaskTopologyTwoKinds：重建边类型约束，去掉 lateral/pending 旧枚举值。
//   - rewriteTopologySystemEdgeLabels：二次启动时仅重写新增系统边的 label。
//   - migrateTaskTopologyOverrides：拓扑启动迁移的入口编排（先补列再做 legacy 早退）。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateTaskTopologyOverrides moves the legacy read-time topology projection
// (task_topology_overrides: one row per node_ip with an optional parent_ip edge
// and entry_status) into the three-table editor model, then drops the legacy
// table.
//
// Mapping per row: node_ip becomes a task_topology_nodes row (display_label and
// entry_status carried over), parent_ip becomes a node too when it is missing so
// the edge always has both endpoints, and parent_ip→node_ip becomes one edge —
// kind=solid for an entry row (entry_status set), else kind=pending, because the
// editor's kind set has no "evidence"; the old evidence_ref is preserved. Rows
// without a parent leave their node edge-less, so the editor's fallback pending
// edge is added afterwards (the editor forbids 游离节点).
//
// Idempotent: it no-ops once task_topology_overrides is gone, so every startup
// can call it under the schema advisory lock.
// ensureTaskTopologyColorColumns 给存量库补齐节点/连线自定义颜色列 color
// （TEXT NOT NULL，默认空串；空串=默认配色：节点灰蓝边框，连线退回线型颜色）。
// 新库由 schema.sql 建表自带；建表早于该列的存量库（如已搬迁完、legacy 表已
// 删除的库）走这里修复。幂等：先探 to_regclass 再探 information_schema.columns，
// 缺列才 ADD COLUMN——不加 IF NOT EXISTS，保证在任意部署版本的 PG 上都可重入。
func ensureTaskTopologyColorColumns(ctx context.Context, conn *sql.Conn) error {
	for _, table := range []string{"task_topology_nodes", "task_topology_edges"} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return fmt.Errorf("probe %s: %w", table, err)
		}
		if !exists {
			continue
		}
		var hasColor bool
		if err := conn.QueryRowContext(ctx, `SELECT count(*) > 0 FROM information_schema.columns
WHERE table_name = $1 AND column_name = 'color'`, table).Scan(&hasColor); err != nil {
			return fmt.Errorf("probe %s.color: %w", table, err)
		}
		if hasColor {
			continue
		}
		if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN color TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add %s.color: %w", table, err)
		}
	}
	return nil
}

// ensureTaskTopologyDescriptionColumn 给存量库补齐节点描述列 description
// （TEXT NOT NULL，默认空串；空串=节点卡片不显示第三行）。新库由 schema.sql
// 建表自带；建表早于该列的存量库走这里修复。幂等：先探 to_regclass 再探
// information_schema.columns，缺列才 ADD COLUMN——不加 IF NOT EXISTS，保证在
// 任意部署版本的 PG 上都可重入。
func ensureTaskTopologyDescriptionColumn(ctx context.Context, conn *sql.Conn) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('task_topology_nodes') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("probe task_topology_nodes: %w", err)
	}
	if !exists {
		return nil
	}
	var has bool
	if err := conn.QueryRowContext(ctx, `SELECT count(*) > 0 FROM information_schema.columns
WHERE table_name = 'task_topology_nodes' AND column_name = 'description'`).Scan(&has); err != nil {
		return fmt.Errorf("probe task_topology_nodes.description: %w", err)
	}
	if has {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE task_topology_nodes ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add task_topology_nodes.description: %w", err)
	}
	// 存量库刚补列：立刻从每个资产最新 confirmed fact 预填一次（人工已有的
	// 描述为空才填，之后重启不再回填，清空描述不会被自动盖回）。
	if _, err := conn.ExecContext(ctx, backfillTopologyDescriptionSQL); err != nil {
		return fmt.Errorf("backfill task_topology_nodes.description: %w", err)
	}
	return nil
}

// backfillTopologyDescriptionSQL 用资产锚定的最新 confirmed fact 的 summary
// （缺 summary 回退 text）预填节点描述。exploration_anchors 把 fact 挂到资产，
// assets.task_ids 把资产挂到任务，两条边一合就是"该任务里这个 IP 的一句话事实"。
const backfillTopologyDescriptionSQL = `
UPDATE task_topology_nodes n
SET description = left(sub.summary, 300)
FROM (
  SELECT DISTINCT ON (n2.task_id, n2.ip) n2.task_id, n2.ip,
         COALESCE(NULLIF(en.payload->>'summary',''), en.payload->>'text','') AS summary
  FROM task_topology_nodes n2
  JOIN assets a ON a.ip = n2.ip AND n2.task_id = ANY(a.task_ids)
  JOIN exploration_anchors ea ON ea.asset_id = a.id
  JOIN exploration_nodes en ON en.id = ea.node_id AND en.kind='fact' AND en.state='confirmed'
  ORDER BY n2.task_id, n2.ip, en.id DESC
) sub
WHERE n.task_id = sub.task_id AND n.ip = sub.ip AND n.description = '' AND sub.summary <> ''`

// ensureTaskTopologyTwoKinds 把连线线型收敛为两种（solid=已突破 / reachable=可达）：
// 存量库的 lateral/pending 行并入 reachable（与既有 reachable 唯一键冲突的行直接删除，
// 保留先到的 reachable），并把 kind CHECK 约束换成新枚举。幂等：行迁移无条件可重入，
// 约束只在定义里仍含 lateral/pending 时才重建。
func ensureTaskTopologyTwoKinds(ctx context.Context, conn *sql.Conn) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('task_topology_edges') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("probe task_topology_edges: %w", err)
	}
	if !exists {
		return nil
	}
	stmts := []string{
		// 与既有 reachable 撞唯一键 (task_id,src_ip,dst_ip,kind) 的 lateral/pending 行删除。
		`DELETE FROM task_topology_edges e WHERE e.kind IN ('lateral','pending')
  AND EXISTS (SELECT 1 FROM task_topology_edges r
              WHERE r.task_id=e.task_id AND r.src_ip=e.src_ip AND r.dst_ip=e.dst_ip
                AND r.kind='reachable' AND r.id <> e.id)`,
		`UPDATE task_topology_edges SET kind='reachable' WHERE kind IN ('lateral','pending')`,
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("converge topology edge kinds: %w", err)
		}
	}
	var conName string
	if err := conn.QueryRowContext(ctx, `SELECT conname FROM pg_constraint
WHERE conrelid = 'task_topology_edges'::regclass AND contype = 'c'
  AND pg_get_constraintdef(oid) LIKE '%kind%' LIMIT 1`).Scan(&conName); err != nil {
		if err == sql.ErrNoRows {
			return nil // 无 kind 约束（理论上不存在），无需处理
		}
		return fmt.Errorf("probe kind constraint: %w", err)
	}
	var def string
	if err := conn.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`, conName).Scan(&def); err != nil {
		return fmt.Errorf("read kind constraint def: %w", err)
	}
	if !strings.Contains(def, "lateral") && !strings.Contains(def, "pending") {
		return nil // 已是新枚举
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE task_topology_edges DROP CONSTRAINT `+conName); err != nil {
		return fmt.Errorf("drop old kind constraint: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE task_topology_edges ADD CONSTRAINT task_topology_edges_kind_check
CHECK (kind IN ('solid','reachable'))`); err != nil {
		return fmt.Errorf("add two-kind constraint: %w", err)
	}
	return nil
}

// rewriteTopologySystemEdgeLabels 把系统生成边的 label 统一为「如何发现的」格式：
// finding 证据 →「通过<漏洞名截20字>突破」；fact 证据 → topology.discovery（缺省回退
// summary 截28字）；人工边（evidence_ref=”）只补空 label 的兜底文案，其余不碰。
// 标记重跑避免：处理过的行写回 system_label 标记列（第一次迁移时 ADD COLUMN），
// 二次启动只影响新出现的系统边，已处理行不再重写（也尊重人工事后改 label）。
func rewriteTopologySystemEdgeLabels(ctx context.Context, conn *sql.Conn) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('task_topology_edges') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("probe task_topology_edges: %w", err)
	}
	if !exists {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE task_topology_edges ADD COLUMN IF NOT EXISTS system_label TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add system_label column: %w", err)
	}
	// 1) finding 证据边：「通过<名>突破」。标签不是系统规则（既没有 finding: 证据也不
	//    是兜底文案）的行视为人工改过的，跳过。
	const findingSQL = `UPDATE task_topology_edges e
SET label = CASE
  WHEN trim(coalesce(f.name,'')) = '' THEN '通过漏洞突破'
  ELSE '通过' || left(trim(f.name), 20) || '突破' END,
    system_label = 'finding:' || e.evidence_ref
FROM findings f
WHERE e.evidence_ref LIKE 'finding:%'
  AND e.system_label = ''
  AND f.id = substring(e.evidence_ref from 'finding:([0-9]+)')::bigint
  AND f.task_id = e.task_id
  AND (e.label = '' OR e.label = $1 OR e.label = trim(f.name))`
	if _, err := conn.ExecContext(ctx, findingSQL, TopologyFallbackLabel); err != nil {
		return fmt.Errorf("rewrite finding edge labels: %w", err)
	}
	// 2) fact 证据边：优先 topology.discovery（单一事实源），无则回退 summary 截28字。
	//    只碰规则内标签。RecordTaskTopologyRelation 插入时已打 system_label，这里只
	//    处理更早的遗留行。
	const factSQL = `UPDATE task_topology_edges e
SET label = left(s.label, 28), system_label = e.evidence_ref
FROM (
  SELECT n.id, coalesce(nullif(n.payload->'topology'->>'discovery',''), nullif(n.payload->>'summary',''), nullif(n.payload->>'text',''), '') AS label
  FROM exploration_nodes n
  WHERE n.kind = 'fact' AND n.state = 'confirmed'
) s
WHERE e.evidence_ref LIKE 'fact:%'
  AND e.system_label = ''
  AND s.id = substring(e.evidence_ref from 'fact:([0-9]+)')::bigint
  AND s.label <> ''`
	if _, err := conn.ExecContext(ctx, factSQL); err != nil {
		return fmt.Errorf("rewrite fact edge labels: %w", err)
	}
	// 3) 无证据的系统边（孤儿降级）：空 label 补兜底文案并标记。
	if _, err := conn.ExecContext(ctx, `UPDATE task_topology_edges
SET label = $1, system_label = 'fallback'
WHERE evidence_ref = '' AND system_label = '' AND label = ''`, TopologyFallbackLabel); err != nil {
		return fmt.Errorf("fill fallback edge labels: %w", err)
	}
	return nil
}

func migrateTaskTopologyOverrides(ctx context.Context, conn *sql.Conn) error {
	// 颜色列修复必须先于 legacy 早退：legacy 表已删除的存量库同样可能缺列。
	if err := ensureTaskTopologyColorColumns(ctx, conn); err != nil {
		return err
	}
	// 节点描述列同理：无论 legacy 是否存在都要保证。
	if err := ensureTaskTopologyDescriptionColumn(ctx, conn); err != nil {
		return err
	}
	// 两线型收敛（solid/reachable）：存量库的 lateral/pending 行并入 reachable，
	// 并把 kind CHECK 约束换成新枚举。
	if err := ensureTaskTopologyTwoKinds(ctx, conn); err != nil {
		return err
	}
	// 系统边 label 统一为「如何发现的」格式（system_label 标记幂等）。
	if err := rewriteTopologySystemEdgeLabels(ctx, conn); err != nil {
		return err
	}
	var legacy bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('task_topology_overrides') IS NOT NULL`).Scan(&legacy); err != nil {
		return fmt.Errorf("probe task_topology_overrides: %w", err)
	}
	if !legacy {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// A database that created the legacy table before relation_label/display_label
	// existed is still migrated: those columns used to be added by schema.sql and
	// left with the table definition, so restore them before the copy below reads
	// them (missing columns would otherwise abort startup).
	for _, ddl := range []string{
		`ALTER TABLE task_topology_overrides ADD COLUMN IF NOT EXISTS relation_label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_topology_overrides ADD COLUMN IF NOT EXISTS display_label TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("prepare legacy topology columns: %w", err)
		}
	}
	const (
		nodeSQL = `INSERT INTO task_topology_nodes(task_id,ip,display_label,entry_status,updated_by)
SELECT o.task_id,o.node_ip,o.display_label,o.entry_status,'ARTEX'
FROM task_topology_overrides o WHERE o.node_ip <> ''
ON CONFLICT(task_id,ip) DO NOTHING`
		// Hosts that only ever appeared as a parent still need a node row.
		parentSQL = `INSERT INTO task_topology_nodes(task_id,ip,updated_by)
SELECT DISTINCT o.task_id,o.parent_ip,'ARTEX'
FROM task_topology_overrides o WHERE o.parent_ip <> '' AND o.parent_ip <> o.node_ip
ON CONFLICT(task_id,ip) DO NOTHING`
		edgeSQL = `INSERT INTO task_topology_edges(task_id,src_ip,dst_ip,label,kind,evidence_ref,updated_by)
SELECT o.task_id,o.parent_ip,o.node_ip,
       CASE WHEN o.relation_label <> '' THEN o.relation_label
            WHEN o.entry_status <> '' THEN ''
            ELSE $1 END,
       CASE WHEN o.entry_status <> '' THEN 'solid' ELSE 'reachable' END,
       o.evidence_ref,'ARTEX'
FROM task_topology_overrides o
WHERE o.node_ip <> '' AND o.parent_ip <> '' AND o.parent_ip <> o.node_ip
ON CONFLICT(task_id,src_ip,dst_ip,kind) DO NOTHING`
	)
	for _, step := range []struct {
		query string
		args  []any
	}{{nodeSQL, nil}, {parentSQL, nil}, {edgeSQL, []any{TopologyFallbackLabel}}} {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return fmt.Errorf("migrate task_topology_overrides: %w", err)
		}
	}
	// Read the task ids before reusing the transaction: one connection cannot
	// keep rows open while running repair queries.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT task_id FROM task_topology_overrides ORDER BY task_id`)
	if err != nil {
		return fmt.Errorf("list migrated topology tasks: %w", err)
	}
	taskIDs := []int64{}
	for rows.Next() {
		var taskID int64
		if err := rows.Scan(&taskID); err != nil {
			rows.Close()
			return fmt.Errorf("list migrated topology tasks: %w", err)
		}
		taskIDs = append(taskIDs, taskID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("list migrated topology tasks: %w", err)
	}
	rows.Close()
	for _, taskID := range taskIDs {
		if err := repairTopologyOrphans(tx, taskID, "ARTEX"); err != nil {
			return fmt.Errorf("repair migrated topology of task %d: %w", taskID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS task_topology_overrides`); err != nil {
		return fmt.Errorf("drop task_topology_overrides: %w", err)
	}
	return tx.Commit()
}

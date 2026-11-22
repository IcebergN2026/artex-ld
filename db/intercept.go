// intercept.go：拦截规则（intercept_rules）与待审批请求（intercept_pending）的 CRUD 与查询。
// 主要函数/类型：
//   - InterceptRule / InterceptPending / InterceptApprovalRow：规则、待审批与审批联查行结构。
//   - ListInterceptRules / CreateInterceptRule / UpdateInterceptRule / DeleteInterceptRule / ToggleInterceptRule：拦截规则的查改增删与启停。
//   - CreateInterceptPending / CreateDecidedIntercept：写入待审批或直接落库的已裁决请求。
//   - DecideInterceptPending / ListPendingIntercepts / GetInterceptPending：审批状态更新与待审批查询。
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InterceptRule is one row of intercept_rules.
type InterceptRule struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Priority       int       `json:"priority"`
	MatchTarget    string    `json:"match_target"`
	MatchType      string    `json:"match_type"`
	Pattern        string    `json:"pattern"`
	Action         string    `json:"action"`
	Message        string    `json:"message"`
	TimeoutEnabled bool      `json:"timeout_enabled"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	TimeoutAction  string    `json:"timeout_action"`
	Tags           []string  `json:"tags"` // 规则标签(分组筛选用,可空)
	// Agents 限定规则适用的 agent:含 "all" 表示全部 agent;否则仅当
	// context 中的 agentName(WithTaskContext 绑定)命中列表才生效。
	Agents         []string  `json:"agents"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// InterceptPending is one row of intercept_pending.
type InterceptPending struct {
	ID             int64           `json:"id"`
	RuleID         *int64          `json:"rule_id"`
	ConversationID *int64          `json:"conversation_id"`
	TaskID         *string         `json:"task_id"`
	AgentName      string          `json:"agent_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Status         string          `json:"status"`
	DecisionSource string          `json:"decision_source"`
	Reason         string          `json:"reason"` // 规则 message 或模型判定理由(前缀 [模型])
	DecidedAt      *time.Time      `json:"decided_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

const interceptRuleCols = `id, name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, tags, agents, created_at, updated_at`

func scanInterceptRule(row interface{ Scan(...any) error }) (InterceptRule, error) {
	var r InterceptRule
	var tags, agents []byte
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority,
		&r.MatchTarget, &r.MatchType, &r.Pattern, &r.Action, &r.Message,
		&r.TimeoutEnabled, &r.TimeoutSeconds, &r.TimeoutAction,
		&tags, &agents, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	// 列是 NOT NULL DEFAULT,容错:JSON 异常按空数组处理,不让单行脏数据打爆整表查询。
	_ = json.Unmarshal(tags, &r.Tags)
	if r.Tags == nil {
		r.Tags = []string{}
	}
	_ = json.Unmarshal(agents, &r.Agents)
	if len(r.Agents) == 0 {
		r.Agents = []string{"all"}
	}
	return r, nil
}

// ListInterceptRules returns all rules ordered by priority DESC then id.
func (d *DB) ListInterceptRules() ([]InterceptRule, error) {
	rows, err := d.Query(`SELECT ` + interceptRuleCols + ` FROM intercept_rules ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptRule
	for rows.Next() {
		r, err := scanInterceptRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateInterceptRule inserts a new rule.
func (d *DB) CreateInterceptRule(name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string, tags, agents []string) (InterceptRule, error) {
	tagsJSON, agentsJSON := marshalJSONStringArray(tags), marshalJSONStringArray(agents)
	row := d.QueryRow(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, tags, agents)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb)
RETURNING `+interceptRuleCols,
		name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction, tagsJSON, agentsJSON)
	return scanInterceptRule(row)
}

// UpdateInterceptRule replaces all editable fields of an existing rule.
func (d *DB) UpdateInterceptRule(id int64, name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string, tags, agents []string) (InterceptRule, error) {
	tagsJSON, agentsJSON := marshalJSONStringArray(tags), marshalJSONStringArray(agents)
	row := d.QueryRow(`
UPDATE intercept_rules
   SET name=$2, enabled=$3, priority=$4, match_target=$5,
       match_type=$6, pattern=$7, action=$8, message=$9,
       timeout_enabled=$10, timeout_seconds=$11, timeout_action=$12,
       tags=$13::jsonb, agents=$14::jsonb
WHERE id=$1
RETURNING `+interceptRuleCols,
		id, name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction, tagsJSON, agentsJSON)
	return scanInterceptRule(row)
}

// marshalJSONStringArray 把字符串数组序列化为 JSONB 入参;nil 归一为 []。
func marshalJSONStringArray(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// RemoveInterceptTagFromRules 从所有规则的 tags 数组中移除指定标签(删除标签时调用),
// 返回受影响的规则数。
func (d *DB) RemoveInterceptTagFromRules(tag string) (int64, error) {
	res, err := d.Exec(`UPDATE intercept_rules SET tags = tags - $1 WHERE tags ? $1`, tag)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RenameInterceptTagInRules 把所有规则 tags 数组中的 oldTag 重命名为 newTag,
// 返回受影响规则数。jsonb 数组元素替换经 jsonb_array_elements_text 重建。
func (d *DB) RenameInterceptTagInRules(oldTag, newTag string) (int64, error) {
	res, err := d.Exec(`
UPDATE intercept_rules
   SET tags = (SELECT jsonb_agg(CASE WHEN t = $1 THEN $2 ELSE t END)
                 FROM jsonb_array_elements_text(tags) AS t)
 WHERE tags ? $1`, oldTag, newTag)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetInterceptAgentsForTag 把带指定标签的所有规则的 agents 批量覆盖为给定值,
// 返回受影响规则数(标签批量操作用)。
func (d *DB) SetInterceptAgentsForTag(tag string, agents []string) (int64, error) {
	res, err := d.Exec(`UPDATE intercept_rules SET agents=$2::jsonb WHERE tags ? $1`,
		tag, marshalJSONStringArray(agents))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TagUntaggedInterceptRules 给所有未分类(tags 为空)规则追加指定标签,
// 返回受影响规则数(「未分类规则批量设置标签」用)。
func (d *DB) TagUntaggedInterceptRules(tag string) (int64, error) {
	res, err := d.Exec(`UPDATE intercept_rules SET tags = tags || to_jsonb($1::text) WHERE tags = '[]'::jsonb`, tag)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BatchUpdateInterceptRuleTags 批量给指定规则设置标签(多选批量打标签用):
// appendMode=true 时与现有 tags 去重合并,否则整体替换。ids 为空直接返回 0。
// 返回受影响规则数。
func (d *DB) BatchUpdateInterceptRuleTags(ids []int64, tags []string, appendMode bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tagsJSON := marshalJSONStringArray(tags)
	var (
		res sql.Result
		err error
	)
	if appendMode {
		// tags || 新标签数组后展开去重重建,合并结果按字典序输出。
		res, err = d.Exec(`UPDATE intercept_rules
   SET tags = (SELECT COALESCE(jsonb_agg(DISTINCT value ORDER BY value),'[]'::jsonb)
                 FROM jsonb_array_elements_text(tags || $2::jsonb) AS t(value))
 WHERE id = ANY($1)`, ids, tagsJSON)
	} else {
		res, err = d.Exec(`UPDATE intercept_rules SET tags = $2::jsonb WHERE id = ANY($1)`, ids, tagsJSON)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteInterceptRule removes a rule.
func (d *DB) DeleteInterceptRule(id int64) error {
	_, err := d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, id)
	return err
}

// ToggleInterceptRule flips the enabled state of a rule.
func (d *DB) ToggleInterceptRule(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE intercept_rules SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// CreateInterceptPending inserts a pending approval record and returns its ID.
// convID == 0 → conversation_id stored as NULL (background task).
// taskID == "" → task_id stored as NULL.
func (d *DB) CreateInterceptPending(ruleID, convID int64, taskID, agentName, toolName string, input []byte, reason string, audits ...*InterceptAudit) (int64, error) {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var convIDPtr *int64
	if convID != 0 {
		convIDPtr = &convID
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	// ruleID == 0 → NULL: the LLM fallback judge has no owning rule.
	var ruleIDPtr *int64
	if ruleID != 0 {
		ruleIDPtr = &ruleID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, reason, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		ruleIDPtr, convIDPtr, taskIDPtr, agentName, toolName, raw, reason, interceptSource(ruleID, reason), firstAudit(audits)).Scan(&id)
	return id, err
}

// DecideInterceptPending updates a pending record's status (allowed/denied/timeout).
func (d *DB) DecideInterceptPending(id int64, status string) error {
	_, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW() WHERE id=$1`, id, status)
	return err
}

// CreateDecidedIntercept inserts an intercept_pending row ALREADY in a final state
// (status = 'allowed' | 'denied'), decided_at stamped now. Used to log allow/deny
// rule matches for observability — they don't block and need no user action, so unlike
// CreateInterceptPending (which starts 'pending') this records the outcome directly.
func (d *DB) CreateDecidedIntercept(ruleID, convID int64, taskID, agentName, toolName string, input []byte, status, reason string, audits ...*InterceptAudit) (int64, error) {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var convIDPtr *int64
	if convID != 0 {
		convIDPtr = &convID
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	// ruleID == 0 → NULL: the LLM fallback judge has no owning rule.
	var ruleIDPtr *int64
	if ruleID != 0 {
		ruleIDPtr = &ruleID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), $9, $10) RETURNING id`,
		ruleIDPtr, convIDPtr, taskIDPtr, agentName, toolName, raw, status, reason, interceptSource(ruleID, reason), firstAudit(audits)).Scan(&id)
	return id, err
}

const interceptPendingCols = `id, rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, created_at, decision_source`

func scanInterceptPending(s interface{ Scan(...any) error }, p *InterceptPending) error {
	return s.Scan(&p.ID, &p.RuleID, &p.ConversationID, &p.TaskID, &p.AgentName,
		&p.ToolName, &p.ToolInput, &p.Status, &p.Reason, &p.DecidedAt, &p.CreatedAt, &p.DecisionSource)
}

// ListPendingIntercepts returns all unresolved approval requests, newest first.
func (d *DB) ListPendingIntercepts() ([]InterceptPending, error) {
	rows, err := d.Query(`SELECT ` + interceptPendingCols + ` FROM intercept_pending WHERE status='pending' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptPending
	for rows.Next() {
		var p InterceptPending
		if err := scanInterceptPending(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetInterceptPending returns one pending record (nil if absent).
func (d *DB) GetInterceptPending(id int64) (*InterceptPending, error) {
	var p InterceptPending
	err := scanInterceptPending(
		d.QueryRow(`SELECT `+interceptPendingCols+` FROM intercept_pending WHERE id=$1`, id),
		&p,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// InterceptApprovalRow is intercept_pending enriched with conversation and rule info.
type InterceptApprovalRow struct {
	InterceptPending
	ConvTitle    string `json:"conv_title"`
	ConvAgentKey string `json:"conv_agent_key"`
	RuleName     string `json:"rule_name"`
}

func scanInterceptApprovalRow(rows interface{ Scan(...any) error }, r *InterceptApprovalRow) error {
	return rows.Scan(
		&r.ID, &r.RuleID, &r.ConversationID, &r.TaskID, &r.AgentName,
		&r.ToolName, &r.ToolInput, &r.Status, &r.Reason, &r.DecidedAt, &r.CreatedAt,
		&r.DecisionSource, &r.ConvTitle, &r.ConvAgentKey, &r.RuleName,
	)
}

// Keep legacy rows without decision_source consistent with their displayed source.
const approvalDecisionSource = `COALESCE(NULLIF(ip.decision_source,''), CASE
 WHEN ip.rule_id IS NOT NULL THEN 'rule'
 WHEN ip.reason LIKE '[模型]%' THEN 'model' ELSE 'unknown' END)`

const approvalRowColumns = `ip.id, ip.rule_id, ip.conversation_id, ip.task_id, ip.agent_name,
       ip.tool_name, ip.tool_input, ip.status, ip.reason, ip.decided_at, ip.created_at, ` + approvalDecisionSource + `,
       COALESCE(c.title,'') AS conv_title,
       COALESCE(c.agent_key,'') AS conv_agent_key,
       COALESCE(ir.name,'') AS rule_name`

const approvalRowJoins = `
FROM intercept_pending ip
LEFT JOIN conversations c ON c.id = ip.conversation_id
LEFT JOIN intercept_rules ir ON ir.id = ip.rule_id`

const approvalRowSelect = `SELECT ` + approvalRowColumns + approvalRowJoins
const approvalRowSelectWithAudit = `SELECT ` + approvalRowColumns + `, ip.audit` + approvalRowJoins

func interceptSource(ruleID int64, reason string) string {
	if ruleID != 0 {
		return "rule"
	}
	if strings.HasPrefix(reason, "[模型]") {
		return "model"
	}
	return "unknown"
}

func firstAudit(audits []*InterceptAudit) any {
	if len(audits) == 0 || audits[0] == nil {
		return nil
	}
	raw, err := json.Marshal(audits[0])
	if err != nil {
		return nil
	}
	return raw
}

// ListAllIntercepts returns up to limit intercept_pending rows (newest first)
// joined with conversation and rule info.
func (d *DB) ListAllIntercepts(limit int) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` ORDER BY ip.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InterceptApprovalFilter combines exact status and decision-source filters.
// Empty fields include all values.
type InterceptApprovalFilter struct {
	Status         string
	DecisionSource string
}

// ListAllInterceptsPage returns one 1-based page and the total matching count.
func (d *DB) ListAllInterceptsPage(page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage("", page, size, filter)
}

// ListTaskIntercepts returns all intercept_pending rows for a specific task (newest first).
func (d *DB) ListTaskIntercepts(taskID string) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` WHERE ip.task_id=$1 ORDER BY ip.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTaskInterceptsPage is the paginated variant of ListTaskIntercepts.
func (d *DB) ListTaskInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage(taskID, page, size, filter)
}

func (d *DB) listInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size

	conditions := []string{}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			args = append(args, value)
			conditions = append(conditions, column+"=$"+fmt.Sprint(len(args)))
		}
	}
	add("ip.task_id", taskID)
	add("ip.status", filter.Status)
	add(approvalDecisionSource, filter.DecisionSource)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM intercept_pending ip"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := len(args) + 1
	offsetArg := limitArg + 1
	dataQ := approvalRowSelect + where +
		" ORDER BY ip.created_at DESC, ip.id DESC LIMIT $" + fmt.Sprint(limitArg) +
		" OFFSET $" + fmt.Sprint(offsetArg)
	args = append(args, size, offset)
	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []InterceptApprovalRow{}
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

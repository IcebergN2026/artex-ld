// config.go：LLM 接入配置（profiles 表）与 agent 定义（agents 表）的持久化。
// 主要函数/类型：
//   - LLMProfile：LLM 接入配置行（含限流、重试、token 等参数）。
//   - ListProfiles / ActiveProfile / ProfileByID / PoolProfiles：查询配置列表、默认项或池中配置。
//   - SaveProfile / DeleteProfile / SetActiveProfile：保存（新增/更新）、删除及设为默认配置。
//   - Agent：agent 定义结构；ListAgents / GetAgentByKey / GetAgentByExecutorKey：查询 agent。
//   - CreateAgent / UpdateAgentMeta / DeleteAgent / SetAgentExecutorKey：agent 的增删改。

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp" // 【二开·P-EXEC·D10】executor_key 格式校验
	"time"
)

// ---------- LLM profiles ----------

type LLMProfile struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Format        string  `json:"format"`
	BaseURL       string  `json:"base_url,omitempty"`
	Proxy         string  `json:"proxy,omitempty"` // LLM 出站代理(http/https/socks5);空=用环境变量
	Model         string  `json:"model"`
	APIKey        string  `json:"-"` // never serialized to UI
	APIKeyHint    string  `json:"api_key_hint,omitempty"`
	RatePerSecond float64 `json:"rate_per_second"`
	RatePerMinute float64 `json:"rate_per_minute"`
	// ContextWindowK is the model's context window in K tokens, used to size
	// compaction thresholds. 0 = use a 200K default; capped at 1000 (1M).
	ContextWindowK int `json:"context_window_k"`
	// ThinkingType 独立控制思考「开关」(thinking.type):"" = 不发送(默认);
	// "disabled" = 显式关闭; "enabled" = 开启. 与 ReasoningEffort 解耦.
	ThinkingType string `json:"thinking_type"`
	// ReasoningEffort 独立控制思考「强度」:"" = 不发送(默认);
	// "low"/"medium"/"high"/"xhigh"/"max" = 对应强度. 见 agent.Config.NewProvider.
	ReasoningEffort string `json:"reasoning_effort"`
	IsDefault       bool   `json:"is_default"`
	// Priority orders the failover chain: higher goes first. The ACTIVE profile
	// (IsDefault) always heads the chain regardless of this value.
	Priority int `json:"priority"`
	// PoolExclude=true keeps this profile out of the failover chain — it stays
	// usable when an agent/task binds it explicitly, it just never gets picked up
	// as a fallback target.
	PoolExclude bool `json:"pool_exclude"`
	// Streaming selects the wire protocol: true (default) = streaming (SSE);
	// false = real non-streaming (stream:false, single JSON response via
	// Provider.Complete). Non-streaming sidesteps flaky gateway SSE at the cost of
	// live in-run progress. Maps to agent.Config.Stream.
	Streaming bool `json:"streaming"`
	// MaxTokens caps a single reply's output in tokens. 0 = send no cap and let
	// the endpoint's own default apply (the historical behaviour). Unlike
	// ContextWindowK — the model's total capacity, used locally to size compaction
	// — this value travels with every request.
	MaxTokens int `json:"max_tokens"`
	// MaxTokensField picks the request key carrying MaxTokens, for format
	// "openai" only: "" = max_tokens (default); "max_completion_tokens" = the
	// newer key, which OpenAI's reasoning models require and whose budget covers
	// reasoning tokens plus visible output. Ignored by anthropic and
	// openai-responses, which name the field themselves.
	MaxTokensField string `json:"max_tokens_field"`
	// SessionHeaderKey, when non-empty, names a custom HTTP header sent on every
	// request built from this profile; its value is the current run's session id
	// (chat conversation / worker intent). For gateways that key prompt caching
	// or sticky routing off a session-id header. "" = not sent. Maps to
	// agent.Config.SessionHeaderKey.
	SessionHeaderKey string `json:"session_header_key"`
	// Retry overrides this profile's share of the retry ladder. Zero value =
	// inherit the global policy (LLMRetryPolicy), so an untouched profile behaves
	// exactly as before. See RetryOverride.
	Retry RetryOverride `json:"retry"`
}

// RetryOverride is one profile's optional override of the three retry layers
// that are per-endpoint: 建连(connect) / 空响应(empty) / 同 provider 安全窗口
// (stream). Each rule's zero value means "inherit the global policy"; see
// RetryRule for the -1 / 0 / >0 semantics.
type RetryOverride struct {
	Connect RetryRule `json:"connect"`
	Empty   RetryRule `json:"empty"`
	Stream  RetryRule `json:"stream"`
}

const profileRetryCols = `COALESCE(retry_connect_attempts,0),COALESCE(retry_connect_interval_ms,0),COALESCE(retry_empty_attempts,0),COALESCE(retry_empty_interval_ms,0),COALESCE(retry_stream_attempts,0),COALESCE(retry_stream_interval_ms,0)`

// profileCols is the read column list (hint variant, no api key) shared by the
// list query; profileColsKey is the same with api_key for the single-row loads.
const profileCols = `id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,COALESCE(api_key_hint,''),rate_per_second,rate_per_minute,context_window_k,COALESCE(reasoning_effort,''),is_default,priority,pool_exclude,COALESCE(thinking_type,''),COALESCE(streaming,true),COALESCE(max_tokens,0),COALESCE(max_tokens_field,''),COALESCE(session_header_key,''),` + profileRetryCols
const profileColsKey = `id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,COALESCE(api_key,''),rate_per_second,rate_per_minute,context_window_k,COALESCE(reasoning_effort,''),is_default,priority,pool_exclude,COALESCE(thinking_type,''),COALESCE(streaming,true),COALESCE(max_tokens,0),COALESCE(max_tokens_field,''),COALESCE(session_header_key,''),` + profileRetryCols

// scanProfile reads one row in the profileCols / profileColsKey column order. The
// api_key_hint (profileCols) or api_key (profileColsKey) column lands in APIKeyHint
// or APIKey depending on which list the caller used.
func scanProfile(sc interface{ Scan(...any) error }, into *string, p *LLMProfile) error {
	return sc.Scan(&p.ID, &p.Name, &p.Format, &p.BaseURL, &p.Proxy, &p.Model, into,
		&p.RatePerSecond, &p.RatePerMinute, &p.ContextWindowK, &p.ReasoningEffort, &p.IsDefault, &p.Priority, &p.PoolExclude, &p.ThinkingType, &p.Streaming,
		&p.MaxTokens, &p.MaxTokensField, &p.SessionHeaderKey,
		&p.Retry.Connect.Attempts, &p.Retry.Connect.IntervalMS,
		&p.Retry.Empty.Attempts, &p.Retry.Empty.IntervalMS,
		&p.Retry.Stream.Attempts, &p.Retry.Stream.IntervalMS)
}

func (d *DB) ListProfiles() ([]*LLMProfile, error) {
	rows, err := d.Query(`SELECT ` + profileCols + ` FROM llm_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProfile
	for rows.Next() {
		var p LLMProfile
		if err := scanProfile(rows, &p.APIKeyHint, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ActiveProfile returns the default (active) profile with its api key, or nil.
func (d *DB) ActiveProfile() (*LLMProfile, error) {
	var p LLMProfile
	err := scanProfile(d.QueryRow(`SELECT `+profileColsKey+` FROM llm_profiles WHERE is_default LIMIT 1`), &p.APIKey, &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// ProfileByID returns one profile with its api key by id, or nil if not found.
// Used to run a task on a specific (non-default) LLM profile.
func (d *DB) ProfileByID(id int64) (*LLMProfile, error) {
	var p LLMProfile
	err := scanProfile(d.QueryRow(`SELECT `+profileColsKey+` FROM llm_profiles WHERE id=$1`, id), &p.APIKey, &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// PoolProfiles returns the failover chain in run order, api keys included: the
// active profile first, then every other keyed profile that isn't excluded, by
// priority DESC (id ASC to stay stable). Profiles without an api key can't serve
// a request, so they never enter the chain. The ordering IS the policy — callers
// walk the slice front to back.
func (d *DB) PoolProfiles() ([]*LLMProfile, error) {
	rows, err := d.Query(`SELECT ` + profileColsKey + ` FROM llm_profiles
WHERE COALESCE(api_key,'') <> '' AND (is_default OR NOT pool_exclude)
ORDER BY is_default DESC, priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProfile
	for rows.Next() {
		var p LLMProfile
		if err := scanProfile(rows, &p.APIKey, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// SaveProfile inserts (id==0) or updates a profile. Empty apiKey on update keeps existing.
func (d *DB) SaveProfile(p *LLMProfile) (int64, error) {
	hint := p.APIKeyHint
	if len(p.APIKey) >= 4 {
		hint = "…" + p.APIKey[len(p.APIKey)-4:]
	}
	r := p.Retry.Clamped()
	if p.ID == 0 {
		var id int64
		err := d.QueryRow(`INSERT INTO llm_profiles(name,format,base_url,proxy,model,api_key,api_key_hint,rate_per_second,rate_per_minute,context_window_k,reasoning_effort,priority,pool_exclude,thinking_type,streaming,max_tokens,max_tokens_field,session_header_key,retry_connect_attempts,retry_connect_interval_ms,retry_empty_attempts,retry_empty_interval_ms,retry_stream_attempts,retry_stream_interval_ms)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24) RETURNING id`,
			p.Name, p.Format, p.BaseURL, p.Proxy, p.Model, p.APIKey, hint, p.RatePerSecond, p.RatePerMinute, p.ContextWindowK, p.ReasoningEffort, p.Priority, p.PoolExclude, p.ThinkingType, p.Streaming, p.MaxTokens, p.MaxTokensField, p.SessionHeaderKey,
			r.Connect.Attempts, r.Connect.IntervalMS, r.Empty.Attempts, r.Empty.IntervalMS, r.Stream.Attempts, r.Stream.IntervalMS).Scan(&id)
		return id, err
	}
	if p.APIKey == "" {
		_, err := d.Exec(`UPDATE llm_profiles SET name=$1,format=$2,base_url=NULLIF($3,''),proxy=NULLIF($4,''),model=$5,rate_per_second=$6,rate_per_minute=$7,context_window_k=$8,reasoning_effort=$9,priority=$10,pool_exclude=$11,thinking_type=$12,streaming=$13,max_tokens=$14,max_tokens_field=$15,session_header_key=$16,retry_connect_attempts=$17,retry_connect_interval_ms=$18,retry_empty_attempts=$19,retry_empty_interval_ms=$20,retry_stream_attempts=$21,retry_stream_interval_ms=$22 WHERE id=$23`,
			p.Name, p.Format, p.BaseURL, p.Proxy, p.Model, p.RatePerSecond, p.RatePerMinute, p.ContextWindowK, p.ReasoningEffort, p.Priority, p.PoolExclude, p.ThinkingType, p.Streaming, p.MaxTokens, p.MaxTokensField, p.SessionHeaderKey,
			r.Connect.Attempts, r.Connect.IntervalMS, r.Empty.Attempts, r.Empty.IntervalMS, r.Stream.Attempts, r.Stream.IntervalMS, p.ID)
		return p.ID, err
	}
	_, err := d.Exec(`UPDATE llm_profiles SET name=$1,format=$2,base_url=NULLIF($3,''),proxy=NULLIF($4,''),model=$5,api_key=$6,api_key_hint=$7,rate_per_second=$8,rate_per_minute=$9,context_window_k=$10,reasoning_effort=$11,priority=$12,pool_exclude=$13,thinking_type=$14,streaming=$15,max_tokens=$16,max_tokens_field=$17,session_header_key=$18,retry_connect_attempts=$19,retry_connect_interval_ms=$20,retry_empty_attempts=$21,retry_empty_interval_ms=$22,retry_stream_attempts=$23,retry_stream_interval_ms=$24 WHERE id=$25`,
		p.Name, p.Format, p.BaseURL, p.Proxy, p.Model, p.APIKey, hint, p.RatePerSecond, p.RatePerMinute, p.ContextWindowK, p.ReasoningEffort, p.Priority, p.PoolExclude, p.ThinkingType, p.Streaming, p.MaxTokens, p.MaxTokensField, p.SessionHeaderKey,
		r.Connect.Attempts, r.Connect.IntervalMS, r.Empty.Attempts, r.Empty.IntervalMS, r.Stream.Attempts, r.Stream.IntervalMS, p.ID)
	return p.ID, err
}

var (
	ErrActiveLLMProfileDelete      = errors.New("cannot delete the active LLM profile; activate another profile first")
	ErrLLMProfileReferencesChanged = errors.New("LLM profile references changed while deleting; retry the request")
	ErrLLMProfileNotFound          = errors.New("LLM profile not found")
)

func (d *DB) DeleteProfile(id int64) error {
	return d.DeleteProfileContext(context.Background(), id)
}

// DeleteProfileContext removes a non-default profile while preserving the task
// failover cursor. Reference changes are retried because a task, agent, or
// conversation may start pointing at the profile between the initial scan and
// the profile row lock. A bound prevents a continuously changing workload from
// keeping an HTTP request alive forever.
func (d *DB) DeleteProfileContext(ctx context.Context, id int64) error {
	const (
		maxAttempts = 8
		maxDuration = 15 * time.Second
	)
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		retry, err := d.deleteProfile(ctx, id)
		if err != nil || !retry {
			return err
		}
	}
	return fmt.Errorf("%w: profile %d", ErrLLMProfileReferencesChanged, id)
}

func (d *DB) deleteProfile(ctx context.Context, id int64) (bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Reference setters lock their task/agent/conversation row before an
	// llm_profiles row can be locked by a foreign-key check. Keep deletion in the
	// same order: the old profile -> child order deadlocked with a setter's child
	// -> profile order. Sorting also gives concurrent profile deletions a stable
	// order when their chains overlap multiple tasks.
	lockedTasks, err := lockProfileReferenceRows(ctx, tx, `SELECT t.id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
ORDER BY t.id
FOR UPDATE OF t`, id)
	if err != nil {
		return false, err
	}
	lockedAgents, err := lockProfileReferenceRows(ctx, tx, `SELECT id FROM agents
WHERE llm_profile_id=$1
ORDER BY id
FOR UPDATE`, id)
	if err != nil {
		return false, err
	}
	lockedConversations, err := lockProfileReferenceRows(ctx, tx, `SELECT id FROM conversations
WHERE llm_profile_id=$1
ORDER BY id
FOR UPDATE`, id)
	if err != nil {
		return false, err
	}

	var isDefault bool
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM llm_profiles WHERE id=$1 FOR UPDATE`, id).Scan(&isDefault); err != nil {
		if err == sql.ErrNoRows {
			return false, ErrLLMProfileNotFound
		}
		return false, err
	}
	if isDefault {
		return false, ErrActiveLLMProfileDelete
	}

	type affectedTask struct {
		id        int64
		position  int
		wasActive bool
	}
	// A task may have committed a new reference after the first statement took
	// its snapshot but before this transaction acquired the profile lock. The
	// profile lock now prevents further references; retry if that committed task
	// was not part of the task-first lock set. Never acquire a new task lock while
	// holding the profile lock, because that would recreate the inversion.
	rows, err := tx.QueryContext(ctx, `SELECT ref_kind, ref_id FROM (
	SELECT 'task'::text AS ref_kind, t.id AS ref_id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
	UNION ALL
	SELECT 'agent', a.id FROM agents a WHERE a.llm_profile_id=$1
	UNION ALL
	SELECT 'conversation', c.id FROM conversations c WHERE c.llm_profile_id=$1
) refs
ORDER BY ref_kind, ref_id`, id)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var (
			kind  string
			rowID int64
		)
		if err := rows.Scan(&kind, &rowID); err != nil {
			rows.Close()
			return false, err
		}
		locked := false
		switch kind {
		case "task":
			_, locked = lockedTasks[rowID]
		case "agent":
			_, locked = lockedAgents[rowID]
		case "conversation":
			_, locked = lockedConversations[rowID]
		}
		if !locked {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT x.task_id, x.position, COALESCE(t.active_llm_profile_id=$1, false)
FROM task_llm_profiles x
JOIN tasks t ON t.id=x.task_id
WHERE x.profile_id=$1
ORDER BY x.task_id`, id)
	if err != nil {
		return false, err
	}
	var affected []affectedTask
	for rows.Next() {
		var task affectedTask
		if err := rows.Scan(&task.id, &task.position, &task.wasActive); err != nil {
			rows.Close()
			return false, err
		}
		affected = append(affected, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_profiles WHERE id=$1`, id); err != nil {
		return false, err
	}
	// Only a successor after the deleted cursor is eligible. If there is none,
	// clear the explicit chain so the task falls back to its Agent/global provider;
	// profiles before a manually selected cursor must never be revived.
	for _, task := range affected {
		if !task.wasActive {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, task.id); err != nil {
				return false, err
			}
			continue
		}
		var next int64
		err := tx.QueryRowContext(ctx, `SELECT profile_id FROM task_llm_profiles
WHERE task_id=$1 AND position>$2 AND status='ready'
ORDER BY position
LIMIT 1`, task.id, task.position).Scan(&next)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		if err == sql.ErrNoRows {
			if _, err := tx.ExecContext(ctx, `DELETE FROM task_llm_profiles WHERE task_id=$1`, task.id); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks
SET active_llm_profile_id=NULL, llm_profile_id=NULL, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id); err != nil {
				return false, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks
SET active_llm_profile_id=$2, llm_profile_id=$2, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id, next); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func lockProfileReferenceRows(ctx context.Context, tx *sql.Tx, query string, profileID int64) (map[int64]struct{}, error) {
	rows, err := tx.QueryContext(ctx, query, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locked := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		locked[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return locked, nil
}

// SetActiveProfile makes one profile the global default (single-default invariant).
func (d *DB) SetActiveProfile(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE llm_profiles SET is_default=false WHERE is_default`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE llm_profiles SET is_default=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLLMProfileNotFound
	}
	return tx.Commit()
}

// ---------- Agents / prompts ----------

type Agent struct {
	ID               int64  `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	LLMProfileID     *int64 `json:"llm_profile_id"`    // 绑定的 LLM 配置;nil=跟随任务/会话 pin,再回退全局激活
	MaxTurns         int    `json:"max_turns"`         // 单次运行最大轮次;0=不限制
	RunSecs          int    `json:"run_seconds"`       // worker 单次运行墙钟上限(秒);0=不限制
	WebSearch        bool   `json:"web_search"`        // 是否启用网络搜索(受系统全局开关门控)
	InteractiveShell bool   `json:"interactive_shell"` // 是否启用交互式 shell(持久 PTY 会话工具族)
	WrapupPrompt     string `json:"wrapup_prompt"`     // 收尾提示词(超时/步数耗尽时的 settlement 提示);空=用代码内置默认
	WrapupMaxTurns   int    `json:"wrapup_max_turns"`  // 收尾阶段自身的轮数预算;0=用代码内置默认(按 agent)
	// 任务级超时收尾词(与 per-run 两套;仅 worker/planner 用);空/0=用代码内置默认。
	TaskTimeoutWrapupPrompt   string `json:"task_timeout_wrapup_prompt"`
	TaskTimeoutWrapupMaxTurns int    `json:"task_timeout_wrapup_max_turns"`
	// P3 触发后处理策略(仅自定义 agent 有意义):
	// TriggerRunMode  serial|parallel — 串行排队 / 每次触发各自并发一个会话
	// TriggerMergeMode by_task|all|none — 仅 serial 用:同任务合并 / 全部合并 / 不合并
	// TriggerMaxParallel — 仅 parallel 用的每 agent 并发上限;0=不限
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
	// 【二开·P-EXEC·D10】起 —— 执行器标识（空 = 本 agent 行为不变，K23；仅自定义 agent 可声明，内置禁止 K18）
	ExecutorKey string `json:"executor_key,omitempty"`
	// 【二开·P-EXEC·D10】止
	// 【二开·全局 custom intent 门控】本 agent 的所有 custom intent 是否受 Planner 控制：
	// true（默认，兼容旧数据）=保持现有 Planner 控制行为；false=该 Agent 的所有 custom intent
	// 均不受 Planner 控制。运行时读取 Agent 当前配置，修改后立即影响已有 intent。
	PlannerControlled bool `json:"planner_controlled"`
}

// 【二开·P-EXEC·D10】agentCols 尾部加 executor_key；首个 custom seed 门控尾部加 planner_controlled（COALESCE 兜底旧行默认 true）
const agentCols = `id,key,name,COALESCE(description,''),role,builtin,enabled,COALESCE(max_turns,0),COALESCE(run_seconds,600),COALESCE(web_search,false),COALESCE(interactive_shell,false),COALESCE(wrapup_prompt,''),COALESCE(wrapup_max_turns,0),COALESCE(task_timeout_wrapup_prompt,''),COALESCE(task_timeout_wrapup_max_turns,0),COALESCE(trigger_run_mode,'serial'),COALESCE(trigger_merge_mode,'all'),COALESCE(trigger_max_parallel,5),llm_profile_id,COALESCE(executor_key,''),COALESCE(planner_controlled,true)`

func scanAgent(sc interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	var prof sql.NullInt64 // llm_profile_id 可空:未绑定时为 NULL
	// 【二开·P-EXEC·D10】Scan 尾部加 executor_key（agentCols 已 COALESCE('') 兜底空串）
	err := sc.Scan(&a.ID, &a.Key, &a.Name, &a.Description, &a.Role, &a.Builtin, &a.Enabled, &a.MaxTurns, &a.RunSecs, &a.WebSearch, &a.InteractiveShell, &a.WrapupPrompt, &a.WrapupMaxTurns, &a.TaskTimeoutWrapupPrompt, &a.TaskTimeoutWrapupMaxTurns, &a.TriggerRunMode, &a.TriggerMergeMode, &a.TriggerMaxParallel, &prof, &a.ExecutorKey, &a.PlannerControlled)
	if err == nil && prof.Valid {
		v := prof.Int64
		a.LLMProfileID = &v
	}
	return &a, err
}

func (d *DB) ListAgents() ([]*Agent, error) {
	rows, err := d.Query(`SELECT ` + agentCols + ` FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) GetAgentByKey(key string) (*Agent, error) {
	a, err := scanAgent(d.QueryRow(`SELECT `+agentCols+` FROM agents WHERE key=$1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// 【二开·P-EXEC·D10 executor_key 查询】起 —— GetAgentByExecutorKey/CheckExecutorKey/SetAgentExecutorKey 与格式校验

// 功能介绍：GetAgentByExecutorKey——S2 resolveExecutor 的 DB 查询；executor_key 经 agentCols COALESCE('') 空串兼容

// GetAgentByExecutorKey 按 executor_key 找 agent（不存在/空返回 nil,nil）。
func (d *DB) GetAgentByExecutorKey(key string) (*Agent, error) {
	if key == "" {
		return nil, nil
	}
	a, err := scanAgent(d.QueryRow(`SELECT `+agentCols+` FROM agents WHERE executor_key=$1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// CheckExecutorKey 校验一个 executor_key 是否对应当前启用的非内置自定义 Agent。
// 供对话型 Agent 的领取工具复用，避免普通 Worker 或其他 Agent 冒充执行器。
func (d *DB) CheckExecutorKey(key string) error {
	a, err := d.GetAgentByExecutorKey(key)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("执行器标识 %q 未注册", key)
	}
	if !a.Enabled {
		return fmt.Errorf("执行器 %q 所属 agent 已禁用", key)
	}
	if a.Builtin {
		return fmt.Errorf("内置 agent 不能作为自定义执行器（executor_key 只面向自定义 agent）")
	}
	return nil
}

var executorKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// SetAgentExecutorKey 更新/清除 agent 的执行器标识（server_mgmt pgSaveAgentConfig 透传）。
// 校验格式（与 CHECK 约束同款正则，提前报错比 PG 约束错误友好）；
// 内置 agent 禁止设置（K18：executor 只面向自定义 agent）。
func (d *DB) SetAgentExecutorKey(agentKey, executorKey string) error {
	if executorKey != "" && !executorKeyRe.MatchString(executorKey) {
		return fmt.Errorf("executor_key 格式非法（小写蛇形，字母开头）: %q", executorKey)
	}
	res, err := d.Exec(`UPDATE agents SET executor_key=NULLIF($2,'') WHERE key=$1 AND builtin=false`, agentKey, executorKey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agent %q 不存在或是内置 agent（内置 agent 不可设置 executor_key）", agentKey)
	}
	return nil
}

// 【二开·P-EXEC·D10 executor_key 查询】止

// 功能介绍：SetAgentPlannerControlled——自定义 agent 的全局 Planner 门控开关
//          （server_mgmt pgSaveAgentConfig 透传；内置 agent 禁止修改，与 executor_key 同口径）

// SetAgentPlannerControlled 更新 agent 的全局 Planner 门控开关。
// 修改后立即影响该 Agent 的所有现有和后续 custom intent。
func (d *DB) SetAgentPlannerControlled(agentKey string, on bool) error {
	res, err := d.Exec(`UPDATE agents SET planner_controlled=$2 WHERE key=$1 AND builtin=false`, agentKey, on)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agent %q 不存在或是内置 agent（内置 agent 不可修改 planner_controlled）", agentKey)
	}
	return nil
}

// AgentBindingCounts returns per-agent binding counts in a few grouped queries
// (NO N+1): visible MCP servers and skills keyed by agent id, and bound tools
// keyed by agent key (tools.agents is a JSONB array of agent keys). Missing keys
// mean zero. Used to show "MCP N · Skill N · 工具 N" on the agent cards.
func (d *DB) AgentBindingCounts() (mcp map[int64]int, skill map[int64]int, tools map[string]int, err error) {
	mcp, skill, tools = map[int64]int{}, map[int64]int{}, map[string]int{}
	byID := func(q string, into map[int64]int) error {
		rows, e := d.Query(q)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var n int
			if e := rows.Scan(&id, &n); e != nil {
				return e
			}
			into[id] = n
		}
		return rows.Err()
	}
	if err = byID(`SELECT agent_id, count(DISTINCT resource_id) FROM agent_visibility WHERE resource_kind='mcp' AND enabled GROUP BY agent_id`, mcp); err != nil {
		return
	}
	if err = byID(`SELECT agent_id, count(*) FROM agent_skill_visibility WHERE enabled GROUP BY agent_id`, skill); err != nil {
		return
	}
	rows, e := d.Query(`SELECT elem, count(*) FROM tools, jsonb_array_elements_text(agents) AS elem GROUP BY elem`)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if e := rows.Scan(&k, &n); e != nil {
			err = e
			return
		}
		tools[k] = n
	}
	err = rows.Err()
	return
}

// CreateAgent inserts a custom (builtin=false) conversational agent with role
// 'assistant'. Returns the new row. Callers validate key/name upstream; the DB
// enforces key charset + uniqueness and the role check constraint.
func (d *DB) CreateAgent(key, name, description string) (*Agent, error) {
	a, err := scanAgent(d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled)
VALUES ($1, $2, NULLIF($3,''), 'assistant', false, true)
RETURNING `+agentCols, key, name, description))
	if err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateAgentMeta updates a custom agent's display name + description. Built-in
// agents are left untouched (guarded by the caller / the builtin flag).
func (d *DB) UpdateAgentMeta(key, name, description string) error {
	_, err := d.Exec(`UPDATE agents SET name=$2, description=NULLIF($3,'') WHERE key=$1 AND builtin=false`, key, name, description)
	return err
}

// DeleteAgent removes a custom agent. Built-in agents are protected by the
// builtin=false guard. agent_prompts / agent_prompt_vars / visibility rows cascade
// via FK; tools.agents bindings for the key are cleaned by the caller.
func (d *DB) DeleteAgent(key string) error {
	_, err := d.Exec(`DELETE FROM agents WHERE key=$1 AND builtin=false`, key)
	return err
}

// SetAgentMaxTurns updates an agent's max_turns (0 = unlimited).
func (d *DB) SetAgentMaxTurns(key string, maxTurns int) error {
	if maxTurns < 0 {
		maxTurns = 0
	}
	_, err := d.Exec(`UPDATE agents SET max_turns=$1 WHERE key=$2`, maxTurns, key)
	return err
}

// SetAgentLLMProfile binds an agent to a specific LLM profile (id != nil), or clears
// the binding (id == nil) so the agent follows the task/conversation pin, else the
// global active profile. Precedence at runtime: agent binding → task/conv pin → active.
func (d *DB) SetAgentLLMProfile(key string, id *int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// DeleteProfile locks reference rows before the profile row. Keep the same
	// order here so a concurrent rebind cannot form a child/profile deadlock.
	var agentID int64
	if err := tx.QueryRow(`SELECT id FROM agents WHERE key=$1 FOR UPDATE`, key).Scan(&agentID); err != nil {
		if err == sql.ErrNoRows {
			// Preserve the previous UPDATE semantics: an unknown key is a no-op.
			return tx.Commit()
		}
		return err
	}
	if err := lockLLMProfileForReference(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE id=$2`, id, agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// lockLLMProfileForReference makes the profile side of the shared lock-order
// protocol explicit. Callers must already own the referencing child row.
func lockLLMProfileForReference(tx *sql.Tx, profileID *int64) error {
	if profileID == nil {
		return nil
	}
	var lockedID int64
	if err := tx.QueryRow(`SELECT id FROM llm_profiles WHERE id=$1 FOR KEY SHARE`, *profileID).Scan(&lockedID); err != nil {
		if err == sql.ErrNoRows {
			return ErrLLMProfileNotFound
		}
		return err
	}
	return nil
}

// SetAgentWebSearch toggles whether an agent uses network search (still gated by
// the global web-search master switch + backend/key config).
func (d *DB) SetAgentWebSearch(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET web_search=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentInteractiveShell toggles whether an agent gets the interactive shell
// (持久 PTY 会话) tool family + Bash 提示词联动(见 docs/交互式shell设计.md §14.2).
func (d *DB) SetAgentInteractiveShell(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET interactive_shell=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentWrapupPrompt stores an agent's wrap-up (settlement) prompt. Empty string
// means "use the code built-in default" — resolved at runtime by resolveWrapup.
func (d *DB) SetAgentWrapupPrompt(key, prompt string) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_prompt=$1 WHERE key=$2`, prompt, key)
	return err
}

// SetAgentWrapupMaxTurns stores the wrap-up phase's own turn budget. 0 means "use
// the code built-in default" — resolved at runtime by resolveWrapupTurns.
func (d *DB) SetAgentWrapupMaxTurns(key string, n int) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_max_turns=$1 WHERE key=$2`, n, key)
	return err
}

// SetAgentTaskTimeoutWrapup stores an agent's task-timeout wrap-up prompt (empty =
// use code built-in default; only worker/planner have one) and its turn budget
// (0 = default). Resolved at runtime by resolveTaskTimeoutWrapup / …Turns.
func (d *DB) SetAgentTaskTimeoutWrapup(key, prompt string, maxTurns int) error {
	_, err := d.Exec(`UPDATE agents SET task_timeout_wrapup_prompt=$1, task_timeout_wrapup_max_turns=$2 WHERE key=$3`, prompt, maxTurns, key)
	return err
}

// SetAgentRunSeconds updates an agent's run_seconds wall-clock budget (0 = unlimited).
func (d *DB) SetAgentRunSeconds(key string, runSecs int) error {
	if runSecs < 0 {
		runSecs = 0
	}
	_, err := d.Exec(`UPDATE agents SET run_seconds=$1 WHERE key=$2`, runSecs, key)
	return err
}

// SetAgentTriggerBehavior stores an agent's P3 trigger post-processing策略:
// runMode(serial|parallel) / mergeMode(by_task|all|none) / maxParallel(parallel 用,0=不限)。
// 枚举做白名单校验,非法值回落默认,避免脏数据把调度 pump 带偏。
func (d *DB) SetAgentTriggerBehavior(key, runMode, mergeMode string, maxParallel int) error {
	switch runMode {
	case "serial", "parallel":
	default:
		runMode = "serial"
	}
	switch mergeMode {
	case "by_task", "all", "none":
	default:
		mergeMode = "by_task"
	}
	if maxParallel < 0 {
		maxParallel = 0
	}
	_, err := d.Exec(`UPDATE agents SET trigger_run_mode=$1, trigger_merge_mode=$2, trigger_max_parallel=$3 WHERE key=$4`,
		runMode, mergeMode, maxParallel, key)
	return err
}

type PromptVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
	Source      string `json:"source"`
}

func (d *DB) PromptVars(agentID int64) ([]PromptVar, error) {
	rows, err := d.Query(`SELECT var_name,COALESCE(description,''),COALESCE(example,''),source FROM agent_prompt_vars WHERE agent_id=$1 ORDER BY var_name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromptVar{}
	for rows.Next() {
		var v PromptVar
		if err := rows.Scan(&v.Name, &v.Description, &v.Example, &v.Source); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CurrentPrompt returns the agent's active template text ("" if none set yet).
func (d *DB) CurrentPrompt(agentID int64) (string, error) {
	var tmpl sql.NullString
	err := d.QueryRow(`SELECT p.template_text FROM agents a JOIN agent_prompts p ON p.id=a.current_prompt_id WHERE a.id=$1`, agentID).Scan(&tmpl)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tmpl.String, err
}

// SeedPromptIfEmpty writes the code-default template as the agent's first prompt
// version ONLY when it has none yet (current_prompt_id IS NULL). Mirrors
// SeedTool's first-insert-only philosophy: a user's edited prompt is never
// clobbered on restart. Idempotent — a no-op once any version exists.
func (d *DB) SeedPromptIfEmpty(agentID int64, tmpl string) error {
	var cur sql.NullInt64
	if err := d.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, agentID).Scan(&cur); err != nil {
		return err
	}
	if cur.Valid {
		return nil // already seeded or user-edited → leave it
	}
	_, err := d.SavePrompt(agentID, tmpl, "内置默认", "system")
	return err
}

// ResetPromptToDefault appends the code-default template as a new version and
// points current at it — the explicit "恢复为内置默认" action.
func (d *DB) ResetPromptToDefault(agentID int64, tmpl string) (int, error) {
	return d.SavePrompt(agentID, tmpl, "恢复为内置默认", "system")
}

// SavePrompt appends a new version and points current_prompt_id at it.
func (d *DB) SavePrompt(agentID int64, template, note, by string) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var ver int
	if err := tx.QueryRow(`SELECT COALESCE(max(version),0)+1 FROM agent_prompts WHERE agent_id=$1`, agentID).Scan(&ver); err != nil {
		return 0, err
	}
	var pid int64
	if err := tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')) RETURNING id`,
		agentID, ver, template, note, by).Scan(&pid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, pid, agentID); err != nil {
		return 0, err
	}
	return ver, tx.Commit()
}

type PromptVersion struct {
	Version   int       `json:"version"`
	Template  string    `json:"template_text"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"ts"`
}

func (d *DB) ListPromptVersions(agentID int64) ([]PromptVersion, error) {
	rows, err := d.Query(`SELECT version,template_text,COALESCE(note,''),created_at FROM agent_prompts WHERE agent_id=$1 ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromptVersion{}
	for rows.Next() {
		var v PromptVersion
		if err := rows.Scan(&v.Version, &v.Template, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------- MCP servers ----------

type MCPServer struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Transport string          `json:"transport"`
	Command   string          `json:"command,omitempty"`
	Args      json.RawMessage `json:"args"`
	Env       json.RawMessage `json:"env"`
	URL       string          `json:"url,omitempty"`
	Enabled   bool            `json:"enabled"`
	Insecure  bool            `json:"insecure"`        // http: skip TLS cert verification (self-signed servers, issue #108)
	Tools     []string        `json:"tools,omitempty"` // cached tool names (mcp_tools_cache)
}

func (d *DB) ListMCP() ([]*MCPServer, error) {
	rows, err := d.Query(`SELECT id,name,transport,COALESCE(command,''),args,env,COALESCE(url,''),enabled,insecure FROM mcp_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []*MCPServer
	for rows.Next() {
		var m MCPServer
		var args, env []byte
		if err := rows.Scan(&m.ID, &m.Name, &m.Transport, &m.Command, &args, &env, &m.URL, &m.Enabled, &m.Insecure); err != nil {
			rows.Close()
			return nil, err
		}
		m.Args, m.Env = json.RawMessage(args), json.RawMessage(env)
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // free the connection before the per-server tool-cache queries below
	// Attach each server's cached tool names (best-effort; empty until discovered).
	for _, m := range out {
		m.Tools, _ = d.MCPToolNames(m.ID)
	}
	return out, nil
}

// MCPToolNames returns the cached tool names for a server (empty until discovered).
func (d *DB) MCPToolNames(serverID int64) ([]string, error) {
	rows, err := d.Query(`SELECT tool_name FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MCPTool is one cached tool of an MCP server (name + description).
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MCPToolsDetailed returns the cached tools (name + description) for a server.
func (d *DB) MCPToolsDetailed(serverID int64) ([]MCPTool, error) {
	rows, err := d.Query(`SELECT tool_name, COALESCE(description,'') FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.Name, &t.Description); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveMCPTools replaces the cached tool list for a server (called after discovery).
func (d *DB) SaveMCPTools(serverID int64, tools []MCPTool) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mcp_tools_cache WHERE server_id=$1`, serverID); err != nil {
		return err
	}
	for _, t := range tools {
		if _, err := tx.Exec(`INSERT INTO mcp_tools_cache(server_id, tool_name, description) VALUES ($1,$2,$3)
ON CONFLICT (server_id, tool_name) DO UPDATE SET description=EXCLUDED.description`, serverID, t.Name, t.Description); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) SaveMCP(m *MCPServer) (int64, error) {
	args, env := string(m.Args), string(m.Env)
	if args == "" {
		args = "[]"
	}
	if env == "" {
		env = "{}"
	}
	if m.ID == 0 {
		var id int64
		err := d.QueryRow(`INSERT INTO mcp_servers(name,transport,command,args,env,url,enabled,insecure) VALUES ($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''),$7,$8) RETURNING id`,
			m.Name, m.Transport, m.Command, args, env, m.URL, m.Enabled, m.Insecure).Scan(&id)
		return id, err
	}
	_, err := d.Exec(`UPDATE mcp_servers SET name=$1,transport=$2,command=NULLIF($3,''),args=$4,env=$5,url=NULLIF($6,''),enabled=$7,insecure=$8 WHERE id=$9`,
		m.Name, m.Transport, m.Command, args, env, m.URL, m.Enabled, m.Insecure, m.ID)
	return m.ID, err
}

func (d *DB) DeleteMCP(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_visibility WHERE resource_kind='mcp' AND resource_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM mcp_servers WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- Skill visibility (agent × skill_name) ----------

// AgentSkillNames returns the skill directory names visible to an agent.
func (d *DB) AgentSkillNames(agentID int64) ([]string, error) {
	rows, err := d.Query(`SELECT skill_name FROM agent_skill_visibility WHERE agent_id=$1 AND enabled ORDER BY skill_name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// SkillAgents returns the agent IDs that can see a skill.
func (d *DB) SkillAgents(skillName string) ([]int64, error) {
	rows, err := d.Query(`SELECT agent_id FROM agent_skill_visibility WHERE skill_name=$1 AND enabled`, skillName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetAgentSkillVisibility replaces all skill visibility for an agent.
func (d *DB) SetAgentSkillVisibility(agentID int64, names []string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_skill_visibility WHERE agent_id=$1`, agentID); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := tx.Exec(`INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) VALUES ($1,$2,true)
ON CONFLICT (agent_id,skill_name) DO UPDATE SET enabled=true`, agentID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleSkillVisibility sets one (agent, skill_name) visibility on/off.
func (d *DB) ToggleSkillVisibility(agentID int64, skillName string, on bool) error {
	if on {
		_, err := d.Exec(`INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) VALUES ($1,$2,true)
ON CONFLICT (agent_id,skill_name) DO UPDATE SET enabled=true`, agentID, skillName)
		return err
	}
	_, err := d.Exec(`DELETE FROM agent_skill_visibility WHERE agent_id=$1 AND skill_name=$2`, agentID, skillName)
	return err
}

// DeleteSkillVisibility removes all visibility rows for a skill (called on skill delete).
func (d *DB) DeleteSkillVisibility(skillName string) error {
	_, err := d.Exec(`DELETE FROM agent_skill_visibility WHERE skill_name=$1`, skillName)
	return err
}

// ---------- Visibility (agent × mcp) ----------

// AgentVisible returns the resource ids of a kind visible to an agent.
func (d *DB) AgentVisible(agentID int64, kind string) ([]int64, error) {
	rows, err := d.Query(`SELECT resource_id FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND enabled`, agentID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ResourceAgents returns the agent ids that can see a resource.
func (d *DB) ResourceAgents(kind string, resourceID int64) ([]int64, error) {
	rows, err := d.Query(`SELECT agent_id FROM agent_visibility WHERE resource_kind=$1 AND resource_id=$2 AND enabled`, kind, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetAgentVisibilityKind replaces the full set of visible resources of a kind for
// an agent (agent-side bulk write). Bidirectional with the resource-side view —
// both read/write the same agent_visibility rows.
func (d *DB) SetAgentVisibilityKind(agentID int64, kind string, resourceIDs []int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2`, agentID, kind); err != nil {
		return err
	}
	for _, rid := range resourceIDs {
		if _, err := tx.Exec(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled) VALUES ($1,$2,$3,true)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=true`, agentID, kind, rid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleVisibility sets one (agent, kind, resource) visibility on/off (idempotent).
func (d *DB) ToggleVisibility(agentID int64, kind string, resourceID int64, on bool) error {
	if on {
		_, err := d.Exec(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled) VALUES ($1,$2,$3,true)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=true`, agentID, kind, resourceID)
		return err
	}
	_, err := d.Exec(`DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND resource_id=$3`, agentID, kind, resourceID)
	return err
}

// 【二开·P-TPL·D11 planner_templates 数据层】
// 功能介绍：planner 提示词模板库 CRUD（List/Get/Create/Update/Delete/SetDefault/DefaultPlannerTemplateBody 七方法）
//           + 内置/默认删除保护 + seed 首插入；仿制 db/task_templates.go 的 nkey 归一/原子合并/哨兵错风格

// 【二开·P-TPL·D11-1 planner_templates 类型与声明】起 —— 常量、哨兵错、行类型与归一化辅助
const (
	MaxPlannerTemplateNameRunes = 120
	MaxPlannerTemplateBodyBytes = 32 << 10 // 32KB 上限（按字节计，含中文）；S11 试渲染同口径
	// PlannerTemplateBuiltinName/NKey 内置默认模板的固定身份——builtin 行名称只读，
	// nkey 恒定，seed 才能用 ON CONFLICT (nkey) DO NOTHING 幂等（改名会换 nkey 导致重复 seed）。
	PlannerTemplateBuiltinName = ExternalPentestTemplateName
	PlannerTemplateBuiltinNKey = ExternalPentestTemplateName
)

var (
	ErrPlannerTemplateInvalid          = errors.New("invalid planner template")
	ErrPlannerTemplateNameConflict     = errors.New("planner template name already exists")
	ErrPlannerTemplateNotFound         = errors.New("planner template not found")
	ErrPlannerTemplateBuiltin          = errors.New("builtin planner template is read-only")
	ErrPlannerTemplateDefaultProtected = errors.New("default planner template cannot be deleted")
)

// PlannerTemplate 是一套 planner 提示词预设（段 [A] 全文替换体）。
type PlannerTemplate struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	Body      string    `json:"body"`
	Builtin   bool      `json:"builtin"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PlannerTemplateInput 是 create/update 归一化后的载荷。
type PlannerTemplateInput struct {
	Name string
	Body string
}

// PlannerTemplatePatch 只改指针非 nil 的字段（PATCH 语义）。
type PlannerTemplatePatch struct {
	Name *string
	Body *string
}

// 归一化直接复用 task_templates.go 同包的 taskTemplateName/taskTemplateNKey/taskTemplateUniqueViolation，
// 以及 template_crud.go 的 checkTemplateName/checkTemplateBody 共享规则（同包重名会编译冲突，不重复定义）。

func scanPlannerTemplate(row interface{ Scan(...any) error }) (PlannerTemplate, error) {
	t, err := scanTemplateRow(row)
	return PlannerTemplate(t), err
}

func normalizePlannerTemplateInput(in PlannerTemplateInput) (PlannerTemplateInput, string, error) {
	name, body, nkey, err := normalizeTemplateInputFields(in.Name, in.Body, MaxPlannerTemplateNameRunes, MaxPlannerTemplateBodyBytes, ErrPlannerTemplateInvalid)
	if err != nil {
		return in, "", err
	}
	in.Name, in.Body = name, body
	return in, nkey, nil
}

func normalizePlannerTemplatePatch(patch PlannerTemplatePatch) (PlannerTemplatePatch, *string, error) {
	name, nkey, body, err := normalizeTemplatePatchFields(patch.Name, patch.Body, MaxPlannerTemplateNameRunes, MaxPlannerTemplateBodyBytes, ErrPlannerTemplateInvalid)
	if err != nil {
		return patch, nil, err
	}
	patch.Name, patch.Body = name, body
	return patch, nkey, nil
}

// 【二开·P-TPL·D11-1 planner_templates 类型与声明】止

// 【二开·P-TPL·D11-2 planner_templates 读方法】起 —— List/Get/GetByNKey
// 【方法 1/7】ListPlannerTemplates 最近维护在前（与 ListTaskTemplates 同序）。
func (d *DB) ListPlannerTemplates() ([]*PlannerTemplate, error) {
	rows, err := listTemplateRows(d, "planner_templates")
	if err != nil {
		return nil, err
	}
	out := make([]*PlannerTemplate, 0, len(rows))
	for _, t := range rows {
		converted := PlannerTemplate(*t)
		out = append(out, &converted)
	}
	return out, nil
}

// 【方法 2/7】GetPlannerTemplate 不存在返回 (nil, nil)（S13 存在性校验靠它区分 404 与 500）。
func (d *DB) GetPlannerTemplate(id int64) (*PlannerTemplate, error) {
	t, err := getTemplateRow(d, "planner_templates", id)
	if err != nil || t == nil {
		return nil, err
	}
	converted := PlannerTemplate(*t)
	return &converted, nil
}

// GetPlannerTemplateByNKey 按规范化名查模板（场景/种子按 nkey 解析模板 id 用）。
func (d *DB) GetPlannerTemplateByNKey(nkey string) (*PlannerTemplate, error) {
	t, err := getTemplateRowByNKey(d, "planner_templates", nkey)
	if err != nil || t == nil {
		return nil, err
	}
	converted := PlannerTemplate(*t)
	return &converted, nil
}

// 【二开·P-TPL·D11-2 planner_templates 读方法】止

// 【二开·P-TPL·D11-3 planner_templates 写方法】起 —— Create/Update/Patch/Delete/SetDefault/DefaultBody
// 【方法 3/7】CreatePlannerTemplate 插入一套预设；nkey 冲突（视觉等价重名）→ ErrPlannerTemplateNameConflict。
func (d *DB) CreatePlannerTemplate(in PlannerTemplateInput) (*PlannerTemplate, error) {
	in, nkey, err := normalizePlannerTemplateInput(in)
	if err != nil {
		return nil, err
	}
	t, err := createTemplateRow(d, "planner_templates", in.Name, nkey, in.Body, ErrPlannerTemplateNameConflict)
	if err != nil {
		return nil, err
	}
	converted := PlannerTemplate(*t)
	return &converted, nil
}

// 【方法 4/7】UpdatePlannerTemplate 全量替换可编辑字段（PUT 语义，内部走原子 Patch）。
func (d *DB) UpdatePlannerTemplate(id int64, in PlannerTemplateInput) (*PlannerTemplate, error) {
	in, _, err := normalizePlannerTemplateInput(in)
	if err != nil {
		return nil, err
	}
	return d.PatchPlannerTemplate(id, PlannerTemplatePatch{Name: &in.Name, Body: &in.Body})
}

// PatchPlannerTemplate 单 UPDATE 原子合并所给字段（并发 disjoint PATCH 不丢改动）。
// builtin 行允许编辑正文；名称/nkey 保持 seed 和场景引用身份，内置行仍禁止删除。
// 共享实现在 template_crud.go patchTemplateRow；planner 侧 sentinel 经参数注入。
func (d *DB) PatchPlannerTemplate(id int64, patch PlannerTemplatePatch) (*PlannerTemplate, error) {
	patch, nkey, err := normalizePlannerTemplatePatch(patch)
	if err != nil {
		return nil, err
	}
	t, err := patchTemplateRow(d, "planner_templates", id, patch.Name, nkey, patch.Body,
		ErrPlannerTemplateNotFound, ErrPlannerTemplateBuiltin, ErrPlannerTemplateNameConflict)
	if err != nil {
		return nil, err
	}
	converted := PlannerTemplate(*t)
	return &converted, nil
}

// 【方法 5/7】DeletePlannerTemplate 删除一套预设；内置/默认禁删；返回是否存在并删除。
// 已选用它的任务不受影响——创建时正文已快照进 tasks.planner_prompt_override（D11-b）。
func (d *DB) DeletePlannerTemplate(id int64) (bool, error) {
	return deleteTemplateRow(d, "planner_templates", id, ErrPlannerTemplateBuiltin, ErrPlannerTemplateDefaultProtected)
}

// 【方法 6/7】SetDefaultPlannerTemplate 事务内先清后设，全库唯一默认；部分唯一索引 uq_planner_templates_default 兜底并发。
func (d *DB) SetDefaultPlannerTemplate(id int64) (*PlannerTemplate, error) {
	t, err := setDefaultTemplateRow(d, "planner_templates", id, ErrPlannerTemplateNotFound)
	if err != nil {
		return nil, err
	}
	converted := PlannerTemplate(*t)
	return &converted, nil
}

// 【方法 7/7】DefaultPlannerTemplateBody 读默认模板正文；无默认行（表空/直写库）→ ("", nil)，
// 调用方落到 00 册 A4 第④级内置模板兜底。S13 快照链与 A4 第③级共用本方法。
func (d *DB) DefaultPlannerTemplateBody() (string, error) {
	return defaultTemplateBodyRow(d, "planner_templates")
}

// 【二开·P-TPL·D11-3 planner_templates 写方法】止

// 【二开·P-TPL·D11-4 planner_templates 首插 seed】起 —— SeedPlannerTemplateIfEmpty
// SeedPlannerTemplateIfEmpty 首插入 only：nkey 已存在即 no-op，用户后续操作（换默认/新建/删除自定义）
// 重启不被重置。调用方见 server/assembly.go seedPrompts（D11-d）；正文单源=agent/planner.go
// plannerDefaultTmpl 与 agent/planner_domain_templates.go 五领域模板（经 agent 包透出）——db 不 import agent，否则成环。
// 【二开·P-LOG·S11】返回值改为 (inserted bool, err error)：供调用方只在首插时记 INFO（04 册 S11①）。
// 【二开·P-TPL·DOMAIN】isDefault 参数化：五领域模板传 false，全库默认仍只有「内置默认」一行
// （部分唯一索引 uq_planner_templates_default 保证；重复播种同名默认也不会违反）。
func (d *DB) SeedPlannerTemplateIfEmpty(name, nkey, body string, isDefault bool) (bool, error) {
	return seedTemplateIfEmptyRow(d, "planner_templates", name, nkey, body, isDefault)
}

// 【二开·P-TPL·D11-4 planner_templates 首插 seed】止

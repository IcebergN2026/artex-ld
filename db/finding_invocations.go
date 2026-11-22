// finding_invocations.go：planner 显式派发 finding 给下游 agent 的调用记录与补发认领。
// 主要函数/类型：
//   - FindingInvocation：一次 agent × finding 调用的记录（会话未创建时为待补发）。
//   - RecordFindingInvocation：首次写入 invocation；冲突时原样返回既有行，不改任何字段。
//   - SetFindingInvocationConversation：仅在 conversation_id 仍为空时回填，重复/合并投递
//     不能覆盖第一个已知会话 ID。
//   - PendingFindingInvocations：列出到期且尚未创建会话的 invocation（COALESCE 时间门控）。
//   - ClaimFindingInvocationRetry：原子推进 last_retry_at 并报告是否实际认领，正常调用与
//     兜底 sweeper 并发时不会双重补发。

package db

import (
	"database/sql"
	"errors"
	"time"
)

// FindingInvocation records one planner dispatch of a finding to an agent.
// A nil ConversationID means the downstream conversation has not been created
// yet and the 10-minute sweeper may re-enqueue the dispatch.
type FindingInvocation struct {
	AgentKey       string
	FindingID      int64
	TaskID         int64
	Reason         string
	ConversationID *int64
	CreatedAt      time.Time
	LastRetryAt    *time.Time
}

const findingInvocationCols = `agent_key, finding_id, task_id, reason, conversation_id, created_at, last_retry_at`

func scanFindingInvocation(row interface{ Scan(...any) error }) (*FindingInvocation, error) {
	inv := &FindingInvocation{}
	err := row.Scan(&inv.AgentKey, &inv.FindingID, &inv.TaskID, &inv.Reason,
		&inv.ConversationID, &inv.CreatedAt, &inv.LastRetryAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return inv, err
}

// RecordFindingInvocation inserts the first invocation for an agent × finding
// pair. On conflict it reads the existing row unchanged: reason, task_id,
// created_at and retry timestamps of the first call are never overwritten, so
// a duplicate planner call can tell already_invoked from retry_scheduled.
func (d *DB) RecordFindingInvocation(agentKey string, findingID, taskID int64, reason string) (FindingInvocation, bool, error) {
	inv, err := scanFindingInvocation(d.QueryRow(`INSERT INTO finding_agent_invocations(agent_key, finding_id, task_id, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (agent_key, finding_id) DO NOTHING
		RETURNING `+findingInvocationCols, agentKey, findingID, taskID, reason))
	if err != nil {
		return FindingInvocation{}, false, err
	}
	if inv != nil {
		return *inv, true, nil
	}
	// Conflict: return the stored row exactly as the first call left it.
	inv, err = scanFindingInvocation(d.QueryRow(`SELECT `+findingInvocationCols+`
		FROM finding_agent_invocations WHERE agent_key=$1 AND finding_id=$2`, agentKey, findingID))
	if err != nil {
		return FindingInvocation{}, false, err
	}
	if inv == nil {
		return FindingInvocation{}, false, errors.New("finding_agent_invocations 行在冲突读取时消失")
	}
	return *inv, false, nil
}

// SetFindingInvocationConversation backfills the conversation created for a
// dispatch. It only updates rows whose conversation_id is still NULL, so a
// repeated or merged delivery can never overwrite the first known session.
func (d *DB) SetFindingInvocationConversation(agentKey string, findingID, conversationID int64) error {
	_, err := d.Exec(`UPDATE finding_agent_invocations SET conversation_id=$3
		WHERE agent_key=$1 AND finding_id=$2 AND conversation_id IS NULL`,
		agentKey, findingID, conversationID)
	return err
}

// PendingFindingInvocations lists dispatches whose conversation does not exist
// yet and whose COALESCE(last_retry_at, created_at) is at least olderThan old,
// oldest first, capped at limit. Rows already backfilled are never replayed.
func (d *DB) PendingFindingInvocations(olderThan time.Duration, limit int) ([]FindingInvocation, error) {
	rows, err := d.Query(`SELECT `+findingInvocationCols+` FROM finding_agent_invocations
		WHERE conversation_id IS NULL AND COALESCE(last_retry_at, created_at) <= now() - ($1 * interval '1 microsecond')
		ORDER BY created_at ASC LIMIT $2`, olderThan.Microseconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FindingInvocation{}
	for rows.Next() {
		inv, err := scanFindingInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// ClaimFindingInvocationRetry atomically advances last_retry_at on a due,
// still-pending invocation and reports whether this caller actually claimed it.
// Only the caller that updated the row may re-enqueue; a concurrent normal
// dispatch and sweeper pass therefore cannot double-enqueue the same finding.
func (d *DB) ClaimFindingInvocationRetry(agentKey string, findingID int64, olderThan time.Duration) (bool, error) {
	res, err := d.Exec(`UPDATE finding_agent_invocations SET last_retry_at=now()
		WHERE agent_key=$1 AND finding_id=$2 AND conversation_id IS NULL
		AND COALESCE(last_retry_at, created_at) <= now() - ($3 * interval '1 microsecond')`,
		agentKey, findingID, olderThan.Microseconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

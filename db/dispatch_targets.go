// dispatch_targets.go：任务分发目标表 task_dispatch_targets 的写入与查询，含资产键归一化与冲突检测。
// 主要函数/类型：
//   - TaskDispatchTarget：分发目标行结构（资产键 → 任务/漏洞）。
//   - CanonicalAssetKey / CanonicalAssetKeys：把资产归一化为分发键。
//   - insertTaskDispatchTargets：事务内写入分发目标，键冲突时返回 TaskDispatchConflictError。
//   - ListTaskDispatchTargets：按键集合查询分发目标。

package db

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// TaskDispatchTarget records the canonical host asset that an internal-pentest
// task was created for. It is deliberately separate from assets.task_ids: source
// inheritance links every parent asset and cannot identify the dispatch target.
type TaskDispatchTarget struct {
	AssetKey  string `json:"asset_key"`
	TaskID    int64  `json:"task_id"`
	FindingID int64  `json:"finding_id"`
	TaskName  string `json:"task_name"`
	Status    string `json:"status"`
}

type TaskDispatchConflictError struct {
	AssetKey string
	TaskID   int64
}

func (e *TaskDispatchConflictError) Error() string {
	return fmt.Sprintf("资产 %s 已创建内网渗透子任务 #%d", e.AssetKey, e.TaskID)
}

// CanonicalAssetKey collapses an IP, its services and its endpoints to one host
// key. Ports, schemes and URL paths never participate in dispatch deduplication.
func CanonicalAssetKey(a *Asset) string {
	if a == nil {
		return ""
	}
	candidates := []string{a.IP, a.Domain, a.RootDomain}
	for _, candidate := range candidates {
		if key := canonicalHostKey(candidate); key != "" {
			return key
		}
	}
	if parsed, err := url.Parse(a.URL); err == nil {
		return canonicalHostKey(parsed.Hostname())
	}
	return ""
}

func canonicalHostKey(raw string) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return "ip:" + ip.String()
	}
	return "domain:" + host
}

// CanonicalAssetKeys deduplicates all hosts linked to a Finding while retaining
// distinct hosts so the caller can require an explicit primary target.
func CanonicalAssetKeys(assets []*Asset) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, asset := range assets {
		key := CanonicalAssetKey(asset)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeDispatchAssetKeys(keys []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" || (!strings.HasPrefix(key, "ip:") && !strings.HasPrefix(key, "domain:")) {
			return nil, fmt.Errorf("invalid dispatch asset key %q", raw)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// insertTaskDispatchTargets runs inside CreateTaskWithOptions. The unique asset
// key is the hard cross-parent and cross-concurrency guard; any conflict rolls
// back the new exploration, task and relations in the same transaction.
func insertTaskDispatchTargets(tx *sql.Tx, taskID, findingID int64, keys []string) error {
	keys, err := normalizeDispatchAssetKeys(keys)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	if findingID <= 0 {
		return fmt.Errorf("finding_id is required for internal dispatch targets")
	}
	for _, key := range keys {
		var insertedKey string
		err := tx.QueryRow(`
INSERT INTO task_dispatch_targets(asset_key, task_id, finding_id)
VALUES ($1,$2,$3)
ON CONFLICT (asset_key) DO NOTHING
RETURNING asset_key`, key, taskID, findingID).Scan(&insertedKey)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		var existingTaskID int64
		if queryErr := tx.QueryRow(`SELECT task_id FROM task_dispatch_targets WHERE asset_key=$1`, key).Scan(&existingTaskID); queryErr != nil {
			return queryErr
		}
		return &TaskDispatchConflictError{AssetKey: key, TaskID: existingTaskID}
	}
	return nil
}

func (d *DB) ListTaskDispatchTargets(keys []string) ([]TaskDispatchTarget, error) {
	keys, err := normalizeDispatchAssetKeys(keys)
	if err != nil || len(keys) == 0 {
		return []TaskDispatchTarget{}, err
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = key
	}
	rows, err := d.Query(`
SELECT d.asset_key, d.task_id, d.finding_id, COALESCE(t.name,''), t.status
FROM task_dispatch_targets d
JOIN tasks t ON t.id=d.task_id
WHERE d.asset_key IN (`+strings.Join(placeholders, ",")+`)
ORDER BY d.asset_key, d.task_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskDispatchTarget{}
	for rows.Next() {
		var target TaskDispatchTarget
		if err := rows.Scan(&target.AssetKey, &target.TaskID, &target.FindingID, &target.TaskName, &target.Status); err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	return out, rows.Err()
}

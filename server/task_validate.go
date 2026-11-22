// task_validate.go：建任务与 spawn_task 共享的输入校验和默认提示词读取。
// 主要函数/类型：
//   - validatePreAgent：判定 pre_agent（executor_key）是否为已启用非内置 agent。
//   - normalizeSourceTaskIDs：解析、去重并校验关联任务 id 列表。
//   - validateWorkerCount：校验 worker_count 取值范围（0~16）。
//   - defaultPlannerPrompt / defaultWorkerPrompt：读取默认 planner/worker 模板正文快照。
package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"artex/db"
)

// errPreAgentUnavailable 是 validatePreAgent 的哨兵错误：pre_agent（executor_key）不可用。
// 具体报错文案各调用侧自定（HTTP 建任务 vs spawn_task 文案不同），helper 只负责判定。
var errPreAgentUnavailable = errors.New("pre_agent unavailable")

// errWorkerCountOutOfRange 是 validateWorkerCount 的哨兵错误：worker_count 越界。
// 报错文案各调用侧自定（「取值」vs「合法区间」）。
var errWorkerCountOutOfRange = errors.New("worker_count out of range")

// validatePreAgent 判定 pre_agent（executor_key）是否可用：须为已启用且非内置的自定义 agent。
// key 为空 → 可用（nil）；err/不存在/!Enabled/Builtin → errPreAgentUnavailable。
func validatePreAgent(pg *db.DB, key string) error {
	if key == "" {
		return nil
	}
	ea, err := pg.GetAgentByExecutorKey(key)
	if err != nil || ea == nil || !ea.Enabled || ea.Builtin {
		return errPreAgentUnavailable
	}
	return nil
}

// normalizeSourceTaskIDs 解析/去重/校验关联任务 id 列表（HTTP 建任务与 spawn_task 同款规则）。
// 上限 db.MaxTaskSourceCount；error 文案两侧逐字一致。exists 由调用侧闭包注入（便于无 DB 单测）。
func normalizeSourceTaskIDs(raw []string, exists func(id int64) bool) ([]int64, error) {
	if len(raw) > db.MaxTaskSourceCount {
		return nil, fmt.Errorf("关联任务最多选择 %d 个", db.MaxTaskSourceCount)
	}
	ids := make([]int64, 0, len(raw))
	seen := map[int64]bool{}
	for _, r := range raw {
		id, err := strconv.ParseInt(strings.TrimSpace(r), 10, 64)
		if err != nil || id <= 0 || seen[id] {
			return nil, fmt.Errorf("关联任务 id 无效或重复")
		}
		if !exists(id) {
			return nil, fmt.Errorf("关联任务 #%d 不存在", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// validateWorkerCount 判定 worker_count 是否合法（0~16；0=跟随全局）。报错文案各调用侧自定。
func validateWorkerCount(n int) error {
	if n < 0 || n > 16 {
		return errWorkerCountOutOfRange
	}
	return nil
}

// defaultPlannerPrompt 读取默认 planner 模板正文快照（统一「默认模板正文」读取点；
// createTask 与 spawn_task 同型快照链第三级共用）。
func defaultPlannerPrompt(pg *db.DB) (string, error) {
	return pg.DefaultPlannerTemplateBody()
}

// defaultWorkerPrompt 读取默认 worker 模板正文快照（同 defaultPlannerPrompt）。
func defaultWorkerPrompt(pg *db.DB) (string, error) {
	return pg.DefaultWorkerTemplateBody()
}

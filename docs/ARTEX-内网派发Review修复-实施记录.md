# ARTEX 内网派发 Review 修复实施记录

## 问题

正式子任务 `#11` 的分类为“内网渗透”，但 `review_enabled=false`。父任务 `#10` 未开 Review，而内网分类 `default_review_enabled=true`。

根因不是分类配置，而是 Dispatcher 改用 `planner_template_nkey` 后，`spawn_task` 仍只在 `PlannerTemplateID > 0` 时读取分类默认审核开关。稳定 `nkey` 路径的 ID 为 0，绕过了内网场景默认开启 Review 的逻辑。

## 代码修改

### `server/orchestration.go`

新增 `resolveSpawnReview(inherited bool, scenarioNKey string) (bool, error)`：

1. 父任务 Review 已开启：子任务必须开启。
2. 父任务未开启：按统一解析后的场景 `nkey` 查询分类默认。
3. 数字模板和 `nkey` 模板使用同一 `scenarioNKey`，不再依赖数值 ID 是否大于 0。
4. 未选择场景或分类不存在时保持父任务值。

`spawn_task` 不暴露 Review 参数，避免 Dispatcher 覆盖安全默认。

### `agent/promptcatalog.go`

Dispatcher 提示词改为：

> 审核开关由代码按两级默认决定：父任务已开启 review 时子任务必须开启；父任务未开启时，按所选场景分类的 default_review_enabled 决定（内网渗透默认开启）。spawn_task 不提供 review 参数，dispatcher 不传也不需要传；你只负责选场景模板。

`config-export/agent_prompts_current.jsonl` 已同步。

## 测试映射

| 测试 | 实现/断言 |
|---|---|
| `TestResolveSpawnReviewUsesNKeyCategoryDefault/nkey_category_enables_review` | `server/orchestration.go:resolveSpawnReview`；内网 `nkey` 在父任务关闭 Review 时仍开启 |
| `TestResolveSpawnReviewUsesNKeyCategoryDefault/parent_true_remains_enabled` | 父任务 Review 不被场景默认覆盖 |
| `TestResolveSpawnReviewUsesNKeyCategoryDefault/unknown_category_stays_disabled` | 未知分类不擅自开启 |
| `TestResolveSpawnReviewUsesNKeyCategoryDefault/no_scenario_stays_disabled` | 无场景保持原值 |
| `TestVulnDispatcherDefaultPromptUsesStableTemplateKeys` | 提示词包含分类默认，不再写“一律继承父任务” |
| 全量 `go test -p=1 ./...` | 所有 Go 包通过，日志 `/root/artex-review-go-test.log` |
| `go vet ./...` | 通过，日志 `/root/artex-review-go-vet.log` |

## 部署与数据修复

- 数据库备份路径记录在 `/root/artex-review-backup-path`，备份权限为 0600。
- Docker 日志：`/root/artex-review-deploy.log`。
- 正式 Dispatcher 当前提示词通过新增版本切换，没有改写历史提示词版本。
- 尚未运行的子任务 `#11` 已从 `review_enabled=false` 修为 `true`。
- 部署后 `/api/health` 返回 `ok=true`。
- 正式库确认：任务 `#11 review_enabled=t`；当前 Dispatcher 提示词含 `default_review_enabled` 且不含旧的“一律继承父任务”。

## 边界

没有重放历史 Dispatcher，也没有自动启动或执行子任务。此次验证为数据库集成测试、全量 Go 测试和正式字段检查；未做真实 LLM 派发端到端测试。

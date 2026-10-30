# ARTEX Worker 投放事实登记实施记录

## 结论

状态：已实施、全量测试通过并部署。

用户指出 Worker 提示词没有“投放”说明。核实后确认这是实际缺口：`trace_cleaner` 要求读取 `[投放]` 事实，但 Worker 默认提示词和六个内置 Worker 模板都没有登记契约。

没有新增 `record_delivery` 工具，因为代码中不存在该工具。投放物继续使用现有 `record_fact(intent_id,...)` 写入探索图。

## 1. 根因与影响

- Worker 只说明了普通事实、资产和 finding 的写回，没有说明 WebShell/C2/代理工具的落地记录。
- 多个 Worker 场景模板由 `workerDefaultTmpl` 派生，所以一个源缺口扩散到全部内置场景。
- 清理 Agent 找不到 `[投放]` 事实时，无法可靠形成清理清单；最终只有用户手动说明路径才能补救。

## 2. 实际提示词

代码：`agent/worker.go:240` 的 `WorkerDeliveryFactSpec`，并组合进 `workerDefaultTmpl`。

```text
- **投放登记 → record_fact（探索图，传 intent_id，强制）**：每次实际上传、写入或部署 WebShell、C2 木马、反弹脚本、代理/隧道工具及其他后续需要清理的文件后，必须立即登记；summary 以 `[投放]` 开头，detail 写明目标 host/容器、落地绝对路径、产物类型、上传方式、用途、关联 finding（如有）和可清理性；evidence 写实际上传/写入命令及成功回显，confidence=observed，能得 sha256 时一并记录。**每个投放物单独一条 `[投放]` 事实，这是“一意图通常只一条事实”的强制例外；report_finding 不能代替投放登记。失败或仅尝试上传不登记。**后续痕迹清理依赖这些事实，遗漏会导致产物无法销账。
```

这里“每个投放物一条事实”是对普通事实尽量合并的明确例外，用于清理逐项销账。没有要求登记虚假的上传尝试，也不把 finding 当作投放物清单。

## 3. 六个模板如何生效

`agent/worker_domain_templates.go` 继续从 `workerDefaultTmpl` 派生五个场景变体；源码默认、外网渗透、漏洞挖掘、代码审计、内网渗透、权限提升、漏洞利用都包含同一段投放契约。

新增 `server/worker_delivery_migration.go`：

```go
const workerDeliveryFactMigrationKey = "worker_delivery_facts_v1"

func injectWorkerDeliveryFactSpec(body string) string {
    if strings.Contains(body, "[投放]") ||
        !strings.Contains(body, workerReportFindingMarker) {
        return body
    }
    return strings.Replace(body, workerReportFindingMarker,
        "\n"+agent.WorkerDeliveryFactSpec+workerReportFindingMarker, 1)
}
```

`server/assembly.go` 启动时调用 `upgradeWorkerDeliveryFacts(pg)`：

- 只更新 `worker_templates.builtin=true` 的六条记录。
- 更新全局 `agent_prompts` 的 worker 当前正文。
- 缺少内置锚点的完全自定义正文不改。
- 已有 `[投放]` 不重复插入。
- `tasks.worker_prompt_override` 是任务创建时快照，历史任务不回写。

正式库结果：

```text
built_in_templates=6,delivery=6,unauthorized=6
global_worker_delivery=1
historical_snapshot_delivery=0
migration=done
```

`unauthorized=6` 是“没有未授权扫描假设”的既有要求继续保留，不是新增访问限制。

## 4. 配置导出

`config-export/agent_prompts_current.jsonl` 的 worker 正文已与 `agent.BuiltinPromptSeeds()["worker"]` 完整同步，不只是追加一个片段；同时消除了旧文件中的 CRLF 和过期无范围策略文本。

新增 `TestWorkerDeliveryConfigExportMatchesDefault` 防止以后源码与配置包再次漂移。

## 5. 测试映射

| 测试 | 覆盖 | 文件 |
|---|---|---|
| `TestWorkerTemplatesIncludeDeliveryFactContract` | 默认及五个派生场景都含共享投放契约 | `agent/worker_delivery_test.go` |
| `TestInjectWorkerDeliveryFactSpec` | 旧内置正文注入、幂等、自定义正文不变 | `server/worker_delivery_migration_test.go` |
| `TestWorkerDeliveryConfigExportMatchesDefault` | 配置导出与源码默认完全一致 | 同上 |
| `go test -p=1 ./...` | 全量通过，含 PostgreSQL 测试 | `/root/artex-worker-delivery-go-test.log` |
| `go vet ./...`、`git diff --check` | 通过 | `/root/artex-worker-delivery-go-vet.log` |

## 6. 部署与边界

- 数据库备份：`/root/artex-pre-worker-delivery-20261029-101855.dump`，0600。
- Docker 重建后 `/api/health` 返回 ok。
- 日志：`/root/artex-worker-delivery-deploy.log`。
- 没有进行真实 WebShell/C2 上传，也没有对任何目标执行渗透动作。
- 提示词能指导模型登记，但不是数据库硬约束；真实闭环还需在授权测试环境检查 `[投放]` 事实和 trace_cleaner 清单是否一致。

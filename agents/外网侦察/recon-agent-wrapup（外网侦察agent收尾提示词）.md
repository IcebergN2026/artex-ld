进入收尾阶段——这不是任务终止：用户下一条消息你即可继续。一次收尾只补**一轮**被动发散；用户回复「继续」后从持久化队列（state 中的 RUN_ID/next_batch 与 `raw/asset_inventory.jsonl`）恢复，不能把"一轮结束"写成"全部收集结束"。按以下顺序执行审计与补充收集，禁止伪造完成状态、禁止把 `limited`/`not_executed` 说成 `ok`、禁止为凑进度跳过门禁。

# 第一步：进度审计（只读，对照 skill 契约）

在当前工作目录的 `recon_<target>_<ts>/` 下：

1. 读 `raw/execution_ledger.jsonl`、批次 manifest 与 `raw/asset_inventory.jsonl`，统计并如实报告：
   - **批次数**：已执行几个收集批次、当前 RUN_ID 与下一批次号（`next_batch`），最后一批的状态（complete / PENDING / 中断）；
   - **发散收集次数**：读取当前 run 的全部 `divergence/plan_<run>_<batch>.json` 与同批 `divergence/report_<run>_<batch>.json`，按来源分列 planned/executed/cached/ok/empty/limited/failed/disabled/manual_pending；CT 精确链与 clue plan 分开统计，不用全局文件或 hash 游标代替逐轮报告；
   - **状态分布**：全部查询计划的 `ok`/`empty`/`manual_done`/`limited`/`failed`/`not_executed`/`disabled` 计数；
   - **pending 分布**：inventory 各 `next_action`（exact_collect/probe/passive_expand）的 pending 数与 `manual_pending`/`limited` 数（`batch_triage.py pending-work`）。
   - **附加一致性**：读取 `s1_ready.json`，核对 cold_start/degraded/missing 和 clues sha256；逐轮报告中的 `unconsumed_subjects` 必须逐项列出。读取 `manual_work_report.json` 与 `related_entity_tasks_<target>.json`，分别列出人工待办、证据失效重开和关联主体深度/授权状态；任务或模板存在不得写成调研完成。存在 `source_health_<run>.json` 时逐源报告健康状态；裸 IP 必须核对 verdict↔inventory↔主 CSV，manual_confirm 不得写成已授权。
2. 对照 S0-S5 阶段矩阵逐项判定（完成/未完成/受限 limited/未授权）：
   - S0 环境自检与初始化；S1 业务画像与线索包；S2 被动链（CT/子域/DNS/测绘/历史 DNS）；S3 主动链（探活/合格裸 IP 端口/URL 双轴，按已授权范围）；S4 敏感泄露（目录/JS/GitHub/云桶等）；S5 报告与验收。
3. 检查线索包 `clues_<target>.yaml`：keywords、品牌、合作方（partners）、组织架构（org_hierarchy）、ICP、GA ID 等线索中，哪些还没被用于发散查询。

# 第二步：双门禁验收（机器判定，不靠回忆）

1. 运行 `recon_accept`（FAIL 立即停止，先补做或修正再重跑，禁止带 FAIL 收尾）；
2. 运行 `recon_accept_gaps` / `accept_check --gaps` 读 stdout 的 JSON（`complete/degraded/gaps/warnings`），并对照 skill 的 `assets/deliverables.json` **补充注册表**逐项核对补充交付物（含条件触发项，如 `unified/people.json`、逐 root 历史线索报告、`generated/` 下条件触发的报告、`extended_assets_<target>.jsonl`）——它是补充注册表，不是完整产物清单，完整产物以 SKILL 和阶段入口为准；
3. 完成声明的**唯一依据** = `--gaps` 输出 `complete=true`（完整完成）或 `degraded=true`（降级完成，报告不得写"完整覆盖"）；`recon_accept` exit 0 本身不构成完整覆盖证明。任一不满足则收尾报告的缺口清单逐条引用工单条目并注明原因（未授权 / limited / 环境阻塞 / not_executed），禁止凭回忆自判、禁止把缺口改写成已完成。

# 第三步：条件满足时，再跑一轮发散收集（仅限被动轴）

仅当**全部**满足时才执行：① 授权级别覆盖被动收集（无全流程主动授权时止步于此）；② 存在未执行的被动发散线索或可自动处理的 inventory pending。

- **先消费 inventory pending**：可自动处理项（exact_collect 的 A+授权 root、probe 的 in+authorized、passive_expand）按 `pending-work` 顺序处理；
- 再执行 `recon_clues_run`（驱动注册表被动发散轴）和/或 `recon_gdork`（Serper dork），只跑**一轮**并生成当前批 plan/report；逐 query_id 复用 artifact/hash 仍有效的成功记录，只执行新增、failed/需重试 limited 或证据失效项，不重跑成功源；
- divergent 结果**只进入 inventory/triage，不得直接 normalize/merge/`insert_assets`**；只有 authoritative、in-scope 结果才 normalize/merge，随后重跑 `recon_converge`，执行 `batch_triage.py settle-batch` 并以生产 `pending-work` 重算余量；
- 出现任何新 pending 即保持 `COLLECTION_PENDING` 并**停止收尾**（等用户「继续」）；无新增后，对最新 CSV 执行 emit → 逐批 `insert_assets` → 真实回执登记，并按需 `record_fact`（分级/受限/extended 未知类型等收尾事实；intent_id 仅运行时已知真实探索图 intent node id 时传入、否则省略，严禁任务/会话 id 替代），随后重跑双门禁；
- **禁止**在收尾阶段启动新的主动触网动作（探活/端口/目录/爬取）、禁止扩 scope、禁止绕过授权门禁。
- 批次链/任务失败场景不写收敛或受限事实（`record_fact` 一律不调用），停止当前动作链、如实报告阻塞原因与现场路径，停住等用户指示（同启动协议「任务失败处置」）。

# 第四步：更新人工报告与 extended 注册表

双门禁通过后：更新根部 `business_profile_<target>.md`、`sensitive_findings_<target>.md`、`summary_report_<target>.md` 三份人工报告与 `extended_assets_<target>.jsonl` 注册表，把各类型 pending、`limited` 与降级项逐条写入摘要 `## 受限项`；随后依次执行**报告门禁 → `recon_accept` → `recon_qa` → `recon_accept_gaps`**。

# 第五步：收尾报告（最后单独一段纯文本给用户）

固定包含：
1. **阶段矩阵**：S0-S5 逐项状态一行一列；
2. **统计**：批次数、逐轮 planned/executed/cached（CT 与 clue plan 分列）、disabled/limited/unconsumed_subjects、本次收尾新增的发散轮次与新增资产数、各类型 pending 数；S1 readiness 和已执行的源健康状态必须列入覆盖边界；
3. **状态**：完整 / 降级 / 部分（COLLECTION_COMPLETE / DEGRADED / PENDING）；
4. **ARTEX 回写**：已回写 / 跳过（unchanged）/ 失败数（来自 `raw/writeback_receipts.jsonl`）；
5. **缺口清单**：消费 `accept_check --gaps` 的 JSON 工单逐条转写（缺什么、原因：未授权 / limited / 环境阻塞 / not_executed），不得漏条；第三步补充收集后需重跑双门禁，以最新工单为准；
6. **未使用种子**（unresolved_seed 或始终未进入收集链的输入）与**未授权项**清单；
7. **覆盖边界**：主动动作授权范围、CIDR/裸 IP 授权边界、注册表 `disabled`/GAP 项及其依据；
8. **下一步建议**：用户回复「继续」即从持久化队列恢复续跑；如需扩授权（主动动作/新根域/裸 IP/CIDR），明确列出待用户确认的授权项。

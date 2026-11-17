---

# artex-ld

当前版本：**0.1.6**；Release 名称：**artex-ld 0.1.6**；Git Tag：**v0.1.6**。

基于 [ARTEX](https://github.com/autumn27/artex) v0.3.15+ 的二次开发版本：AI 自主渗透测试平台。
在原有「planner-worker 自主推进」基础上，新增四个可控点：**场景可配**（任务分类/模板/种子意图）、**意图可审**（AI 预审 + 人工批准/驳回）、**执行者可指定**（自定义 agent 执行意图）、**finding 可编排**（触发器自动派生子任务/写报告/复测）。所有增强默认关闭，不使用则行为与原版一致。

> ⚠️ 仅限合法授权、自有系统与隔离靶场。审核、拦截和人工确认只是辅助控制，不能替代授权边界、目标隔离与操作者判断。

## 快速开始

### 方式一：Release 部署包（推荐，自带 PostgreSQL 自动初始化）

```bash
# 1. 新建目录并下载 Release 部署包
mkdir artex && cd artex
wget https://github.com/IcebergN2026/artex-ld/releases/download/0.1.6/artex-0.1.6-linux-amd64.deploy.tar.gz

# 2. 解压并一键安装（初始化数据库；不注册/代建任何系统服务）
tar xzf artex-0.1.6-linux-amd64.deploy.tar.gz
chmod +x install.sh
sudo bash install.sh --server artex-0.1.6-linux-amd64.deploy.tar.gz
```

### 方式二：Docker Compose（从源码构建）

```bash
git clone https://github.com/IcebergN2026/artex-ld.git
cd artex-ld
chmod +x deploy.sh && ./deploy.sh
# 已有镜像无需重新构建时：./deploy.sh --no-build
```

首次运行自动生成权限 `0600` 的 `.env`（含随机 PostgreSQL 密码），已有 `.env` 不会被覆盖。

### 方式三：源码构建

```bash
git clone https://github.com/IcebergN2026/artex-ld.git
cd artex-ld
chmod +x build.sh
./build.sh        # 需要 Go 1.26+、Node.js 20+
export ARTEX_PG_DSN='postgres://artex:密码@127.0.0.1:5432/artex?sslmode=disable'
./dist/artex-linux-amd64/artex
```

### 装后管理

| 事项                | 命令                                                  |
| ----------------- | --------------------------------------------------- |
| 启动/停止（二进制安装）      | `cd /opt/artex && ./start.sh`                       |
| 升级（Docker）        | `./update.sh`                                       |
| 升级（二进制）           | 重跑 `install.sh --server <新包>`（自动备份数据库与旧二进制）         |
| 忘记管理员密码           | `./reset-password.sh`                               |

### 首次使用

1. 浏览器访问 `http://<服务器IP>:8787/setup`，设置管理员密码。
2. 在 **系统 → LLM** 配置模型（Anthropic / OpenAI / 兼容 OpenAI 的私有网关均可）。
3. **任务 → 新建任务**：选择场景分类与 planner 模板，填写目标开跑。

### 配套 MCP（可选增强）

场景预设中的部分能力依赖以下 MCP，按需安装后在 **系统 → MCP** 配置并授权可见：

| MCP | 用途 | GitHub | 安装 / 启动 |
|---|---|---|---|
| **GodzillaSuper** | webshell 管理 + 立足点实连验证（`shell_list` / `shell_test`），trace_cleaner 依赖 | [Xaaaa-bip/GodzillaSuper](https://github.com/Xaaaa-bip/GodzillaSuper) | 需 Java 17+，从其 Releases 下载 `gsl5.jar`：`java -jar gsl5.jar mcp 9123 127.0.0.1`，SSE 地址 `http://127.0.0.1:9123/sse` |
| **Playwright MCP** | 浏览器操作（资产探测、截图取证） | [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp) | `npx @playwright/mcp --headless` |
| **ScopeSentry** | 资产测绘同步 | [DoubleShark/ScopeSentry](https://github.com/DoubleShark/ScopeSentry) | 见其 README（Docker 部署），HTTP 方式接入，API Key 在 ScopeSentry 侧生成 |

## 外网侦察 Agent（external-pentest-recon）

仓库内新增外网侦察 Skill 与配套会话型 agent（`recon_scout`），用于授权范围内的外网前期侦察（OSINT / footprinting）。

- **六阶段流程**：S0 准备与初始化 → S1 业务调研（OSINT）→ S2 被动侦察 → S3 主动侦察 → S4 敏感发现 → S5 报告与验收；S0/S1/S5 各执行一次，S2–S4 由待处理队列驱动多轮批次循环，新资产按 A/B/C 分级收敛后扩张攻击面。
- **授权门禁**：被动侦察可随任务启动进入；主动探活、端口/目录扫描、爬取必须显式全流程授权；新增 CIDR、裸 IP、新根域各自独立授权门，未授权不执行。
- **只生成事实**：`recon_scout` 经 Skill 工具执行，资产用 `insert_assets` 逐批回写并登记真实回执，结论用 `record_fact` 写入；不创建 intent/hint，触发后输出建议的首个侦察意图配置供 planner/main agent 采纳。
- **产物**：业务画像、资产 CSV、敏感发现清单（critical 立即披露与可恢复检查点）、摘要报告。
- **依赖与 key**：首次使用执行 `krun --check` 自检，失败按 `install_toolchain.sh` 确定性修复；`config/api_keys.yaml` 仅模板，按需填写 FOFA / Shodan / Serper / GitHub / Hunter / Quake / ZoomEye 等 key（不要提交真实 key）。

安装方式：

1. 将 `agents/外网侦察/external-pentest-recon/` 作为 Skill 导入（或放入 `skill_dir`，例如 `skills/`）。
2. 在「系统 → Agent」用 `agents/外网侦察/recon-agent-prompt（外网侦察agent提示词）.md` 创建自定义 agent，收尾提示词为其配套。
3. 细节见 [`SKILL.md`](agents/外网侦察/external-pentest-recon/SKILL.md) 与 [`README`](agents/外网侦察/README.md)。

## 文档

| 内容                    | 链接                                                     |
| --------------------- | ------------------------------------------------------ |
| 功能详解（agent/工具/审核流/案例） | [docs/ARTEX-二开功能详解.md](docs/ARTEX-二开功能详解.md)               |
| 部署手册（依赖/离线构建/升级/反代）   | [docs/ARTEX-部署手册.md](docs/ARTEX-部署手册.md)               |
| 换环境迁移方案               | [docs/ARTEX-换环境迁移方案.md](docs/ARTEX-换环境迁移方案.md)         |
| 全流程代码链                | [docs/ARTEX-二开-全流程代码链文档.md](docs/ARTEX-二开-全流程代码链文档.md) |
| 更新日志                  | [CHANGELOG.md](CHANGELOG.md)                           |

## 关注我们

**公众号：凌度安全(LDsec)**

微信扫码关注，获取版本更新、实战案例与使用技巧：

![](docs/images/wechat-qrcode.jpg)

## 许可

[AGPL v3](LICENSE)，遵循上游许可。二次开发组件与上游第三方组件的版权及许可义务不因修改而消失。

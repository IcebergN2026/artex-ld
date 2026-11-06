# artex-ld

当前版本：**0.1.4**；Release 名称：**artex-ld 0.1.4**；Git Tag：**v0.1.4**。

基于 [ARTEX](https://github.com/autumn27/artex) v0.3.15+ 的二次开发版本：AI 自主渗透测试平台。
在原有「planner-worker 自主推进」基础上，新增四个可控点：**场景可配**（任务分类/模板/种子意图）、**意图可审**（AI 预审 + 人工批准/驳回）、**执行者可指定**（自定义 agent 执行意图）、**finding 可编排**（触发器自动派生子任务/写报告/复测）。所有增强默认关闭，不使用则行为与原版一致。

> ⚠️ 仅限合法授权、自有系统与隔离靶场。审核、拦截和人工确认只是辅助控制，不能替代授权边界、目标隔离与操作者判断。

## 快速开始

### 方式一：Release 部署包（推荐，自带 PostgreSQL 自动初始化）

```bash
# 1. 新建目录并下载 Release 部署包
mkdir artex && cd artex
wget https://github.com/IcebergN2026/artex-ld/releases/download/0.1.4/artex-0.1.4-linux-amd64.deploy.tar.gz

# 2. 解压并一键安装（自动注册 systemd 服务、初始化数据库）
tar xzf artex-0.1.4-linux-amd64.deploy.tar.gz
chmod +x install.sh
sudo bash install.sh --server artex-0.1.4-linux-amd64.deploy.tar.gz
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

## 文档

| 内容                    | 链接                                                     |
| --------------------- | ------------------------------------------------------ |
| 功能详解（agent/工具/审核流/案例） | [docs/ARTEX-功能详解.md](docs/ARTEX-功能详解.md)               |
| 部署手册（依赖/离线构建/升级/反代）   | [docs/ARTEX-部署手册.md](docs/ARTEX-部署手册.md)               |
| 换环境迁移方案               | [docs/ARTEX-换环境迁移方案.md](docs/ARTEX-换环境迁移方案.md)         |
| 全流程代码链                | [docs/ARTEX-二开-全流程代码链文档.md](docs/ARTEX-二开-全流程代码链文档.md) |
| 更新日志                  | [CHANGELOG.md](CHANGELOG.md)                           |

## 许可

[AGPL v3](LICENSE)，遵循上游许可。二次开发组件与上游第三方组件的版权及许可义务不因修改而消失。

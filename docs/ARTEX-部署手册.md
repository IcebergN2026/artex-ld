# artex-ld 部署手册

> 适用对象：本仓库当前二开源码发布包。代码核查基线为 `1902e43`；发布包不附带内部历史二进制、研究材料或现场配置。
>
> 本文只写当前发布包实际支持的部署方式：Docker 源码构建、Go 本地构建、Linux 二进制部署包。旧版本的实例路径和历史升级记录不属于本发布包的操作步骤。
>
> 仅用于明确授权的目标。默认 Web/API 监听 `:8787`，可能绑定所有网卡；生产环境请使用反向代理、TLS、认证和访问控制，不要把未配置认证的实例直接暴露到公网。

## 1. 环境与端口

### 依赖

| 用途 | 依赖 |
|---|---|
| Docker 部署 | Docker Engine、Docker Compose v2 |
| 源码构建 | Go 1.26+、Node.js/npm、rsync（build.sh 前端同步）、Git |
| 二进制部署 | Ubuntu/Debian、PostgreSQL 16、tar/gzip、curl |
| 可选 MCP | 按服务器类型准备 stdio 命令或网络 endpoint；公开示例 MCP 默认禁用 |

### 默认监听

| 服务 | 默认地址 | 用途 |
|---|---|---|
| Web/API | `:8787` | UI、HTTP API、SSE；默认可能暴露到所有网卡 |
| MITM 代理 | `127.0.0.1:8788` | 浏览器代理、CA 证书管理；不应直接对公网开放 |

Docker Compose 将 `8787` 映射到宿主机，将 `8788` 仅映射到宿主机回环地址。需要限制 Web 暴露时，可把 Compose 端口改为 `127.0.0.1:8787:8787`，再由本机反向代理转发。

## 2. 首次初始化

首次启动且尚未设置管理员密码时：

1. 打开 `http://127.0.0.1:8787/setup`，直接设置管理员密码（管理员用户名默认为 `ARTEX`），无需安装令牌。
2. 密码设置成功后，首次初始化入口关闭；后续登录使用刚设置的管理员密码。
3. 在「系统 → LLM」配置模型提供方、API key 和模型。公开配置包不包含任何 LLM 配置。

**不要提交或公开** `.env`、`config.json`、`data/`、`jwt.key`、数据库密码和 API key。

## 3. 推荐：Docker Compose 从当前源码启动

在发布包根目录执行一键部署脚本：

```bash
chmod +x deploy.sh
./deploy.sh
# 如已有构建好的镜像、不需要重新构建：
./deploy.sh --no-build
```

首次运行会自动创建权限为 `0600` 的 `.env`，生成 **64 个十六进制字符的随机 PostgreSQL 密码**，随后从当前源码构建并启动服务。脚本同时检查 `/api/health` 和前端首页，失败时输出容器状态与最近日志。凭据默认不打印；确需查看数据库密码时，显式运行 `./deploy.sh --show-secrets`。JWT 密钥由服务自动写入并复用 `data/jwt.key`，不是 `.env` 中可随意替换的变量。

已有 `.env` 不会被覆盖，已有密码也不会因重复执行脚本而重新生成。空密码、常见弱默认值及含 Docker DSN 特殊字符的密码会被拒绝。LLM API Key 可留空，初始化后在 UI 中配置。可用 `./deploy.sh --help` 查看参数。

如需手动部署，也可执行：

```bash
cp .env.example .env
# 编辑 .env，至少填写 POSTGRES_PASSWORD；LLM 可留空后到 UI 配置
$EDITOR .env

docker compose up -d --build
docker compose ps
docker compose logs -f artex
```

Compose 的实际行为：

- PostgreSQL 使用 `postgres:16-alpine`，密码由 `.env` 的 `POSTGRES_PASSWORD` 必填项提供；留空会拒绝启动。`deploy.sh` 仅在首次创建 `.env` 时自动生成随机密码。
- artex 使用 `Dockerfile.source` 从**当前源码**构建，镜像名默认为 `artex-second-dev:local`。
- Web/API 为 `8787`，代理仅绑定 `127.0.0.1:8788`。
- `./data` 挂载到容器 `/app/data`，`./skills` 挂载到 `/app/skills`；删除容器不等于删除数据。
- 不要把服务替换成上游 `autumn27/artex:latest`，否则会得到没有本二开功能的另一套程序。

停止服务但保留数据：

```bash
docker compose down
```

如需连 PostgreSQL 数据卷一起删除，必须另外明确执行 `docker compose down -v`；该操作不可恢复。

## 4. 本地源码构建与运行

### 4.1 直接运行安装脚本

Linux/macOS 下可使用交互菜单：

```bash
chmod +x install.sh
./install.sh
# 选择 2：本地运行（Go 编译）
```

脚本会询问已有 PostgreSQL 或用 Docker 创建 PostgreSQL，生成 `config.json`，构建前端（有 npm 时）并编译运行 `./artex`。它不会替用户配置 LLM，也不会把密钥写入公开配置包。

### 4.2 手动构建

```bash
# 在发布包根目录；Go 版本须满足 go.mod 的 1.26 要求
go version
./build.sh --target linux/amd64

# 默认二进制输出示例（版本/架构不同会改变目录名）
./dist/artex-linux-amd64/artex
```

`build.sh` 默认构建前端并以 `embedui` 标签编译。没有 rsync 时不能使用默认前端同步流程；可先准备 `server/webui/dist`，再按脚本说明使用 `ARTEX_SKIP_FRONTEND=1`。Windows 目标输出为 `artex.exe`。

手动准备数据库时，在程序根目录创建 `config.json`（权限建议 0600）：

```json
{
  "database": {
    "host": "127.0.0.1",
    "port": 5432,
    "user": "artex",
    "password": "<请填写数据库密码>",
    "dbname": "artex",
    "sslmode": "disable"
  },
  "skill_dir": "./skills"
}
```

也可设置 `ARTEX_PG_DSN`。数据库账号至少需要连接、建表和读写本应用数据库的权限；生产环境不要把超级用户密码交给应用。

## 5. Linux 服务器二进制部署

### 5.1 生成部署包

在开发机构建 Linux 目标：

```bash
./build.sh --target linux/amd64
ls -l dist/artex-*.deploy.tar.gz dist/artex-*.deploy.tar.gz.sha256
```

单目标非 Windows 构建会生成包含 `artex`、`start.sh`、`install.sh`、`config.example.json`、`skills/` 和 `scripts/` 的部署包；包内是刚编译的二进制，不是源码占位文件。将包和 SHA256 文件通过安全渠道传到服务器，先校验：

```bash
sha256sum -c artex-*.deploy.tar.gz.sha256
```

### 5.2 使用 install.sh --server

在 Ubuntu/Debian、root、已有 systemd 和 PostgreSQL 安装条件满足时：

```bash
# 在包含 install.sh 和部署包的目录执行；也可传入包的绝对路径
sudo bash install.sh --server ./artex-<版本>-linux-amd64.deploy.tar.gz
```

脚本实际会做：

- 安装/检查 PostgreSQL、curl、tar、python3、logrotate 等依赖。
- 创建或复用 `artex` 数据库和账号，密码写入 `/opt/artex/config.json`，不打印密码；升级前用 `pg_dump` 在 `/opt/artex-backups/` 留下数据库备份。
- 安装到 `/opt/artex`，保留已有 `data/`、`config.json` 和数据库；旧二进制最多保留 5 份备份，失败时恢复旧二进制和技能目录并尝试回拉服务。
- 写入 `/etc/logrotate.d/artex`，`daily`、`rotate 7`、`copytruncate`。
- **全新部署不创建 systemd 服务**；已有 `artex.service` 才会按原配置停启升级，并检查 systemd 状态、`/api/health` 和前端首页。非默认端口用 `ARTEX_HEALTH_URL=http://127.0.0.1:<端口>` 指定。

全新部署后按脚本提示启动：

```bash
cd /opt/artex
nohup ./start.sh >> /opt/artex/artex.log 2>&1 &
```

需要开机自启时自行编写并审核 systemd unit；不要把“脚本不创建服务”误解为已经配置好开机启动。

### 5.3 自定义监听地址

程序默认 `-addr :8787`、`-proxy 127.0.0.1:8788`。例如只让本机反向代理访问 Web：

```bash
./artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

修改监听地址时，同时调整反向代理、健康检查和防火墙规则；不要照搬历史实例的端口。

## 6. 升级与回滚

### Docker 源码部署

```bash
# 在已更新的发布包根目录
./update.sh
# 或等价地：docker compose up -d --build artex
```

`update.sh` 调用统一的 `deploy.sh` 从当前源码重建并重启 artex，执行完整健康检查和失败诊断。数据库和 `./data` 按 Compose 卷配置保留；升级前仍应备份数据库和数据目录。

### 二进制部署

重新执行 `sudo bash install.sh --server <新部署包>`，保留配置/数据并备份旧二进制。数据库 schema 不会因为有旧二进制备份就自动回退；需要回滚时同时评估数据库兼容性。

### 页面自动更新的边界

本发布包的 `selfupdate` 没有配置更新源，页面检查会明确提示“未配置自动更新源，请使用源码构建升级”，不会查询或下载上游版本。不要把历史版本“页面一键更新”的说明当成本发布包能力；如需启用，必须在代码中明确配置自己的 fork、重新构建并重新验证签名/回滚流程。

## 7. 生产安全清单

- Web/API 默认可能监听所有网卡；用 `127.0.0.1`、防火墙和安全组限制来源，前置 HTTPS 反向代理。
- 8788 代理只绑定回环；不要把代理端口或 CA 私钥公开。
- `.env`、`config.json`、`data/`、`jwt.key`、数据库卷和 LLM key 都按秘密处理，备份时加密并限制权限。
- 使用 PostgreSQL 原生备份，并同时备份 `data/`、`skills/` 和必要配置；恢复后在隔离环境验证。
- 日志默认由应用写入；服务器安装脚本配置 `daily/rotate 7`。磁盘、日志和任务并发应按实际环境监控。

## 8. 验收命令

```bash
# Docker
docker compose ps
curl -fsS http://127.0.0.1:8787/ >/dev/null

# 源码/二进制
./artex -h

# 更新检查应为“未配置自动更新源”，而不是下载上游包
# 在 UI「系统 → 设置 → 版本与更新」查看提示

# 备份前确认没有把秘密纳入公开包
git status --short
```

前端/后端发布前验证：

```bash
(cd web && npm ci && npm run build)
go test ./selfupdate ./guard ./intercept ./agent ./cmd/artex
go test ./server -run '^$'
go vet ./...
```

需要 PostgreSQL 的集成测试必须先准备测试数据库并设置 `ARTEX_PG_DSN`；没有数据库时不要把相关跳过或连接失败报成“全量测试通过”。多个 Go 包共用同一个测试库时使用 `go test -p=1 ./...`，避免跨包并行清理或修改同一批数据。

部署脚本可运行离线回归：

```bash
bash scripts/test-deploy-scripts.sh
```

该测试使用 fake docker/curl/go/npm/psql 检查参数、失败分支和文件生成，**不替代真实 Docker Compose、PostgreSQL 或生产服务验收**；发布前仍须完成上面的数据库集成测试和对应部署方式的健康检查。

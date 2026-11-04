# 部署手册

三种方式选一，部署后都做：**/setup 设管理员密码 → 系统 → LLM 配模型**。
可选：导入编排配置 `./start.sh import-config ./config-export`。

## A. Docker（推荐）

```bash
./deploy.sh                 # 首次：自动建 .env（随机 PG 密码）→ 构建 → 启动 → 健康检查
./deploy.sh --no-build      # 已有镜像
```

- Web `:8787`，MITM 代理只绑回环 `127.0.0.1:8788`
- 数据在 `./data`、`./skills` 卷里，`down` 不删，`down -v` 删且不可恢复
- 不要换成上游 `autumn27/artex:latest`

## B. 服务器二进制（Ubuntu/Debian）

开发机构建：

```bash
./build.sh --target linux/amd64     # 产出 dist/*.deploy.tar.gz + .sha256
```

> Windows 上构建：先 `cd web && NEXT_EXPORT=1 npx next build`，`cp -r web/out server/webui/dist`，
> 再 `ARTEX_SKIP_FRONTEND=1 bash build.sh --target linux/amd64`（npm.cmd 不认 Unix 环境变量语法，rsync 常缺失）。

服务器安装（root）：

```bash
sha256sum -c *.deploy.tar.gz.sha256
sudo bash install.sh --server ./*.deploy.tar.gz
cd /opt/artex
setsid nohup ./start.sh >> artex.log 2>&1 < /dev/null &   # 守护启动
curl -fsS http://127.0.0.1:8787/ && echo OK
```

安装脚本自动装 PostgreSQL、建库、备份旧数据，装到 `/opt/artex`；**不建 systemd 服务**，
开机自启需自建 unit。

## C. 本地源码

```bash
./build.sh                                   # 需要 Go 1.26+、Node.js 20+
./dist/artex-linux-amd64/artex               # 前台运行，Ctrl-C 退出
```

数据库走环境变量（或按 `config.example.json` 写 `config.json`）：

```bash
export ARTEX_PG_DSN='postgres://artex:密码@127.0.0.1:5432/artex?sslmode=disable'
```

本地开发（前后端热更）用 `./dev.sh`。

## 常用维护

| 事项 | 命令 |
|---|---|
| 升级（Docker） | `./update.sh` |
| 升级（二进制） | 重跑 `install.sh --server <新包>`（自动备份） |
| 配置迁移 | 见《换环境迁移》，`export-config` / `import-config`（需 `ARTEX_DB_URL`） |
| 忘记密码 | `reset-password.sh` |
| 看日志 | `/opt/artex/artex.log`（已配 logrotate，留 7 天） |

## 排障

| 现象 | 处理 |
|---|---|
| 启动循环刷 `bind: address already in use` | 8787/8788 被占：`ss -tlnp \| grep 8787` 找到旧进程，按 PID 停掉（勿用朴素 `pkill -f`，会杀掉自身 ssh） |
| 迁移命令报 `role "root" does not exist` | 没设 `ARTEX_DB_URL`，见《换环境迁移》 |
| 已有 PG 集群上装 | 注意：脚本会重置 `artex` 角色密码，共用该角色的其他应用会断 |

## 安全底线

- Web 默认绑所有网卡：限制来源 + 反代 + TLS 后再对外
- `.env`、`config.json`、`data/`、`jwt.key`、LLM key 按秘密管理，不进仓库

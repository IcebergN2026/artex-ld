# artex-ld

当前版本：**0.1.7**。

基于 **ARTEX v0.3.15+** 的二次开发版本，面向合法授权的安全测试与攻防实验。保留自主渗透、探索图、planner-worker、MCP、Skill 和报告能力，并增强任务场景、意图审核、自定义 Agent 与漏洞后续编排。

> ⚠️ 仅限合法授权、自有系统与隔离靶场。审核、拦截和人工确认只是辅助控制，不能替代授权边界、目标隔离与操作者判断。

## 核心能力

- **场景化任务**：内置外网渗透、内网渗透、权限提升、漏洞利用、漏洞挖掘和代码审计场景，支持 planner 模板与种子意图。
- **可控审核**：支持待审意图、AI 预审、人工批准或驳回、对话修订与指定执行器。
- **自定义 Agent**：可配置提示词、工具、Skill、MCP 和运行预算，执行种子或普通意图。
- **漏洞驱动编排**：Finding 可触发报告、Agent 派发和子任务，并支持手动复测与痕迹清理。
- **资产与运维**：提供内外网资产分区、命令拦截、日志、配置迁移和一键更新。

## 快速开始

### Docker Compose

```bash
chmod +x deploy.sh
./deploy.sh
```

部署脚本使用 `Dockerfile.source` 从当前源码构建。首次运行会创建 `.env` 并生成随机 PostgreSQL 密码，已有配置不会被覆盖。

访问 `http://localhost:8787/setup` 设置管理员密码，然后在 UI 中配置 LLM。

### 本地或服务器

```bash
chmod +x build.sh install.sh
./build.sh
./install.sh
```

详细依赖、离线构建、升级和反向代理配置见[部署手册](docs/ARTEX-部署手册.md)。

## 文档

- [完整项目说明](docs/README.md)：版本亮点、功能细节、使用案例与配置迁移。
- [二开功能详解](docs/ARTEX-二开功能详解.md)
- [部署手册](docs/ARTEX-部署手册.md)
- [架构说明](ARCHITECTURE.md)
- [更新日志](CHANGELOG.md)

## 许可

[AGPL v3](LICENSE)，遵循上游许可。二次开发组件与上游第三方组件的版权及许可义务不因修改而消失。

package agent

import "strings"

// WorkerDomainSeed is a built-in, fully materialized Worker prompt for one scenario.
type WorkerDomainSeed struct {
	NKey string `json:"nkey"`
	Body string `json:"body"`
}

// WorkerDomainSeeds keeps the common Worker safety/evidence rules in one place.
// The stored template is still a complete prompt, so it remains editable offline.
func WorkerDomainSeeds() []WorkerDomainSeed {
	sections := map[string]string{
		"内网渗透": `## 当前场景：内网渗透
- 从父任务登记的立足点开始，识别主机、网卡、路由、凭据和可达关系，再决定后续利用。
- 每次横向动作都写清源主机、目标主机、协议和证据来源。
- 发现其他网卡、路由、网段或存活服务时，可依据当前意图继续探测和验证，并记录事实与证据。
- 发现可复用凭据、配置或拓扑事实时 record_fact；关键 HTTP 和代理流量沿用 stream_id 与 tool_call_id。
- 确认网卡、路由、容器网络或会话连接后，先 record_fact 保存证据，再调用 record_topology_relation(parent_ip, node_ip, label, evidence_ref=fact:<返回的节点 id>)；label 按实际关系自定义。找不到证据就保持独立节点，禁止因同任务、同网段或存在入口而猜测连线。
- 获得新主机控制权后，按实际证据登记 getshell、webshell 或获取目标权限，不把端口开放当作立足点。`,
		"权限提升": `## 当前场景：权限提升
- 从已验证的低权限上下文开始，先记录当前用户、主机、权限边界和现有凭据，再检查配置、令牌、服务和已知提权路径。
- 优先可读、可回滚、低噪声的验证；禁止破坏数据、拒绝服务、关闭审计或留下持久化后门。
- 每次权限变化都用原上下文与新上下文的对照证据证明。只读取到敏感配置但没有获得权限时，登记信息泄露而不是获取目标权限。
- 实际获得更高权限账户或命令执行上下文后登记获取目标权限；另有反弹或命令 Shell 时按 getshell 登记。`,
		"漏洞利用": `## 当前场景：漏洞利用
- 先固定目标、漏洞前提和受影响组件，再设计最小化 PoC。逐步记录请求、工具、版本和观察结果。
- 先证明可控性与影响，再考虑扩大利用。获得所需证据后停止，不为了展示效果破坏数据或扩大影响。
- 上传 Web 后门必须验证可访问或可连接，并遵守 godzilla 登记纪律；只有文件写入成功不能登记 webshell。
- 每次利用结果都关联原始流量或工具调用；失败时保留排除证据，不把扫描器告警直接写成漏洞。`,
		"漏洞挖掘": `## 当前场景：漏洞挖掘
- 围绕当前目标提出少量可证伪假设，逐个改变输入、身份、状态或协议条件，并保留对照结果。
- 优先覆盖同类输入面、相邻接口和不同角色，不偏离当前目标做无边界的全网扫描。
- 对候选漏洞先排除缓存、错误配置、前端展示和偶然差异，再确认可复现性与实际影响。
- 只有验证完成的事实才进入 report_finding；研究过程、排除路径和待确认假设使用 record_fact。`,
		"代码审计": `## 当前场景：代码审计
- 先识别入口、数据来源、敏感 sink、鉴权与过滤链路，再沿真实调用关系证明可达性；不因关键词命中就报告漏洞。
- 记录文件、函数、关键语句和控制流。能用运行时验证时，继续给出最小化验证和对应流量；不能运行时验证时明确证据缺口。
- 区分源到 sink 的理论路径、可利用路径和已验证漏洞。只有满足任务证据门槛的结果才登记 finding，其余写审计事实。
- 不修改目标源码，不执行构建脚本中的未知命令；需要运行测试或动态验证时先遵守任务授权和 Worker 证据纪律。`,
	}
	order := []string{"内网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"}
	seeds := make([]WorkerDomainSeed, 0, len(order))
	for _, name := range order {
		body := strings.Replace(workerDefaultTmpl, "**边发现边写回**", sections[name]+"\n\n**边发现边写回**", 1)
		seeds = append(seeds, WorkerDomainSeed{NKey: name, Body: body})
	}
	return seeds
}

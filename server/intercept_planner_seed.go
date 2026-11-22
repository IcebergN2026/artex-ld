// intercept_planner_seed.go：planner 拦截规则的 DB 种子。
// 主要函数/类型：
//   - seedPlannerInterceptRules：启动时幂等播种 8 条 planner 正则规则（标签 planner拦截）。
//
// 历史：planner 内置拦截规则曾是代码占有的 Shell AST 结构判定（guard/planner_bash_policy.go，
// 已删除）。2026-11-21 按用户拍板全部改为普通 DB 正则规则：在「系统 → 命令拦截」页
// 与其他规则同构管理（停用/编辑/删除/标签），不再有任何代码层特殊判定。
// 命名：不带 [内置] 前缀，统一打 plannerInterceptTag（planner拦截）标签分组；
// 旧库里的 [内置] planner … 行在启动时幂等改名并补标签，其余字段原样保留。
//
// 形态变化带来的语义差异（有意为之，勿"修复"回代码层）：
//   - 匹配对象是工具入参原始 JSON 文本，不是解析后的命令 AST；引号切分/变量拼接
//     等混淆可以绕过正则，这是正则形态的固有代价。
//   - 网络工具名单中的 http/https（httpie 别名）未纳入正则——\bhttp(s)?\b 会命中
//     所有 URL 字面量，误伤过大；httpie 本体保留。
//   - 原 parse_error（Bash 参数 JSON 解析失败 fail-closed）与 windows_unsupported
//     （Windows 整体保守拒绝）无正则等价物，随代码层一并移除：解析失败/Windows 平台
//     的调用现在落通用拦截链（DB 规则 → LLM 判官）。
//   - 动态命令规则只能在 JSON 包装上做近似：命令串以 $ 或反引号开头视为动态命令，
//     命令中段/子命令位置的动态命令名不再识别。
package server

import (
	"log"
	"strings"
)

// plannerInterceptTag 是 8 条 planner 种子规则的统一分组标签（前端按标签筛选）。
const plannerInterceptTag = "planner拦截"

// legacyPlannerRulePrefix 是上一版种子使用的名字前缀，启动时幂等剥掉并补标签。
const legacyPlannerRulePrefix = "[内置] planner "

// plannerInterceptSeedRule 是一条待播种的 planner 内置规则。
type plannerInterceptSeedRule struct {
	name        string
	matchTarget string // tool_input / tool_name
	pattern     string // RE2
	message     string // deny 时返回给模型的原因与指引
}

// legacyPlannerWorkerGuidance 是初版统一处置指引，保留用于存量消息的幂等升级识别。
const legacyPlannerWorkerGuidance = "；不要改写命令、替换网络工具、使用脚本解释器或编码绕过，需要目标操作时请派发 intent 给 worker 执行"

// plannerWorkerGuidance 统一追加的处置指引：旧指引 + 查资料通道（web_search/WebFetch）。
const plannerWorkerGuidance = legacyPlannerWorkerGuidance + "；查询公开资料请改用 web_search/WebFetch，不要用 Bash curl/wget"

// plannerInterceptSeeds 即原 10 条代码规则的正则化（parse_error 与
// windows_unsupported 无正则等价物，见文件头说明）。priority=1000 高于普通
// 用户规则（默认 0，审核确认卡 100），保持「planner 内置规则先行」的判定顺序。
var plannerInterceptSeeds = []plannerInterceptSeedRule{
	{
		name:        "planner 命令拦截：网络工具",
		matchTarget: "tool_input",
		pattern:     `\b(curl|wget|httpie|nc|ncat|netcat|socat|telnet|nmap|masscan|httpx|nuclei|ffuf|gobuster|dirsearch|sqlmap|nikto|ssh|scp|sftp|ftp)\b`,
		message:     "planner 不允许直接发起网络请求/连接/扫描（命中网络工具名单）" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：Shell 包装器",
		matchTarget: "tool_input",
		pattern:     `\b(bash|sh|zsh|dash|ksh|cmd|powershell|pwsh|eval|source|env|sudo|nohup|setsid|stdbuf|busybox)\b`,
		message:     "planner 不允许再进入 Shell 或使用命令包装器" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：脚本解释器",
		matchTarget: "tool_input",
		pattern:     `\b(python3?|perl|php|ruby|node|deno|lua|rscript|awk|gawk|mawk|nawk)\b`,
		message:     "planner 不允许调用可执行任意网络代码的脚本解释器" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：间接执行（find -exec / xargs / tar）",
		matchTarget: "tool_input",
		pattern:     `\bfind\b[^;&|]*-exec(ute)?\b|\bxargs\b|\btar\b[^;&|]*--checkpoint-action`,
		message:     "planner 不允许通过 find/xargs/tar 间接执行命令" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：命令替换",
		matchTarget: "tool_input",
		pattern:     `\$\(|` + "`" + `|<\(|>\(`,
		message:     "planner 不允许在命令中使用命令替换/进程替换" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：网络重定向（/dev/tcp）",
		matchTarget: "tool_input",
		pattern:     `/dev/(tcp|udp)/`,
		message:     "planner 不允许使用 /dev/tcp、/dev/udp 网络通道" + plannerWorkerGuidance,
	},
	{
		name:        "planner 命令拦截：动态命令",
		matchTarget: "tool_input",
		// JSON 入参上命令值以 $ 或反引号开头 → 命令名是展开结果，无法静态归类。
		pattern: "\"command\"\\s*:\\s*\"\\s*[$`]",
		message: "planner 不允许以变量/替换结果作为命令名（动态命令）" + plannerWorkerGuidance,
	},
	{
		name:        "planner 工具拦截：MCP WebShell/远程命令",
		matchTarget: "tool_name",
		pattern:     `(?i)^mcp__((godzilla|antsword|behinder|rebeyond|webshell|shell)__|[^_]+__.*(webshell|shell_create|shell_add|shell_test|shell_exec|shell_connect|shell_list|cmd_exec|exec_cmd|execute_command|run_command|remote_exec|terminal_exec))`,
		message:     "不允许 planner 通过 MCP 工具对目标执行 WebShell 命令或远程命令" + plannerWorkerGuidance,
	},
}

// seedPlannerInterceptRules 幂等播种 planner 内置拦截规则（按 name 查重，与
// seedReviewIntercept 同口径）：用户停用/编辑后不会被覆盖；删除后重启会重新
// 播种（与审核确认卡种子行为一致）。同时清理代码层时代遗留的 settings 键。
func (s *Server) seedPlannerInterceptRules() {
	pg := s.m.pg
	if pg == nil || s.m.interceptor == nil {
		return
	}
	// ① 清理代码层时代的废弃 settings 键（planner_builtin_disabled/hidden/
	// rule_tags/overrides/tags，每次启动执行，幂等）。
	res, err := pg.Exec(`DELETE FROM settings WHERE key LIKE 'planner\_builtin\_%'`)
	if err != nil {
		log.Printf("[planner-seed] 清理废弃 settings 键失败: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[planner-seed] 清理 %d 个代码层时代 planner 规则 settings 键", n)
	}
	// ② 旧名迁移：`[内置] planner …` → `planner …`，并补 planner拦截 标签。
	// 只动 name/tags，用户改过的正则、启停、优先级、agents 等字段原样保留。
	existing, err := pg.ListInterceptRules()
	if err != nil {
		log.Printf("[planner-seed] 读取拦截规则失败: %v", err)
		return
	}
	for idx, r := range existing {
		if !strings.HasPrefix(r.Name, legacyPlannerRulePrefix) {
			continue
		}
		tags := r.Tags
		hasTag := false
		for _, t := range tags {
			if t == plannerInterceptTag {
				hasTag = true
				break
			}
		}
		if !hasTag {
			tags = append(append([]string{}, tags...), plannerInterceptTag)
		}
		newName := strings.TrimPrefix(r.Name, "[内置] ")
		updated, err := pg.UpdateInterceptRule(r.ID, newName, r.MatchTarget, r.MatchType,
			r.Pattern, r.Action, r.Message, r.Priority, r.Enabled,
			r.TimeoutEnabled, r.TimeoutSeconds, r.TimeoutAction, tags, r.Agents)
		if err != nil {
			log.Printf("[planner-seed] 旧规则 %s 改名迁移失败: %v", r.Name, err)
			continue
		}
		existing[idx] = updated
		log.Printf("[planner-seed] 旧规则迁移：%s → %s（标签 %s）", r.Name, updated.Name, plannerInterceptTag)
	}
	// ③ deny 文案升级：消息仍以旧统一指引结尾（=未被用户改过结尾）的 planner
	// 种子行，替换为含查资料通道的新指引；用户自定义过结尾的消息不动。
	seedByName := make(map[string]plannerInterceptSeedRule, len(plannerInterceptSeeds))
	for _, s := range plannerInterceptSeeds {
		seedByName[s.name] = s
	}
	for idx, r := range existing {
		if _, ok := seedByName[r.Name]; !ok || !strings.HasSuffix(r.Message, legacyPlannerWorkerGuidance) {
			continue
		}
		newMsg := strings.TrimSuffix(r.Message, legacyPlannerWorkerGuidance) + plannerWorkerGuidance
		updated, err := pg.UpdateInterceptRule(r.ID, r.Name, r.MatchTarget, r.MatchType,
			r.Pattern, r.Action, newMsg, r.Priority, r.Enabled,
			r.TimeoutEnabled, r.TimeoutSeconds, r.TimeoutAction, r.Tags, r.Agents)
		if err != nil {
			log.Printf("[planner-seed] 规则 %s 文案升级失败: %v", r.Name, err)
			continue
		}
		existing[idx] = updated
		log.Printf("[planner-seed] 规则 %s deny 文案追加查资料通道指引", r.Name)
	}
	// ④ 按 name 查重播种。
	have := make(map[string]bool, len(existing))
	for _, r := range existing {
		have[r.Name] = true
	}
	for _, seed := range plannerInterceptSeeds {
		if have[seed.name] {
			continue
		}
		_, err := pg.CreateInterceptRule(
			seed.name,                     // name
			seed.matchTarget,              // match_target
			"regex",                       // match_type
			seed.pattern,                  // pattern（RE2）
			"deny",                        // action
			seed.message,                  // message
			1000,                          // priority：高于普通用户规则，保持内置先行
			true,                          // enabled
			false,                         // timeout_enabled：deny 规则无审批超时语义
			60,                            // timeout_seconds（占位，不生效）
			"deny",                        // timeout_action（占位，不生效）
			[]string{plannerInterceptTag}, // tags：planner拦截 分组
			[]string{"planner"},           // agents：只对 planner 生效
		)
		if err != nil {
			log.Printf("[planner-seed] 种子拦截规则 %s 失败: %v", seed.name, err)
		} else {
			log.Printf("[planner-seed] planner 拦截规则种子：%s（deny，agents=[planner]）", seed.name)
		}
	}
	s.m.interceptor.Invalidate()
}

// intercept_planner_seed_test.go：planner 内置种子规则的正则可编译性与命中/误伤边界。
// 种子是 deny 规则，坏正则会静默跳过（intercept loadLocked 容错），等于规则消失，
// 因此编译检查是必需项；命中/误伤用例钉住正则化后的语义边界（见种子文件头说明）。
package server

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"artex/db"
	"artex/guard"
	"artex/intercept"
)

func compileSeedPatterns(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	out := make(map[string]*regexp.Regexp, len(plannerInterceptSeeds))
	for _, s := range plannerInterceptSeeds {
		if _, dup := out[s.name]; dup {
			t.Fatalf("种子规则名重复：%s", s.name)
		}
		re, err := regexp.Compile(s.pattern)
		if err != nil {
			t.Fatalf("种子规则 %s 正则不可编译：%v", s.name, err)
		}
		out[s.name] = re
		if strings.Contains(s.name, "[内置]") {
			t.Fatalf("种子规则 %s 不应带 [内置] 前缀（统一用 planner拦截 标签分组）", s.name)
		}
		if !strings.Contains(s.message, "web_search/WebFetch") {
			t.Fatalf("种子规则 %s 缺少查资料通道指引", s.name)
		}
		if !strings.Contains(s.message, "派发 intent 给 worker") {
			t.Fatalf("种子规则 %s 缺少派发 worker 指引", s.name)
		}
	}
	return out
}

func TestPlannerSeedPatternsCompile(t *testing.T) {
	compileSeedPatterns(t)
}

// subjectFor 模拟 Interceptor 的取词：tool_input 规则看入参 JSON，tool_name 规则看工具名。
func TestPlannerSeedMatchBoundaries(t *testing.T) {
	rules := compileSeedPatterns(t)
	cases := []struct {
		rule    string // 规则名子串
		subject string
		want    bool
	}{
		// 网络工具
		{"网络工具", `{"command":"curl http://10.0.0.1/"}`, true},
		{"网络工具", `{"command":"nmap -sV 10.0.0.1"}`, true},
		{"网络工具", `{"command":"cat /etc/passwd"}`, false},
		// http/https 未纳入名单（URL 误伤），含 URL 字面量的命令不得命中
		{"网络工具", `{"command":"echo http://10.0.0.1/"}`, false},
		// Shell 包装器
		{"Shell 包装器", `{"command":"bash -c id"}`, true},
		{"Shell 包装器", `{"command":"ls -la"}`, false},
		// 脚本解释器
		{"脚本解释器", `{"command":"python3 -c 'import socket'"}`, true},
		{"脚本解释器", `{"command":"awk '{print $1}' /tmp/x"}`, true},
		{"脚本解释器", `{"command":"cat notes.txt"}`, false},
		// 间接执行
		{"间接执行", `{"command":"find / -name *.log -exec cat {} ;"}`, true},
		{"间接执行", `{"command":"cat urls.txt | xargs -n1 fetch"}`, true},
		{"间接执行", `{"command":"tar -cf a.tar --checkpoint-action=exec=sh x"}`, true},
		{"间接执行", `{"command":"find / -name *.log"}`, false},
		// 命令替换
		{"命令替换", `{"command":"echo $(id)"}`, true},
		{"命令替换", "{\"command\":\"echo `id`\"}", true},
		{"命令替换", `{"command":"echo hello"}`, false},
		// 网络重定向
		{"网络重定向", `{"command":"exec 3<>/dev/tcp/10.0.0.1/80"}`, true},
		{"网络重定向", `{"command":"ls /dev/null"}`, false},
		// 动态命令（JSON 近似：命令值以 $ 或反引号开头）
		{"动态命令", `{"command":"$TOOL -h"}`, true},
		{"动态命令", "{\"command\":\"`which curl` -s\"}", true},
		{"动态命令", `{"command":"ls"}`, false},
		// MCP WebShell（tool_name）
		{"MCP WebShell", "mcp__godzilla__shell_exec", true},
		{"MCP WebShell", "mcp__target__execute_command", true},
		{"MCP WebShell", "add_intent", false},
	}
	for _, c := range cases {
		var re *regexp.Regexp
		for name, r := range rules {
			if strings.Contains(name, c.rule) {
				re = r
				break
			}
		}
		if re == nil {
			t.Fatalf("找不到规则：%s", c.rule)
		}
		if got := re.MatchString(c.subject); got != c.want {
			t.Errorf("%s 对 %q 命中=%v，期望 %v", c.rule, c.subject, got, c.want)
		}
	}
}

// TestPlannerSeedRulesLiveMatch 走真实 DB + Interceptor.Match 全链路：8 条 planner
// 规则必须在 planner 身份下逐条 deny，且在 worker 身份（含 "work#1 · #99" lane 标签）
// 下全部不命中。ARTEX_PG_DSN 未设置时跳过；库里缺哪条就临时补种哪条，结束清理。
func TestPlannerSeedRulesLiveMatch(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN is not set")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pg.Close()

	existing, err := pg.ListInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]db.InterceptRule{}
	for _, r := range existing {
		byName[r.Name] = r
	}
	var created []int64
	defer func() {
		for _, id := range created {
			_, _ = pg.Exec(`DELETE FROM intercept_rules WHERE id=$1`, id)
		}
	}()
	for _, s := range plannerInterceptSeeds {
		if _, ok := byName[s.name]; ok {
			continue
		}
		r, err := pg.CreateInterceptRule(s.name, s.matchTarget, "regex", s.pattern, "deny",
			s.message, 1000, true, false, 60, "deny", []string{plannerInterceptTag}, []string{"planner"})
		if err != nil {
			t.Fatalf("补种 %s 失败: %v", s.name, err)
		}
		created = append(created, r.ID)
	}

	ic := intercept.New(pg)
	ic.Invalidate()
	plannerCtx := intercept.WithTaskContext(context.Background(), "1", "planner", nil)
	workerCtx := intercept.WithTaskContext(context.Background(), "1", "work#1 · #99", nil)

	cases := []struct {
		rule     string // 种子规则名（全名）
		tool     string
		input    string
		wantDeny bool // planner 身份下期望命中 deny
	}{
		{"planner 命令拦截：网络工具", "Bash", `{"command":"curl http://127.0.0.1:5000/"}`, true},
		{"planner 命令拦截：Shell 包装器", "Bash", `{"command":"bash -c id"}`, true},
		{"planner 命令拦截：脚本解释器", "Bash", `{"command":"python3 -c 'print(1)'"}`, true},
		{"planner 命令拦截：间接执行（find -exec / xargs / tar）", "Bash", `{"command":"cat urls.txt | xargs -n1 echo"}`, true},
		{"planner 命令拦截：命令替换", "Bash", `{"command":"echo $(id)"}`, true},
		{"planner 命令拦截：网络重定向（/dev/tcp）", "Bash", `{"command":"exec 3<>/dev/tcp/127.0.0.1/80"}`, true},
		{"planner 命令拦截：动态命令", "Bash", `{"command":"$TOOL -h"}`, true},
		{"planner 工具拦截：MCP WebShell/远程命令", "mcp__godzilla__shell_exec", `{}`, true},
	}
	for _, c := range cases {
		dec, matched := ic.Match(plannerCtx, c.tool, []byte(c.input))
		if !matched || dec.Action != "deny" || dec.RuleName != c.rule {
			t.Errorf("planner 调 %s %s：命中=%v 规则=%q 动作=%q，期望被 %q deny",
				c.tool, c.input, matched, dec.RuleName, dec.Action, c.rule)
		}
		if dec2, matched2 := ic.Match(workerCtx, c.tool, []byte(c.input)); matched2 && dec2.RuleName == c.rule {
			t.Errorf("worker 身份不应命中 planner 规则 %q（实际 %q/%q）", c.rule, dec2.RuleName, dec2.Action)
		}
	}

	// Guard 全链路补验：MCP 规则必须能拦住 ExecuteExtraTool 包装调用（真实调用面）。
	// 包装器需在启用集内；临时补齐，结束恢复原设置。
	enabled, err := ic.GetEnabledTools()
	if err != nil {
		t.Fatal(err)
	}
	hasExec := false
	for _, n := range enabled {
		if n == "ExecuteExtraTool" {
			hasExec = true
		}
	}
	if !hasExec {
		if err := ic.SetEnabledTools(append(append([]string{}, enabled...), "ExecuteExtraTool")); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ic.SetEnabledTools(enabled) }()
	}
	g := guard.NewWithInterceptor(ic)
	blocked, _, _ := g.Hooks().PreToolUse(plannerCtx, "ExecuteExtraTool",
		json.RawMessage(`{"tool_name":"mcp__godzilla__shell_exec","params":{"cmd":"id"}}`))
	if !blocked {
		t.Error("planner 经 ExecuteExtraTool 调 mcp__godzilla__shell_exec 应被 Guard 拦截")
	}
}

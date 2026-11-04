package agent

import (
	"strings"
	"testing"
)

func TestWorkerPromptClassifiesVerifiedFootholds(t *testing.T) {
	for _, want := range []string{
		"getshell=已通过反弹 shell",
		"webshell=Webshell 已上传且可访问或连接控制",
		"获取目标权限=已获得目标系统或应用权限",
		"有回显才按 webshell 记 finding",
	} {
		if !strings.Contains(workerDefaultTmpl, want) {
			t.Fatalf("worker prompt missing %q", want)
		}
	}
}

func TestWorkerDomainSeeds(t *testing.T) {
	seeds := WorkerDomainSeeds()
	wantNames := []string{"内网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"}
	wantRules := []string{"继续探测和验证", "权限变化", "最小化 PoC", "可证伪假设", "敏感 sink"}
	if len(seeds) != len(wantNames) {
		t.Fatalf("seed count=%d, want %d", len(seeds), len(wantNames))
	}
	for i, seed := range seeds {
		if seed.NKey != wantNames[i] {
			t.Errorf("seed[%d] nkey=%q, want %q", i, seed.NKey, wantNames[i])
		}
		for _, want := range []string{"## 当前场景：" + seed.NKey, wantRules[i], "report_finding", "**边发现边写回**"} {
			if !strings.Contains(seed.Body, want) {
				t.Errorf("%s missing %q", seed.NKey, want)
			}
		}
		if seed.Body == workerDefaultTmpl {
			t.Errorf("%s did not add its scenario layer", seed.NKey)
		}
	}
}

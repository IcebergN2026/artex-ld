package agent

import (
	"strings"
	"testing"

	"artex/db"
)

// TestConstraintBlockFromStore exercises the real task_constraints read path used
// by planner/worker before every round. Skipped when no test database is wired.
func TestConstraintBlockFromStore(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("约束注入测试", "验证操作约束进入系统提示")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	ts := d.Exploration(expID)
	if _, err := ts.AddConstraint("deny", "禁止测试 192.0.2.0/24 其他地址", "test"); err != nil {
		t.Fatal(err)
	}
	block := constraintBlock(ts)
	if !strings.Contains(block, "禁止测试 192.0.2.0/24 其他地址") {
		t.Fatalf("constraint block missing stored deny rule: %q", block)
	}
	if !strings.Contains(block, "不得为其派生意图或执行动作") {
		t.Fatalf("constraint block missing out-of-scope contract: %q", block)
	}
}

func TestConstraintBlockNilStore(t *testing.T) {
	if got := constraintBlock(nil); got != "" {
		t.Fatalf("nil store must render no constraint block, got %q", got)
	}
}

func TestRenderConstraintBlockEmpty(t *testing.T) {
	for _, rows := range [][]db.Constraint{
		nil,
		{},
		{{Kind: "allow", Text: "   "}, {Kind: "deny", Text: ""}},
	} {
		if got := renderConstraintBlock(rows); got != "" {
			t.Fatalf("empty/blank constraints must render no block, got %q", got)
		}
	}
}

func TestRenderConstraintBlockAllowAndDeny(t *testing.T) {
	got := renderConstraintBlock([]db.Constraint{
		{Kind: "allow", Text: "仅测试 192.0.2.10 及其 443 端口"},
		{Kind: "deny", Text: "禁止扫描 192.0.2.0/24 其他地址"},
	})
	for _, want := range []string{
		"【操作约束（最高优先级",
		"允许的操作：\n- 仅测试 192.0.2.10 及其 443 端口",
		"禁止的操作：\n- 禁止扫描 192.0.2.0/24 其他地址",
		"不得为其派生意图或执行动作",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("constraint block missing %q:\n%s", want, got)
		}
	}
}

func TestRenderConstraintBlockSingleKind(t *testing.T) {
	denyOnly := renderConstraintBlock([]db.Constraint{{Kind: "deny", Text: "禁止内网渗透"}})
	if strings.Contains(denyOnly, "允许的操作") {
		t.Errorf("deny-only block must not claim an allow section:\n%s", denyOnly)
	}
	if !strings.Contains(denyOnly, "- 禁止内网渗透") {
		t.Errorf("deny constraint missing:\n%s", denyOnly)
	}

	allowOnly := renderConstraintBlock([]db.Constraint{{Kind: "allow", Text: "只允许被动侦察"}})
	if strings.Contains(allowOnly, "禁止的操作") {
		t.Errorf("allow-only block must not claim a deny section:\n%s", allowOnly)
	}
	if !strings.Contains(allowOnly, "- 只允许被动侦察") {
		t.Errorf("allow constraint missing:\n%s", allowOnly)
	}
}

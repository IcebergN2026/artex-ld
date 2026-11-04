package agent

// ═══ ARTEX 二开 · [P-TPL·DOMAIN 五领域模板逐字节快照测试] ═══
// golden（testdata/planner_domain_templates_golden.txt）捕获于公共段常量化重构【之前】：
// 任何对五套模板正文的改动（含公共段常量与领域差异段）都会在这里以逐字节 diff 现形。
// 更新 golden 仅在刻意变更模板文案时进行，并须在评审中明示 diff。

import (
	"os"
	"strings"
	"testing"
)

// goldenSeparator 分隔 golden 文件中各模板条目（名称行 + 正文）。
const goldenSeparator = "\n=====TEMPLATE=====\n"

// TestPlannerDomainTemplatesByteExact 五套领域模板正文与改前 golden 逐字节一致。
func TestPlannerDomainTemplatesByteExact(t *testing.T) {
	raw, err := os.ReadFile("testdata/planner_domain_templates_golden.txt")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	entries := strings.Split(string(raw), goldenSeparator)
	// 首段为空（文件以分隔符开头）。
	want := map[string]string{}
	for _, e := range entries {
		e = strings.TrimPrefix(e, "\n")
		if e == "" {
			continue
		}
		nl := strings.IndexByte(e, '\n')
		if nl < 0 {
			t.Fatalf("malformed golden entry: %q", e[:min(40, len(e))])
		}
		want[e[:nl]] = e[nl+1:]
	}
	seeds := PlannerDomainSeeds()
	if len(seeds) != len(want) {
		t.Fatalf("golden has %d templates, PlannerDomainSeeds has %d", len(want), len(seeds))
	}
	for name, body := range seeds {
		golden, ok := want[name]
		if !ok {
			t.Errorf("golden missing template %q", name)
			continue
		}
		if body != golden {
			t.Errorf("%s: body differs from pre-refactor golden (got %d bytes, want %d)", name, len(body), len(golden))
			// 定位首个差异，便于评审。
			gb, bb := []byte(golden), []byte(body)
			n := min(len(gb), len(bb))
			for i := range n {
				if gb[i] != bb[i] {
					lo := max(0, i-40)
					t.Errorf("first diff at byte %d:\ngolden: %q\ngot:    %q", i, gb[lo:min(i+40, len(gb))], bb[lo:min(i+40, len(bb))])
					break
				}
			}
		}
	}
}

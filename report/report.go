// report.go：渲染可交付的渗透测试 Markdown 报告（任务目标、摘要、确认发现与证据）。
// 主要函数/类型：
//   - Input / findingView：报告输入与发现的渲染视图数据结构。
//   - Markdown：根据 Input 渲染完整渗透测试报告 Markdown。
//   - parseFinding / nz：把图节点解析为发现视图并提供空值兜底。
//

// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

// severityKey canonicalizes a stored severity for ordering and summary counts.
// Historical rows may predate write-time normalization (for example "HIGH" or
// " high ").
func severityKey(severity string) string {
	return strings.ToLower(strings.TrimSpace(severity))
}

// severityRank orders severities from most to least severe. Anything unrecognized,
// including empty and legacy free-form values, deliberately sorts last: a missing
// lookup must never make an unknown severity rank ahead of critical.
func severityRank(severity string) int {
	switch severityKey(severity) {
	case db.SeverityCritical:
		return 0
	case db.SeverityHigh:
		return 1
	case db.SeverityMedium:
		return 2
	case db.SeverityLow:
		return 3
	default:
		return 4
	}
}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 摘要\n\n")
	fmt.Fprintf(&b, "- 确认发现：**%d** 个\n", len(in.Findings))
	fmt.Fprintf(&b, "- 资产：")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## 发现\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_本次未确认漏洞。_\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n))
		}
		sort.SliceStable(fs, func(i, j int) bool { return severityRank(fs[i].Severity) < severityRank(fs[j].Severity) })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

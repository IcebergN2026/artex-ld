package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const defaultMaxOutput = 30000

func maxOut(tc *ToolContext) int {
	if tc != nil && tc.MaxOutputChars > 0 {
		return tc.MaxOutputChars
	}
	return defaultMaxOutput
}

var spillSeq atomic.Int64

// Capture bounds a tool's textual output for the model. When the output exceeds
// the limit (ToolContext.MaxOutputChars, default 30000) and the host configured
// an OutputDir, the FULL output is written to a file there and a tight head + a
// pointer to that file is returned — so the model can read slices on demand
// (grep/sed) instead of losing the overflow. Without an OutputDir it falls back
// to a head+tail truncation (the overflow is discarded). Tools that produce
// potentially large output should return Capture(tc, output) rather than the raw
// string.
func Capture(tc *ToolContext, s string) string {
	max := maxOut(tc)
	if len(s) <= max {
		return s
	}
	if tc != nil && tc.OutputDir != "" {
		if path, err := spillOutput(tc.OutputDir, s); err == nil {
			ref := path
			if tc.WorkingDir != "" {
				if rel, e := filepath.Rel(tc.WorkingDir, path); e == nil && !strings.HasPrefix(rel, "..") {
					ref = rel
				}
			}
			lines := strings.Count(s, "\n") + 1
			return s[:max] + fmt.Sprintf(
				"\n\n... <persisted-output>[Output too large: full %d bytes / %d lines.Full output saved to  %s </persisted-output>",
				len(s), lines, ref)
		}
	}
	return truncate(s, max)
}

// alreadyCaptured reports whether s already carries a marker Capture leaves
// behind — a spill pointer or a head+tail truncation notice. It is how a global
// post-tool net recognises output a tool already ran through Capture itself.
//
// A false negative (legitimate text that happens to contain a marker) only means
// the guard fails to re-cap already-large output; it never corrupts. The markers
// are distinctive enough that this is vanishingly rare.
func alreadyCaptured(s string) bool {
	return strings.Contains(s, "<persisted-output>") || strings.Contains(s, "characters truncated]")
}

// CaptureOnce is Capture with an idempotency guard: output a tool already ran
// through Capture (so it carries a spill pointer or truncation notice) is
// returned unchanged. This lets a single post-tool net cap every tool's output
// without re-truncating or re-spilling what built-in / MCP / custom tools
// already handled themselves. Output within the limit is a no-op either way,
// since Capture returns it unchanged.
func CaptureOnce(tc *ToolContext, s string) string {
	if alreadyCaptured(s) {
		return s
	}
	return Capture(tc, s)
}

func spillOutput(dir, s string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("output-%d-%d.txt", time.Now().Unix(), spillSeq.Add(1))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// resolvePath joins a relative path against the working directory. When
// tc.Root is set the result is confined to that tree: absolute paths outside
// it and ../ escapes are rewritten to an impossible sentinel path so the
// caller's os.* call fails with a clear not-found error instead of touching
// files outside the sandbox.
func resolvePath(tc *ToolContext, p string) string {
	if !filepath.IsAbs(p) && tc != nil && tc.WorkingDir != "" {
		p = filepath.Join(tc.WorkingDir, p)
	}
	if tc == nil || tc.Root == "" {
		return p
	}
	root, err := filepath.Abs(tc.Root)
	if err != nil {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return p
	}
	return filepath.Join(root, "..escape-denied-")
}

// truncate trims s to max characters, keeping head and tail.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + fmt.Sprintf("\n\n... [%d characters truncated] ...\n\n", len(s)-max) + s[len(s)-half:]
}

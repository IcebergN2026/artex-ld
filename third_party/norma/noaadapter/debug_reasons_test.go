package noaadapter

import (
	"strings"
	"testing"
)

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestDebugReasons128k(t *testing.T) {
	sess := simSession(t, 128_000)
	a := newSimAgent(t, sess, 1)
	for i := range 182 {
		a.work(4000)
		view := sess.View(a.history)
		if nudge := findNudge(view); nudge != "" {
			t.Logf("turn %d: nudge=%q", i, firstLine(nudge))
		}
		a.observe()
	}
	byTier := map[int]int{}
	for _, b := range sess.State().Blocks {
		if b.Active {
			byTier[int(b.Tier)]++
		}
	}
	t.Logf("blocks by tier: %v", byTier)
}

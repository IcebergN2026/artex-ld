package server

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestTaskWorkspaceTargets 前缀边界与缺失目录语义：
// exp12- 只命中 exploration 12 的前缀项，绝不匹配 exp123-；tasks 目录缺失时仅返回 transcripts 项。
func TestTaskWorkspaceTargets(t *testing.T) {
	dataDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dataDir, "tasks", "9"))
	mustMkdirAll(t, filepath.Join(dataDir, "transcripts", "exp12-a"))
	mustMkdirAll(t, filepath.Join(dataDir, "transcripts", "exp12-b"))
	mustMkdirAll(t, filepath.Join(dataDir, "transcripts", "exp123-other"))
	mustWrite(t, filepath.Join(dataDir, "transcripts", "exp12-c.jsonl"))
	mustWrite(t, filepath.Join(dataDir, "transcripts", "exp123-d.jsonl"))
	mustWrite(t, filepath.Join(dataDir, "transcripts", "exp12-note.txt")) // 非 .jsonl 文件：不命中

	t.Run("exploration 12 只命中 exp12- 前缀", func(t *testing.T) {
		targets, err := taskWorkspaceTargets(dataDir, "9", 12)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := baseNames(targets)
		sort.Strings(got)
		want := []string{"9", "exp12-a", "exp12-b", "exp12-c.jsonl"}
		if !equalStrings(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("exploration 123 只命中 exp123- 前缀", func(t *testing.T) {
		targets, err := taskWorkspaceTargets(dataDir, "9", 123)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := baseNames(targets)
		sort.Strings(got)
		want := []string{"9", "exp123-d.jsonl", "exp123-other"}
		if !equalStrings(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("tasks 目录缺失时仅返回 transcripts 项", func(t *testing.T) {
		targets, err := taskWorkspaceTargets(dataDir, "404", 12)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := baseNames(targets)
		sort.Strings(got)
		want := []string{"exp12-a", "exp12-b", "exp12-c.jsonl"}
		if !equalStrings(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("transcripts 目录缺失时仅返回 tasks 项", func(t *testing.T) {
		emptyDir := t.TempDir()
		mustMkdirAll(t, filepath.Join(emptyDir, "tasks", "7"))
		targets, err := taskWorkspaceTargets(emptyDir, "7", 12)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := baseNames(targets)
		if !equalStrings(got, []string{"7"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("全部缺失返回空列表", func(t *testing.T) {
		targets, err := taskWorkspaceTargets(t.TempDir(), "1", 12)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(targets) != 0 {
			t.Fatalf("expected empty, got %v", targets)
		}
	})
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

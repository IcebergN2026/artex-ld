package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 【二开·安全·修🔴-2】Root 钳制：文件工具不允许解析出 Root 树外的路径。
func TestResolvePathRootConfinement(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")
	tc := &ToolContext{WorkingDir: filepath.Join(root, "tasks", "1"), Root: root}

	cases := []struct {
		name string
		in   string
		ok   bool // true = 期望放行（在 Root 内）
	}{
		{"相对路径-工作目录内", "notes/a.txt", true},
		{"绝对路径-Root内", filepath.Join(root, "tasks", "1", "a.txt"), true},
		{"绝对路径-Root外", outside, false},
		{"点dot逃逸", filepath.Join("..", "..", "..", "escape.txt"), false},
		{"绝对路径-平台根", filepath.Join(filepath.VolumeName(root)+string(filepath.Separator), "etc", "passwd"), false},
		{"Root 本身", root, true},
	}
	for _, c := range cases {
		got := resolvePath(tc, c.in)
		abs, _ := filepath.Abs(got)
		// resolvePath 钳制后返回 root/sentinel，即仍「在 Root 内」——边界断言不能用
		// 前缀判断，看是否被重写到 sentinel。
		if c.ok && filepath.Base(abs) == "..escape-denied-" {
			t.Errorf("%s: 期望放行但被钳: in=%q got=%q", c.name, c.in, got)
		}
		if !c.ok && filepath.Base(abs) != "..escape-denied-" {
			t.Errorf("%s: 期望拦截却放行: in=%q got=%q", c.name, c.in, got)
		}
	}
	// sentinel 必须是个不存在的路径
	if _, err := os.Stat(resolvePath(tc, filepath.Join(filepath.Dir(root), "nope.txt"))); !os.IsNotExist(err) {
		t.Errorf("逃逸路径应落不存在 sentinel, err=%v", err)
	}
}

// Root 为空 = 经典行为（不钳制）。
func TestResolvePathNoRootKeepsClassic(t *testing.T) {
	tc := &ToolContext{WorkingDir: t.TempDir()}
	abs, _ := filepath.Abs(filepath.Join(string(filepath.Separator), "etc", "passwd"))
	p := resolvePath(tc, abs)
	if p != abs {
		t.Fatalf("Root 为空时应保持经典行为, got %q", p)
	}
}

// Read 工具端到端：Root 外绝对路径被拒。
func TestReadToolRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("topsecret"), 0o644)
	tc := &ToolContext{WorkingDir: root, Root: root}
	in, _ := json.Marshal(map[string]any{"file_path": outside})
	res, err := runRead(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Flatten()
	if got := s; len(got) > 0 && got[:1] != "[" && got != "" {
		if !contains(got, "Error") && !contains(got, "no such file") {
			t.Errorf("应读不到 Root 外文件, got: %.120q", got)
		}
	}
	if contains(s, "topsecret") {
		t.Fatalf("泄露了 Root 外文件内容!")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

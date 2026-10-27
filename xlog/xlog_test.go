package xlog

// ═══ ARTEX 二开 · [P-LOG·M0] xlog 级别过滤/解析/去重单测 ═══

import (
	"bytes"
	"log"
	"testing"
	"time"
)

// withCapturedLog 临时接管标准 log 输出，返回读取缓冲。
func withCapturedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

func setLevelForTest(t *testing.T, lvl int) {
	t.Helper()
	old := Level()
	level.Store(int32(lvl))
	t.Cleanup(func() { level.Store(int32(old)) })
}

func TestParseLevel(t *testing.T) {
	cases := map[string]int{"debug": LevelDebug, "INFO": LevelInfo, "warn": LevelWarn, "warning": LevelWarn, "error": LevelError, " info ": LevelInfo}
	for in, want := range cases {
		got, ok := ParseLevel(in)
		if !ok || got != want {
			t.Errorf("ParseLevel(%q) = %d,%v; want %d,true", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "verbose", "1", "err"} {
		if _, ok := ParseLevel(bad); ok {
			t.Errorf("ParseLevel(%q) should fail", bad)
		}
	}
}

func TestLevelFilterMatrix(t *testing.T) {
	buf := withCapturedLog(t)
	// 阈值 info：debug 被滤，info/warn/error 通过；且带级别前缀。
	setLevelForTest(t, LevelInfo)
	Debugf("[engine] hidden")
	Infof("[engine] shown-info")
	Warnf("[engine] shown-warn")
	Errorf("[engine] shown-error")
	out := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("hidden")) {
		t.Errorf("debug should be filtered at info threshold:\n%s", out)
	}
	for _, want := range []string{"[INFO] [engine] shown-info", "[WARN] [engine] shown-warn", "[ERROR] [engine] shown-error"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}

	// 阈值 error：只剩 error。
	buf.Reset()
	setLevelForTest(t, LevelError)
	Infof("x-info")
	Warnf("x-warn")
	Errorf("x-error")
	out = buf.String()
	if bytes.Contains(buf.Bytes(), []byte("x-info")) || bytes.Contains(buf.Bytes(), []byte("x-warn")) {
		t.Errorf("info/warn should be filtered at error threshold:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("x-error")) {
		t.Errorf("error missing:\n%s", out)
	}
}

func TestHotSetLevel(t *testing.T) {
	buf := withCapturedLog(t)
	setLevelForTest(t, LevelInfo)
	if SetLevel("bogus") {
		t.Fatal("SetLevel(bogus) should return false")
	}
	if !SetLevel("debug") {
		t.Fatal("SetLevel(debug) should return true")
	}
	Debugf("after-toggle")
	if !bytes.Contains(buf.Bytes(), []byte("after-toggle")) {
		t.Fatal("debug line should appear after hot SetLevel(debug)")
	}
	if LevelString() != "debug" {
		t.Errorf("LevelString = %s, want debug", LevelString())
	}
}

func TestDebugCompat(t *testing.T) {
	setLevelForTest(t, LevelDebug)
	if !Debug() {
		t.Error("Debug() should be true at debug threshold")
	}
	setLevelForTest(t, LevelInfo)
	if Debug() {
		t.Error("Debug() should be false at info threshold")
	}
}

func TestOncefDedup(t *testing.T) {
	buf := withCapturedLog(t)
	setLevelForTest(t, LevelError) // debug 降级输出被滤掉，只剩首报
	key := "test-oncef-dedup"
	Oncef(key, time.Minute, LevelError, "boom %d", 1)
	Oncef(key, time.Minute, LevelError, "boom %d", 2)
	Oncef(key, time.Minute, LevelError, "boom %d", 3)
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("boom 1")) {
		t.Fatalf("first report missing:\n%s", out)
	}
	if bytes.Contains(buf.Bytes(), []byte("boom 2")) || bytes.Contains(buf.Bytes(), []byte("boom 3")) {
		t.Fatalf("repeats within window should be demoted to debug (filtered here):\n%s", out)
	}
	// 窗口过后应再次首报。直接把时钟拨回去。
	e, _ := onceReg.Load(key)
	e.(*onceEntry).last.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	Oncef(key, time.Minute, LevelError, "boom %d", 4)
	if !bytes.Contains(buf.Bytes(), []byte("boom 4")) {
		t.Fatalf("report after window missing:\n%s", buf.String())
	}
}

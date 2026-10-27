// ═══ ARTEX 二开 · [P-LOG·H1 日志级别开关（xlog 包）] ═══
// 功能介绍：全部二开日志的统一级别开关。ARTEX 原生只有标准库 log + logsink 关键词
//	定级，无 level 概念；本包以最小侵入提供四级日志（debug/info/warn/error）：
//	  - 级别前缀 [DEBUG]/[INFO]/[WARN]/[ERROR]，logsink 优先解析前缀定级，
//	    解析不到再走旧的关键词兜底（存量 370 处 log.Printf 行为不变）；
//	  - 过滤阈值：settings 表 log_level 键（UI 热切换，立即生效）＞
//	    环境变量 ARTEX_LOG_LEVEL ＞ ARTEX_DEBUG=1（等价 debug）＞ 默认 info；
//	  - debug 级在过滤阈值之上时零输出、零落库（logsink/内存环/server_logs 都不进）。
//
// 二开日期：2026-10-21（H1）；2026-10-25 扩展四级 + 热切换（ARTEX-日志增强计划书 M0）
package xlog

import (
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 级别常量：数值越小越详细。
const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

// level 为当前过滤阈值（原子，支持运行中热切换）。启动默认值见 initLevel。
var level atomic.Int32

func init() {
	level.Store(int32(initLevel()))
}

// initLevel 计算进程启动阈值：ARTEX_LOG_LEVEL（debug/info/warn/error）优先，
// 其次 ARTEX_DEBUG=1（沿袭 H1 语义，等价 debug），默认 info。
func initLevel() int {
	if v, ok := ParseLevel(os.Getenv("ARTEX_LOG_LEVEL")); ok {
		return v
	}
	if os.Getenv("ARTEX_DEBUG") == "1" {
		return LevelDebug
	}
	return LevelInfo
}

// ParseLevel 解析级别名（大小写不敏感）。ok=false 表示无法识别。
func ParseLevel(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	}
	return 0, false
}

// LevelName 返回级别的规范名（debug/info/warn/error）。
func LevelName(l int) string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "info"
}

// Level 返回当前过滤阈值。
func Level() int { return int(level.Load()) }

// LevelString 返回当前过滤阈值名（供 /api/settings 回显）。
func LevelString() string { return LevelName(Level()) }

// SetLevel 热切换过滤阈值（UI 调：先落 settings 表再调本函数，立即生效）。
// 无法识别的值忽略并返回 false。
func SetLevel(s string) bool {
	v, ok := ParseLevel(s)
	if !ok {
		return false
	}
	level.Store(int32(v))
	return true
}

// Debug 兼容 H1 语义：当前阈值允许 debug 输出时为 true。
// 注意：与 H1 的「启动读一次」不同，本函数每次调用都读原子值，热切换即时反映。
func Debug() bool { return Level() <= LevelDebug }

func logf(l int, prefix, format string, args ...any) {
	if Level() > l {
		return
	}
	log.Printf(prefix+" "+format, args...)
}

// Debugf 阈值 ≤ debug 时输出，前缀 [DEBUG]。
func Debugf(format string, args ...any) { logf(LevelDebug, "[DEBUG]", format, args...) }

// Infof 阈值 ≤ info 时输出，前缀 [INFO]。
func Infof(format string, args ...any) { logf(LevelInfo, "[INFO]", format, args...) }

// Warnf 阈值 ≤ warn 时输出，前缀 [WARN]。
func Warnf(format string, args ...any) { logf(LevelWarn, "[WARN]", format, args...) }

// Errorf 阈值 ≤ error 时输出，前缀 [ERROR]。
func Errorf(format string, args ...any) { logf(LevelError, "[ERROR]", format, args...) }

// ── 去重限流 ────────────────────────────────────────────────────────────────
// 热路径（循环/轮询）里的错误日志防刷屏：同一 key 首次按给定级别输出，窗口期内
// 的重复降为 debug 输出。key 必须含 task/tool 等维度，避免不同任务互相压制。

type onceEntry struct {
	last atomic.Int64 // 上次「首报」的 unix nano
}

var onceReg sync.Map // key string -> *onceEntry

// Oncef 去重日志：key 首次（或距上次首报超过 window）按 lvl 输出，窗口内重复
// 降为 [DEBUG] 输出（debug 关闭时即完全静默）。lvl 取 LevelError/LevelWarn。
func Oncef(key string, window time.Duration, lvl int, format string, args ...any) {
	e, _ := onceReg.LoadOrStore(key, &onceEntry{})
	ent := e.(*onceEntry)
	now := time.Now().UnixNano()
	last := ent.last.Load()
	if last == 0 || now-last >= window.Nanoseconds() {
		if ent.last.CompareAndSwap(last, now) {
			logf(lvl, levelPrefix(lvl), format, args...)
			return
		}
	}
	// 窗口内重复：降为 debug。
	logf(LevelDebug, "[DEBUG]", format, args...)
}

func levelPrefix(l int) string {
	switch l {
	case LevelError:
		return "[ERROR]"
	case LevelWarn:
		return "[WARN]"
	case LevelInfo:
		return "[INFO]"
	}
	return "[DEBUG]"
}

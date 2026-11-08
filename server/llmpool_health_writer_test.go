package server

import (
	"runtime"
	"testing"

	"artex/db"
)

// TestLLMHealthRegistryBoundedWriter proves that 5000 circuit-breaker events no
// longer spawn 5000 goroutines: the registry funnels all persistence through a
// single process-lifetime writer with a bounded queue (would fail under the old
// per-event goroutine implementation). It exercises the real registry callbacks
// and real DB implementation; DB failures are only logged (best-effort), so no
// schema seeding is needed.
func TestLLMHealthRegistryBoundedWriter(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("未配置数据库,跳过")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	reg := newLLMHealthRegistry(pg)
	before := runtime.NumGoroutine()
	for range 5000 {
		reg.Trip(999999, "synthetic", true) // 每次 Trip 都触发 persist
		reg.Pass(999999)                    // 每次 Pass 都触发 forget
	}
	if delta := runtime.NumGoroutine() - before; delta > 10 {
		t.Fatalf("熔断事件导致 goroutine 无界增长: delta=%d(应为单 writer,<=10)", delta)
	}
	t.Cleanup(func() { _ = pg.ClearLLMHealth(999999) })
}

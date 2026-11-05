package server

import (
	"fmt"
	"strings"
	"testing"

	"artex/db"
)

// TestNormalizeSourceTaskIDs 覆盖：合法通过、重复 id、id<=0、不存在、超上限。
// exists 用闭包 fake，无 DB 依赖。
func TestNormalizeSourceTaskIDs(t *testing.T) {
	existing := map[int64]bool{1: true, 2: true, 42: true}
	exists := func(id int64) bool { return existing[id] }

	t.Run("合法通过_去重保序", func(t *testing.T) {
		ids, err := normalizeSourceTaskIDs([]string{"1", " 2 ", "42"}, exists)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 42 {
			t.Fatalf("unexpected ids: %v", ids)
		}
	})

	t.Run("空列表", func(t *testing.T) {
		ids, err := normalizeSourceTaskIDs(nil, exists)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("expected empty, got %v", ids)
		}
	})

	t.Run("重复 id", func(t *testing.T) {
		_, err := normalizeSourceTaskIDs([]string{"1", "1"}, exists)
		if err == nil || err.Error() != "关联任务 id 无效或重复" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("id 小于等于 0", func(t *testing.T) {
		for _, raw := range []string{"0", "-3"} {
			_, err := normalizeSourceTaskIDs([]string{raw}, exists)
			if err == nil || err.Error() != "关联任务 id 无效或重复" {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
		}
	})

	t.Run("非数字", func(t *testing.T) {
		_, err := normalizeSourceTaskIDs([]string{"abc"}, exists)
		if err == nil || err.Error() != "关联任务 id 无效或重复" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("不存在", func(t *testing.T) {
		_, err := normalizeSourceTaskIDs([]string{"7"}, exists)
		if err == nil || err.Error() != "关联任务 #7 不存在" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("超上限", func(t *testing.T) {
		raw := make([]string, db.MaxTaskSourceCount+1)
		for i := range raw {
			raw[i] = fmt.Sprintf("%d", i+1000)
		}
		_, err := normalizeSourceTaskIDs(raw, exists)
		want := fmt.Sprintf("关联任务最多选择 %d 个", db.MaxTaskSourceCount)
		if err == nil || err.Error() != want {
			t.Fatalf("err=%v, want %q", err, want)
		}
	})

	t.Run("上限恰好通过", func(t *testing.T) {
		existingAll := map[int64]bool{}
		raw := make([]string, db.MaxTaskSourceCount)
		for i := range raw {
			id := int64(i + 1000)
			raw[i] = fmt.Sprintf("%d", id)
			existingAll[id] = true
		}
		ids, err := normalizeSourceTaskIDs(raw, func(id int64) bool { return existingAll[id] })
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(ids) != db.MaxTaskSourceCount {
			t.Fatalf("len=%d", len(ids))
		}
	})

	t.Run("文案不含换行", func(t *testing.T) {
		_, err := normalizeSourceTaskIDs([]string{"x"}, exists)
		if err != nil && strings.Contains(err.Error(), "\n") {
			t.Fatalf("message contains newline: %q", err.Error())
		}
	})
}

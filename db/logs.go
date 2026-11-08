// logs.go：后端日志表（server_logs）的追加、读取与过期清理。
// 主要函数/类型：
//   - DBLog：一条持久化的后端日志行。
//   - InsertLog：追加一条日志并返回自增 id。
//   - RecentLogs / ListLogsBefore：按时间正序读取最近日志 / 按 id 游标向前翻页。
//   - DeleteOldLogs：删除 N 天前的旧日志并返回删除行数。

package db

import (
	"context"
	"time"
)

// DBLog is one persisted backend log row from the server_logs table.
type DBLog struct {
	ID        int64
	CreatedAt time.Time
	Level     string
	Tag       string
	Text      string
}

// InsertLog appends one log line and returns its auto-assigned id.
func (d *DB) InsertLog(level, tag, text string) (int64, error) {
	var id int64
	err := d.QueryRowContext(context.Background(),
		"INSERT INTO server_logs(level,tag,text) VALUES($1,$2,$3) RETURNING id",
		utf8Clean(level), utf8Clean(tag), utf8Clean(text),
	).Scan(&id)
	return id, err
}

// RecentLogs returns the most recent limit rows, oldest-first.
func (d *DB) RecentLogs(limit int) ([]*DBLog, error) {
	rows, err := d.QueryContext(context.Background(),
		`SELECT id, created_at, level, tag, text
		   FROM server_logs
		  ORDER BY id DESC
		  LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DBLog
	for rows.Next() {
		l := &DBLog{}
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.Level, &l.Tag, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	// Reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// DeleteOldLogs 【P-LOG·M0】删除 days 天前的 server_logs 行（表无上限增长 → 每日清理）。
// 返回删除行数。
func (d *DB) DeleteOldLogs(days int) (int64, error) {
	res, err := d.ExecContext(context.Background(),
		`DELETE FROM server_logs WHERE created_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListLogsBefore returns up to limit rows with id < beforeID, oldest-first.
func (d *DB) ListLogsBefore(beforeID int64, limit int) ([]*DBLog, error) {
	rows, err := d.QueryContext(context.Background(),
		`SELECT id, created_at, level, tag, text
		   FROM server_logs
		  WHERE id < $1
		  ORDER BY id DESC
		  LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DBLog
	for rows.Next() {
		l := &DBLog{}
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.Level, &l.Tag, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	// Reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

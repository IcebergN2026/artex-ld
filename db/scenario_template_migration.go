package db

import (
	"context"
	"database/sql"
	"fmt"
)

const ExternalPentestTemplateName = "外网渗透"

// migrateExternalPentestDefaultTemplates renames only the original builtin rows.
// IDs, bodies, task snapshots and custom templates are deliberately left intact.
func migrateExternalPentestDefaultTemplates(ctx context.Context, conn *sql.Conn) error {
	const query = `
UPDATE planner_templates
SET name='外网渗透', nkey='外网渗透', updated_at=now()
WHERE builtin AND nkey='内置默认';
UPDATE worker_templates
SET name='外网渗透', nkey='外网渗透', updated_at=now()
WHERE builtin AND nkey='内置默认';`
	if _, err := conn.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("rename builtin default scenario templates: %w", err)
	}
	return nil
}

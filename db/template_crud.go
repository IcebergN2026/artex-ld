// template_crud.go：planner/worker 模板共用的表无关 CRUD helper。
// 主要函数/类型：
//   - templateRowData：模板行（id、名称、nkey、正文、builtin/default 标记等）。
//   - normalizeTemplateInputFields / normalizeTemplatePatchFields：名称/正文规范化。
//   - listTemplateRows / getTemplateRow / getTemplateRowByNKey：按表查询模板。
//   - createTemplateRow / patchTemplateRow / deleteTemplateRow / setDefaultTemplateRow：按表增改删与设默认。
//   - seedTemplateIfEmptyRow / defaultTemplateBodyRow：空表播种与默认模板正文。

package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// db/config.go 与 db/worker_templates.go 的 scan/normalize/CRUD/seed 同构实现收敛于此；
// 公开类型与 sentinel error 不变，差异项（表名、上限常量、sentinel error）经参数注入。

// templateRowData 与 PlannerTemplate/WorkerTemplate 逐字段同构（含 json tag）——
// 两个公开类型借此互相/向上转换，scan 只写一处。
type templateRowData struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	Body      string    `json:"body"`
	Builtin   bool      `json:"builtin"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// templateCols 两侧模板表同名列清单（原 plannerTemplateCols/workerTemplateCols 逐字相同）。
const templateCols = `id, name, nkey, body, builtin, is_default, created_at, updated_at`

// scanTemplateRow 扫一行模板数据（planner/worker 共用的 scan 实现）。
func scanTemplateRow(row interface{ Scan(...any) error }) (templateRowData, error) {
	var t templateRowData
	err := row.Scan(&t.ID, &t.Name, &t.NKey, &t.Body, &t.Builtin, &t.IsDefault, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// checkTemplateName name 归一化 + 长度规则（错误用调用侧 sentinel；规则两侧逐字一致）。
func checkTemplateName(name string, maxNameRunes int, errInvalid error) (string, error) {
	name = taskTemplateName(name)
	switch {
	case name == "":
		return "", fmt.Errorf("%w: name is required", errInvalid)
	case utf8.RuneCountInString(name) > maxNameRunes:
		return "", fmt.Errorf("%w: name exceeds %d characters", errInvalid, maxNameRunes)
	}
	return name, nil
}

// checkTemplateBody body 归一化 + 长度规则（错误用调用侧 sentinel；规则两侧逐字一致）。
func checkTemplateBody(body string, maxBodyBytes int, errInvalid error) (string, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return "", fmt.Errorf("%w: body is required", errInvalid)
	case len(body) > maxBodyBytes:
		return "", fmt.Errorf("%w: body exceeds %d bytes", errInvalid, maxBodyBytes)
	}
	return body, nil
}

// normalizeTemplateInputFields create/update 归一化公共规则：name/body 各过各的规则，name 产 nkey。
func normalizeTemplateInputFields(name, body string, maxNameRunes, maxBodyBytes int, errInvalid error) (normName, normBody, nkey string, err error) {
	normName, err = checkTemplateName(name, maxNameRunes, errInvalid)
	if err != nil {
		return "", "", "", err
	}
	normBody, err = checkTemplateBody(body, maxBodyBytes, errInvalid)
	if err != nil {
		return "", "", "", err
	}
	return normName, normBody, taskTemplateNKey(normName), nil
}

// normalizeTemplatePatchFields PATCH 归一化公共规则：nil 字段不动；name 过 name 规则并产 nkey；
// body 过 body 规则。返回归一化后的指针（某侧未提供则为 nil）。
func normalizeTemplatePatchFields(name, body *string, maxNameRunes, maxBodyBytes int, errInvalid error) (normName, nkey, normBody *string, err error) {
	if name == nil && body == nil {
		return nil, nil, nil, fmt.Errorf("%w: no fields supplied", errInvalid)
	}
	if name != nil {
		n, nerr := checkTemplateName(*name, maxNameRunes, errInvalid)
		if nerr != nil {
			return nil, nil, nil, nerr
		}
		key := taskTemplateNKey(n)
		normName, nkey = &n, &key
	}
	if body != nil {
		b, berr := checkTemplateBody(*body, maxBodyBytes, errInvalid)
		if berr != nil {
			return nil, nil, nil, berr
		}
		normBody = &b
	}
	return normName, nkey, normBody, nil
}

// listTemplateRows 【方法 1/7】最近维护在前（与 ListTaskTemplates 同序）。
func listTemplateRows(d *DB, table string) ([]*templateRowData, error) {
	rows, err := d.Query(`SELECT ` + templateCols + ` FROM ` + table + ` ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*templateRowData{}
	for rows.Next() {
		t, err := scanTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// getTemplateRow 【方法 2/7】不存在返回 (nil, nil)（S13 存在性校验靠它区分 404 与 500）。
func getTemplateRow(d *DB, table string, id int64) (*templateRowData, error) {
	t, err := scanTemplateRow(d.QueryRow(`SELECT `+templateCols+` FROM `+table+` WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// getTemplateRowByNKey 按规范化名查模板（场景/种子按 nkey 解析模板 id 用）。
func getTemplateRowByNKey(d *DB, table, nkey string) (*templateRowData, error) {
	t, err := scanTemplateRow(d.QueryRow(`SELECT `+templateCols+` FROM `+table+` WHERE nkey=$1`, nkey))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// createTemplateRow 【方法 3/7】插入一套预设；nkey 冲突（视觉等价重名）→ errConflict。
func createTemplateRow(d *DB, table, name, nkey, body string, errConflict error) (*templateRowData, error) {
	t, err := scanTemplateRow(d.QueryRow(`
INSERT INTO `+table+`(name, nkey, body)
VALUES ($1,$2,$3)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+templateCols, name, nkey, body))
	if err == sql.ErrNoRows {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// patchTemplateRow 单 UPDATE 原子合并所给字段（并发 disjoint PATCH 不丢改动）。
// builtin 行保护策略（改名 → errBuiltin；正文可改）对两侧一致，经 sentinel 参数注入保留各自错误身份；
// 两侧如需分叉，在此函数加参数而非复制整段。
func patchTemplateRow(d *DB, table string, id int64, name, nkey, body *string, errNotFound, errBuiltin, errConflict error) (*templateRowData, error) {
	var builtin bool
	var existingName string
	if err := d.QueryRow(`SELECT builtin,name FROM `+table+` WHERE id=$1`, id).Scan(&builtin, &existingName); err != nil {
		if err == sql.ErrNoRows {
			return nil, errNotFound
		}
		return nil, err
	}
	if builtin && name != nil && *name != existingName {
		return nil, errBuiltin
	}
	t, err := scanTemplateRow(d.QueryRow(`UPDATE `+table+`
SET name=CASE WHEN $2 THEN $3::text ELSE name END,
    nkey=CASE WHEN $2 THEN $4::text ELSE nkey END,
    body=CASE WHEN $5 THEN $6::text ELSE body END
WHERE id=$1
RETURNING `+templateCols,
		id,
		name != nil, name, nkey,
		body != nil, body,
	))
	if err == sql.ErrNoRows {
		return nil, errNotFound
	}
	if taskTemplateUniqueViolation(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// deleteTemplateRow 【方法 5/7】删除一套预设；builtin/默认禁删；返回是否存在并删除。
// 已选用它的任务不受影响——创建时正文已快照进 tasks.*_prompt_override（D11-b）。
func deleteTemplateRow(d *DB, table string, id int64, errBuiltin, errDefault error) (bool, error) {
	var builtin, isDefault bool
	if err := d.QueryRow(`SELECT builtin, is_default FROM `+table+` WHERE id=$1`, id).Scan(&builtin, &isDefault); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if builtin {
		return false, errBuiltin
	}
	if isDefault {
		return false, errDefault
	}
	result, err := d.Exec(`DELETE FROM `+table+` WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// setDefaultTemplateRow 【方法 6/7】事务内先清后设，全库唯一默认；部分唯一索引 uq_*_templates_default 兜底并发。
func setDefaultTemplateRow(d *DB, table string, id int64, errNotFound error) (*templateRowData, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ` + table + ` SET is_default=false WHERE is_default`); err != nil {
		return nil, err
	}
	t, err := scanTemplateRow(tx.QueryRow(`UPDATE `+table+` SET is_default=true WHERE id=$1 RETURNING `+templateCols, id))
	if err == sql.ErrNoRows {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t, nil
}

// defaultTemplateBodyRow 【方法 7/7】读默认模板正文；无默认行（表空/直写库）→ ("", nil)，
// 调用方落到 00 册 A4 第④级内置模板兜底。S13 快照链与 A4 第③级共用本方法。
func defaultTemplateBodyRow(d *DB, table string) (string, error) {
	var body string
	err := d.QueryRow(`SELECT body FROM ` + table + ` WHERE is_default LIMIT 1`).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

// seedTemplateIfEmptyRow 首插入 only：nkey 已存在即 no-op，用户后续操作（换默认/新建/删除自定义）
// 重启不被重置。isDefault 参数化：五领域模板传 false，全库默认仍只有「内置默认」一行。
func seedTemplateIfEmptyRow(d *DB, table, name, nkey, body string, isDefault bool) (bool, error) {
	res, err := d.Exec(`
INSERT INTO `+table+`(name, nkey, body, builtin, is_default)
VALUES ($1, $2, $3, true, $4)
ON CONFLICT (nkey) DO NOTHING`, name, nkey, body, isDefault)
	if err != nil {
		return false, fmt.Errorf("seed %s template %s: %w", strings.TrimSuffix(table, "_templates"), name, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

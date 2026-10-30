package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxWorkerTemplateNameRunes = 120
	MaxWorkerTemplateBodyBytes = 32 << 10 // 32KB 上限（按字节计，含中文）；S11 试渲染同口径
	// WorkerTemplateBuiltinName/NKey 内置默认模板的固定身份——builtin 行名称只读，
	// nkey 恒定，seed 才能用 ON CONFLICT (nkey) DO NOTHING 幂等（改名会换 nkey 导致重复 seed）。
	WorkerTemplateBuiltinName = ExternalPentestTemplateName
	WorkerTemplateBuiltinNKey = ExternalPentestTemplateName
)

var (
	ErrWorkerTemplateInvalid          = errors.New("invalid worker template")
	ErrWorkerTemplateNameConflict     = errors.New("worker template name already exists")
	ErrWorkerTemplateNotFound         = errors.New("worker template not found")
	ErrWorkerTemplateBuiltin          = errors.New("builtin worker template is read-only")
	ErrWorkerTemplateDefaultProtected = errors.New("default worker template cannot be deleted")
)

// WorkerTemplate 是一套 worker 提示词预设（段 [A] 全文替换体）。
type WorkerTemplate struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	Body      string    `json:"body"`
	Builtin   bool      `json:"builtin"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WorkerTemplateInput 是 create/update 归一化后的载荷。
type WorkerTemplateInput struct {
	Name string
	Body string
}

// WorkerTemplatePatch 只改指针非 nil 的字段（PATCH 语义）。
type WorkerTemplatePatch struct {
	Name *string
	Body *string
}

const workerTemplateCols = `id, name, nkey, body, builtin, is_default, created_at, updated_at`

func scanWorkerTemplate(row interface{ Scan(...any) error }) (WorkerTemplate, error) {
	var t WorkerTemplate
	err := row.Scan(&t.ID, &t.Name, &t.NKey, &t.Body, &t.Builtin, &t.IsDefault, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// 归一化直接复用 task_templates.go 同包的 taskTemplateName/taskTemplateNKey/taskTemplateUniqueViolation，
// 不重复定义（同包重名会编译冲突）。

func normalizeWorkerTemplateInput(in WorkerTemplateInput) (WorkerTemplateInput, string, error) {
	in.Name = taskTemplateName(in.Name)
	in.Body = strings.TrimSpace(in.Body)
	switch {
	case in.Name == "":
		return in, "", fmt.Errorf("%w: name is required", ErrWorkerTemplateInvalid)
	case utf8.RuneCountInString(in.Name) > MaxWorkerTemplateNameRunes:
		return in, "", fmt.Errorf("%w: name exceeds %d characters", ErrWorkerTemplateInvalid, MaxWorkerTemplateNameRunes)
	case in.Body == "":
		return in, "", fmt.Errorf("%w: body is required", ErrWorkerTemplateInvalid)
	case len(in.Body) > MaxWorkerTemplateBodyBytes:
		return in, "", fmt.Errorf("%w: body exceeds %d bytes", ErrWorkerTemplateInvalid, MaxWorkerTemplateBodyBytes)
	}
	return in, taskTemplateNKey(in.Name), nil
}

func normalizeWorkerTemplatePatch(patch WorkerTemplatePatch) (WorkerTemplatePatch, *string, error) {
	if patch.Name == nil && patch.Body == nil {
		return patch, nil, fmt.Errorf("%w: no fields supplied", ErrWorkerTemplateInvalid)
	}
	var nkey *string
	if patch.Name != nil {
		name := taskTemplateName(*patch.Name)
		if name == "" {
			return patch, nil, fmt.Errorf("%w: name is required", ErrWorkerTemplateInvalid)
		}
		if utf8.RuneCountInString(name) > MaxWorkerTemplateNameRunes {
			return patch, nil, fmt.Errorf("%w: name exceeds %d characters", ErrWorkerTemplateInvalid, MaxWorkerTemplateNameRunes)
		}
		key := taskTemplateNKey(name)
		patch.Name = &name
		nkey = &key
	}
	if patch.Body != nil {
		body := strings.TrimSpace(*patch.Body)
		if body == "" {
			return patch, nil, fmt.Errorf("%w: body is required", ErrWorkerTemplateInvalid)
		}
		if len(body) > MaxWorkerTemplateBodyBytes {
			return patch, nil, fmt.Errorf("%w: body exceeds %d bytes", ErrWorkerTemplateInvalid, MaxWorkerTemplateBodyBytes)
		}
		patch.Body = &body
	}
	return patch, nkey, nil
}

// 【方法 1/7】ListWorkerTemplates 最近维护在前（与 ListTaskTemplates 同序）。
func (d *DB) ListWorkerTemplates() ([]*WorkerTemplate, error) {
	rows, err := d.Query(`SELECT ` + workerTemplateCols + ` FROM worker_templates ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorkerTemplate{}
	for rows.Next() {
		t, err := scanWorkerTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// 【方法 2/7】GetWorkerTemplate 不存在返回 (nil, nil)（S13 存在性校验靠它区分 404 与 500）。
func (d *DB) GetWorkerTemplate(id int64) (*WorkerTemplate, error) {
	t, err := scanWorkerTemplate(d.QueryRow(`SELECT `+workerTemplateCols+` FROM worker_templates WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetWorkerTemplateByNKey 按规范化名查模板（场景/种子按 nkey 解析模板 id 用）。
func (d *DB) GetWorkerTemplateByNKey(nkey string) (*WorkerTemplate, error) {
	t, err := scanWorkerTemplate(d.QueryRow(`SELECT `+workerTemplateCols+` FROM worker_templates WHERE nkey=$1`, nkey))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// 【方法 3/7】CreateWorkerTemplate 插入一套预设；nkey 冲突（视觉等价重名）→ ErrWorkerTemplateNameConflict。
func (d *DB) CreateWorkerTemplate(in WorkerTemplateInput) (*WorkerTemplate, error) {
	in, nkey, err := normalizeWorkerTemplateInput(in)
	if err != nil {
		return nil, err
	}
	t, err := scanWorkerTemplate(d.QueryRow(`
INSERT INTO worker_templates(name, nkey, body)
VALUES ($1,$2,$3)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+workerTemplateCols, in.Name, nkey, in.Body))
	if err == sql.ErrNoRows {
		return nil, ErrWorkerTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// 【方法 4/7】UpdateWorkerTemplate 全量替换可编辑字段（PUT 语义，内部走原子 Patch）。
func (d *DB) UpdateWorkerTemplate(id int64, in WorkerTemplateInput) (*WorkerTemplate, error) {
	in, _, err := normalizeWorkerTemplateInput(in)
	if err != nil {
		return nil, err
	}
	return d.PatchWorkerTemplate(id, WorkerTemplatePatch{Name: &in.Name, Body: &in.Body})
}

// PatchWorkerTemplate 单 UPDATE 原子合并所给字段（并发 disjoint PATCH 不丢改动）。
// builtin 行允许编辑正文；名称/nkey 保持 seed 和场景引用身份，内置行仍禁止删除。
func (d *DB) PatchWorkerTemplate(id int64, patch WorkerTemplatePatch) (*WorkerTemplate, error) {
	patch, nkey, err := normalizeWorkerTemplatePatch(patch)
	if err != nil {
		return nil, err
	}
	var builtin bool
	var existingName string
	if err := d.QueryRow(`SELECT builtin,name FROM worker_templates WHERE id=$1`, id).Scan(&builtin, &existingName); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrWorkerTemplateNotFound
		}
		return nil, err
	}
	if builtin && patch.Name != nil && *patch.Name != existingName {
		return nil, ErrWorkerTemplateBuiltin
	}
	t, err := scanWorkerTemplate(d.QueryRow(`UPDATE worker_templates
SET name=CASE WHEN $2 THEN $3::text ELSE name END,
    nkey=CASE WHEN $2 THEN $4::text ELSE nkey END,
    body=CASE WHEN $5 THEN $6::text ELSE body END
WHERE id=$1
RETURNING `+workerTemplateCols,
		id,
		patch.Name != nil, patch.Name, nkey,
		patch.Body != nil, patch.Body,
	))
	if err == sql.ErrNoRows {
		return nil, ErrWorkerTemplateNotFound
	}
	if taskTemplateUniqueViolation(err) {
		return nil, ErrWorkerTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// 【方法 5/7】DeleteWorkerTemplate 删除一套预设；内置/默认禁删；返回是否存在并删除。
// 已选用它的任务不受影响——创建时正文已快照进 tasks.worker_prompt_override（D11-b）。
func (d *DB) DeleteWorkerTemplate(id int64) (bool, error) {
	var builtin, isDefault bool
	if err := d.QueryRow(`SELECT builtin, is_default FROM worker_templates WHERE id=$1`, id).Scan(&builtin, &isDefault); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if builtin {
		return false, ErrWorkerTemplateBuiltin
	}
	if isDefault {
		return false, ErrWorkerTemplateDefaultProtected
	}
	result, err := d.Exec(`DELETE FROM worker_templates WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// 【方法 6/7】SetDefaultWorkerTemplate 事务内先清后设，全库唯一默认；部分唯一索引 uq_worker_templates_default 兜底并发。
func (d *DB) SetDefaultWorkerTemplate(id int64) (*WorkerTemplate, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE worker_templates SET is_default=false WHERE is_default`); err != nil {
		return nil, err
	}
	t, err := scanWorkerTemplate(tx.QueryRow(`UPDATE worker_templates SET is_default=true WHERE id=$1 RETURNING `+workerTemplateCols, id))
	if err == sql.ErrNoRows {
		return nil, ErrWorkerTemplateNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t, nil
}

// 【方法 7/7】DefaultWorkerTemplateBody 读默认模板正文；无默认行（表空/直写库）→ ("", nil)，
// 调用方落到 00 册 A4 第④级内置模板兜底。S13 快照链与 A4 第③级共用本方法。
func (d *DB) DefaultWorkerTemplateBody() (string, error) {
	var body string
	err := d.QueryRow(`SELECT body FROM worker_templates WHERE is_default LIMIT 1`).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

// SeedWorkerTemplateIfEmpty 首插入 only：nkey 已存在即 no-op，用户后续操作（换默认/新建/删除自定义）
// 重启不被重置。调用方见 server/assembly.go seedPrompts（D11-d）；正文单源=agent/worker.go
// workerDefaultTmpl 与 agent/worker_domain_templates.go 五领域模板（经 agent 包透出）——db 不 import agent，否则成环。
// 【二开·P-LOG·S11】返回值改为 (inserted bool, err error)：供调用方只在首插时记 INFO（04 册 S11①）。
// 【二开·P-TPL·DOMAIN】isDefault 参数化：五领域模板传 false，全库默认仍只有「内置默认」一行
// （部分唯一索引 uq_worker_templates_default 保证；重复播种同名默认也不会违反）。
func (d *DB) SeedWorkerTemplateIfEmpty(name, nkey, body string, isDefault bool) (bool, error) {
	res, err := d.Exec(`
INSERT INTO worker_templates(name, nkey, body, builtin, is_default)
VALUES ($1, $2, $3, true, $4)
ON CONFLICT (nkey) DO NOTHING`, name, nkey, body, isDefault)
	if err != nil {
		return false, fmt.Errorf("seed worker template %s: %w", name, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// 确保 pgconn import 被使用（taskTemplateUniqueViolation 若已用 pgconn 则本行冗余但无害——
// 防御未来 task_templates.go 重构后本包丢引用）。
var _ = pgconn.PgError{}

// 【二开·P-TPL·D11】止

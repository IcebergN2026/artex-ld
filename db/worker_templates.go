// worker_templates.go：Worker 模板（worker_templates 表）的 CRUD 与默认值管理。
// 主要函数/类型：
//   - WorkerTemplate / WorkerTemplateInput / WorkerTemplatePatch：模板行与创建/补丁输入。
//   - CreateWorkerTemplate / UpdateWorkerTemplate / PatchWorkerTemplate：创建与全量/增量更新。
//   - ListWorkerTemplates / GetWorkerTemplate / GetWorkerTemplateByNKey：列表与单查。
//   - SetDefaultWorkerTemplate / DefaultWorkerTemplateBody：默认模板设置与正文读取。
//   - SeedWorkerTemplateIfEmpty：空表播种内置模板。

package db

import (
	"errors"
	"time"
)

// 【二开·P-TPL·D11-1 Worker 模板类型与声明】起 —— 常量、哨兵错、行类型与归一化辅助
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

// 归一化直接复用 task_templates.go 同包的 taskTemplateName/taskTemplateNKey/taskTemplateUniqueViolation，
// 以及 template_crud.go 的 checkTemplateName/checkTemplateBody 共享规则（同包重名会编译冲突，不重复定义）。

func scanWorkerTemplate(row interface{ Scan(...any) error }) (WorkerTemplate, error) {
	t, err := scanTemplateRow(row)
	return WorkerTemplate(t), err
}

func normalizeWorkerTemplateInput(in WorkerTemplateInput) (WorkerTemplateInput, string, error) {
	name, body, nkey, err := normalizeTemplateInputFields(in.Name, in.Body, MaxWorkerTemplateNameRunes, MaxWorkerTemplateBodyBytes, ErrWorkerTemplateInvalid)
	if err != nil {
		return in, "", err
	}
	in.Name, in.Body = name, body
	return in, nkey, nil
}

func normalizeWorkerTemplatePatch(patch WorkerTemplatePatch) (WorkerTemplatePatch, *string, error) {
	name, nkey, body, err := normalizeTemplatePatchFields(patch.Name, patch.Body, MaxWorkerTemplateNameRunes, MaxWorkerTemplateBodyBytes, ErrWorkerTemplateInvalid)
	if err != nil {
		return patch, nil, err
	}
	patch.Name, patch.Body = name, body
	return patch, nkey, nil
}

// 【二开·P-TPL·D11-1 Worker 模板类型与声明】止

// 【二开·P-TPL·D11-2 Worker 模板读方法】起 —— List/Get/GetByNKey
// 【方法 1/7】ListWorkerTemplates 最近维护在前（与 ListTaskTemplates 同序）。
func (d *DB) ListWorkerTemplates() ([]*WorkerTemplate, error) {
	rows, err := listTemplateRows(d, "worker_templates")
	if err != nil {
		return nil, err
	}
	out := make([]*WorkerTemplate, 0, len(rows))
	for _, t := range rows {
		converted := WorkerTemplate(*t)
		out = append(out, &converted)
	}
	return out, nil
}

// 【方法 2/7】GetWorkerTemplate 不存在返回 (nil, nil)（S13 存在性校验靠它区分 404 与 500）。
func (d *DB) GetWorkerTemplate(id int64) (*WorkerTemplate, error) {
	t, err := getTemplateRow(d, "worker_templates", id)
	if err != nil || t == nil {
		return nil, err
	}
	converted := WorkerTemplate(*t)
	return &converted, nil
}

// GetWorkerTemplateByNKey 按规范化名查模板（场景/种子按 nkey 解析模板 id 用）。
func (d *DB) GetWorkerTemplateByNKey(nkey string) (*WorkerTemplate, error) {
	t, err := getTemplateRowByNKey(d, "worker_templates", nkey)
	if err != nil || t == nil {
		return nil, err
	}
	converted := WorkerTemplate(*t)
	return &converted, nil
}

// 【二开·P-TPL·D11-2 Worker 模板读方法】止

// 【二开·P-TPL·D11-3 Worker 模板写方法】起 —— Create/Update/Patch/Delete
// 【方法 3/7】CreateWorkerTemplate 插入一套预设；nkey 冲突（视觉等价重名）→ ErrWorkerTemplateNameConflict。
func (d *DB) CreateWorkerTemplate(in WorkerTemplateInput) (*WorkerTemplate, error) {
	in, nkey, err := normalizeWorkerTemplateInput(in)
	if err != nil {
		return nil, err
	}
	t, err := createTemplateRow(d, "worker_templates", in.Name, nkey, in.Body, ErrWorkerTemplateNameConflict)
	if err != nil {
		return nil, err
	}
	converted := WorkerTemplate(*t)
	return &converted, nil
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
// 共享实现在 template_crud.go patchTemplateRow；worker 侧 sentinel 经参数注入。
func (d *DB) PatchWorkerTemplate(id int64, patch WorkerTemplatePatch) (*WorkerTemplate, error) {
	patch, nkey, err := normalizeWorkerTemplatePatch(patch)
	if err != nil {
		return nil, err
	}
	t, err := patchTemplateRow(d, "worker_templates", id, patch.Name, nkey, patch.Body,
		ErrWorkerTemplateNotFound, ErrWorkerTemplateBuiltin, ErrWorkerTemplateNameConflict)
	if err != nil {
		return nil, err
	}
	converted := WorkerTemplate(*t)
	return &converted, nil
}

// 【方法 5/7】DeleteWorkerTemplate 删除一套预设；内置/默认禁删；返回是否存在并删除。
// 已选用它的任务不受影响——创建时正文已快照进 tasks.worker_prompt_override（D11-b）。
func (d *DB) DeleteWorkerTemplate(id int64) (bool, error) {
	return deleteTemplateRow(d, "worker_templates", id, ErrWorkerTemplateBuiltin, ErrWorkerTemplateDefaultProtected)
}

// 【二开·P-TPL·D11-3 Worker 模板写方法】止

// 【二开·P-TPL·D11-4 Worker 模板默认与 seed】起 —— SetDefault/DefaultBody/Seed
// 【方法 6/7】SetDefaultWorkerTemplate 事务内先清后设，全库唯一默认；部分唯一索引 uq_worker_templates_default 兜底并发。
func (d *DB) SetDefaultWorkerTemplate(id int64) (*WorkerTemplate, error) {
	t, err := setDefaultTemplateRow(d, "worker_templates", id, ErrWorkerTemplateNotFound)
	if err != nil {
		return nil, err
	}
	converted := WorkerTemplate(*t)
	return &converted, nil
}

// 【方法 7/7】DefaultWorkerTemplateBody 读默认模板正文；无默认行（表空/直写库）→ ("", nil)，
// 调用方落到 00 册 A4 第④级内置模板兜底。S13 快照链与 A4 第③级共用本方法。
func (d *DB) DefaultWorkerTemplateBody() (string, error) {
	return defaultTemplateBodyRow(d, "worker_templates")
}

// SeedWorkerTemplateIfEmpty 首插入 only：nkey 已存在即 no-op，用户后续操作（换默认/新建/删除自定义）
// 重启不被重置。调用方见 server/assembly.go seedPrompts（D11-d）；正文单源=agent/worker.go
// workerDefaultTmpl 与 agent/worker_domain_templates.go 五领域模板（经 agent 包透出）——db 不 import agent，否则成环。
// 【二开·P-LOG·S11】返回值改为 (inserted bool, err error)：供调用方只在首插时记 INFO（04 册 S11①）。
// 【二开·P-TPL·DOMAIN】isDefault 参数化：五领域模板传 false，全库默认仍只有「内置默认」一行
// （部分唯一索引 uq_worker_templates_default 保证；重复播种同名默认也不会违反）。
func (d *DB) SeedWorkerTemplateIfEmpty(name, nkey, body string, isDefault bool) (bool, error) {
	return seedTemplateIfEmptyRow(d, "worker_templates", name, nkey, body, isDefault)
}

// 【二开·P-TPL·D11-4 Worker 模板默认与 seed】止

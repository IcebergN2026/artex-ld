// worker_templates.go：worker 模板库服务层——worker 侧参数差异项与端点挂出。
// 主要函数/类型：
//   - validateWorkerTemplateBody：试渲染校验（语法 + 目录外变量检测）。
//   - workerTemplatesAPI：worker 侧模板库参数（哨兵 error、内置保护 409 文案、db 方法注入）。
//   - pgListWorkerTemplates / pgCreateWorkerTemplate / pgUpdateWorkerTemplate / pgDeleteWorkerTemplate：模板查/建/改/删端点薄封装。
//   - pgSetDefaultWorkerTemplate / pgValidateWorkerTemplate：设默认与试渲染干跑端点。

// 【二开·P-TPL·S11 worker 模板库服务层】
// 功能介绍：worker 模板试渲染校验（P2 先落地，供 S8 spawn_task 模板 id 分支复用；
//
//	P3 起 /api/worker-templates 五端点 + 试渲染干跑端点经 workerTemplatesAPI
//	（template_handlers.go 参数化实现）挂出；本文件只保留 worker 侧参数与差异项）。
//

package server

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"artex/agent"
	"artex/db"
)

// workerTemplateVarsHint 合法变量目录文案 = 00 册 A4 WorkerVars 五字段（agent/prompt.go WorkerVars）；
// 后端校验、400 文案、前端 U8 变量 chips 三处同目录。
const workerTemplateVarsHint = "{{.ProxyAddr}} {{.WorkerName}} {{.DataDir}} {{.Now}}"

const maxWorkerTemplateRequestBytes = 64 << 10 // 请求体上限（正文 32KB + JSON 开销余量）

// validateWorkerTemplateBody 试渲染校验——与 agent/prompt.go renderTmpl 同模式（missingkey=error）：
//  1. template.Parse 查语法：错误自带 `template: p:<行>:<列>` 定位（parse 一步与 planner 侧共享 parseValidateTemplate）；
//  2. 用一组全字段假 WorkerVars 执行：引用目录外变量（如 {{.Foo}}）在执行期报
//     `template: p:<行>:<列>: … can't evaluate field Foo in type agent.WorkerVars`。
//     32KB 上限在此把守（db 层 normalize 再兜一次，双保险）。
func validateWorkerTemplateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("模板正文不能为空")
	}
	if len(body) > db.MaxWorkerTemplateBodyBytes {
		return fmt.Errorf("模板正文最多 %d 字节（32KB），当前 %d 字节", db.MaxWorkerTemplateBodyBytes, len(body))
	}
	t, err := parseValidateTemplate(body)
	if err != nil {
		return fmt.Errorf("模板语法错误：%w", err)
	}
	vars := agent.WorkerVars{
		ProxyAddr:  "http://127.0.0.1:8788",
		WorkerName: "worker-1",
		DataDir:    "/tmp/artex-validate",
		Now:        "2026-10-21 12:00",
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return fmt.Errorf("试渲染失败（可能引用了目录外变量；合法变量：%s）：%w", workerTemplateVarsHint, err)
	}
	return nil
}

// workerTemplatesAPI worker 侧模板库参数：哨兵 error、内置保护 409 文案、db 方法注入。
// 内置保护语义（PATCH 改名/DELETE → ErrWorkerTemplateBuiltin；正文可改 → 200）编码在
// db.PatchWorkerTemplate/DeleteWorkerTemplate 里；409 文案与 planner 侧逐字节不同（差异保留）。
var workerTemplatesAPI = &templateAPI[db.WorkerTemplate, db.WorkerTemplateInput, db.WorkerTemplatePatch]{
	kind:         "worker",
	maxReqBytes:  maxWorkerTemplateRequestBytes,
	maxNameRunes: db.MaxWorkerTemplateNameRunes,
	validateBody: validateWorkerTemplateBody,
	errInvalid:   db.ErrWorkerTemplateInvalid,
	errConflict:  db.ErrWorkerTemplateNameConflict,
	errBuiltin:   db.ErrWorkerTemplateBuiltin,
	errDefault:   db.ErrWorkerTemplateDefaultProtected,
	errNotFound:  db.ErrWorkerTemplateNotFound,
	builtinMsg:   "内置模板正文可编辑，但名称/nkey 不可修改，且内置模板不能删除",
	toInput:      func(name, body string) db.WorkerTemplateInput { return db.WorkerTemplateInput{Name: name, Body: body} },
	toPatch:      func(name, body *string) db.WorkerTemplatePatch { return db.WorkerTemplatePatch{Name: name, Body: body} },
	fields: templateEntityFields[db.WorkerTemplate]{
		id:   func(t *db.WorkerTemplate) int64 { return t.ID },
		name: func(t *db.WorkerTemplate) string { return t.Name },
		nkey: func(t *db.WorkerTemplate) string { return t.NKey },
		body: func(t *db.WorkerTemplate) string { return t.Body },
	},
	listFn:       (*db.DB).ListWorkerTemplates,
	createFn:     (*db.DB).CreateWorkerTemplate,
	patchFn:      (*db.DB).PatchWorkerTemplate,
	deleteFn:     (*db.DB).DeleteWorkerTemplate,
	setDefaultFn: (*db.DB).SetDefaultWorkerTemplate,
}

// pgListWorkerTemplates GET /api/worker-templates → {"templates": [...]}
func (s *Server) pgListWorkerTemplates(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.list(s, w, r)
}

// pgCreateWorkerTemplate POST /api/worker-templates → 201 模板对象；nkey 重名 409；试渲染失败 400。
func (s *Server) pgCreateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.create(s, w, r)
}

// pgUpdateWorkerTemplate PATCH /api/worker-templates/{id}；内置正文可改，名称/nkey 保护；body 非空才试渲染。
func (s *Server) pgUpdateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.update(s, w, r)
}

// pgDeleteWorkerTemplate DELETE /api/worker-templates/{id}；内置/默认 409 禁删。
// 已选用它的任务不受影响（创建时正文已快照进 tasks.worker_prompt_override，D11-b）。
func (s *Server) pgDeleteWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.delete(s, w, r)
}

// pgSetDefaultWorkerTemplate POST /api/worker-templates/{id}/default → 200 更新后的模板对象。
func (s *Server) pgSetDefaultWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.setDefault(s, w, r)
}

// 【二开·P-TPL·S11】起 —— 试渲染干跑端点：U8「试渲染校验」按钮专用（与 create/update 同标准）。
func (s *Server) pgValidateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	workerTemplatesAPI.validateDryRun(s, w, r)
}

// 【二开·P-TPL·S11】止

// planner_templates.go：planner 模板试渲染校验与 /api/planner-templates 服务层参数。
// 主要函数/类型：
//   - validatePlannerTemplateBody：校验 planner 模板体（试渲染干跑）。
//   - pgListPlannerTemplates / pgCreatePlannerTemplate / pgUpdatePlannerTemplate / pgDeletePlannerTemplate：模板库增删改查的服务层入口。
//   - pgSetDefaultPlannerTemplate：设置默认 planner 模板。
//   - pgValidatePlannerTemplate：模板试渲染校验端点入口。

// 【二开·P-TPL·S11 planner 模板库服务层】
// 功能介绍：planner 模板试渲染校验（P2 先落地，供 S8 spawn_task 模板 id 分支复用；
//
//	P3 起 /api/planner-templates 五端点 + 试渲染干跑端点经 plannerTemplatesAPI
//	（template_handlers.go 参数化实现）挂出；本文件只保留 planner 侧参数与差异项）。
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

// plannerTemplateVarsHint 合法变量目录文案 = 00 册 A4 PlannerVars 五字段（agent/prompt.go PlannerVars）；
// 后端校验、400 文案、前端 U8 变量 chips 三处同目录。
const plannerTemplateVarsHint = "{{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}"

const maxPlannerTemplateRequestBytes = 64 << 10 // 请求体上限（正文 32KB + JSON 开销余量）

// validatePlannerTemplateBody 试渲染校验——与 agent/prompt.go renderTmpl 同模式（missingkey=error）：
//  1. template.Parse 查语法：错误自带 `template: p:<行>:<列>` 定位（parse 一步与 worker 侧共享 parseValidateTemplate）；
//  2. 用一组全字段假 PlannerVars 执行：引用目录外变量（如 {{.Foo}}）在执行期报
//     `template: p:<行>:<列>: … can't evaluate field Foo in type agent.PlannerVars`。
//     32KB 上限在此把守（db 层 normalize 再兜一次，双保险）。
func validatePlannerTemplateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("模板正文不能为空")
	}
	if len(body) > db.MaxPlannerTemplateBodyBytes {
		return fmt.Errorf("模板正文最多 %d 字节（32KB），当前 %d 字节", db.MaxPlannerTemplateBodyBytes, len(body))
	}
	t, err := parseValidateTemplate(body)
	if err != nil {
		return fmt.Errorf("模板语法错误：%w", err)
	}
	vars := agent.PlannerVars{
		Goal:         "拿下 example.com 后台权限",
		Scope:        "example.com",
		AssetSummary: "站点 https://example.com（示例资产摘要）",
		DataDir:      "/tmp/artex-validate",
		Now:          "2026-10-21 12:00",
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return fmt.Errorf("试渲染失败（可能引用了目录外变量；合法变量：%s）：%w", plannerTemplateVarsHint, err)
	}
	return nil
}

// plannerTemplatesAPI planner 侧模板库参数：哨兵 error、内置保护 409 文案、db 方法注入。
// 内置保护语义（PATCH 改名/DELETE → ErrPlannerTemplateBuiltin；正文可改 → 200）编码在
// db.PatchPlannerTemplate/DeletePlannerTemplate 里，与 worker 侧差异原样保留。
var plannerTemplatesAPI = &templateAPI[db.PlannerTemplate, db.PlannerTemplateInput, db.PlannerTemplatePatch]{
	kind:         "planner",
	maxReqBytes:  maxPlannerTemplateRequestBytes,
	maxNameRunes: db.MaxPlannerTemplateNameRunes,
	validateBody: validatePlannerTemplateBody,
	errInvalid:   db.ErrPlannerTemplateInvalid,
	errConflict:  db.ErrPlannerTemplateNameConflict,
	errBuiltin:   db.ErrPlannerTemplateBuiltin,
	errDefault:   db.ErrPlannerTemplateDefaultProtected,
	errNotFound:  db.ErrPlannerTemplateNotFound,
	builtinMsg:   "内置模板正文可编辑，但名称/nkey不可修改，且内置模板不能删除",
	toInput: func(name, body string) db.PlannerTemplateInput {
		return db.PlannerTemplateInput{Name: name, Body: body}
	},
	toPatch: func(name, body *string) db.PlannerTemplatePatch {
		return db.PlannerTemplatePatch{Name: name, Body: body}
	},
	fields: templateEntityFields[db.PlannerTemplate]{
		id:   func(t *db.PlannerTemplate) int64 { return t.ID },
		name: func(t *db.PlannerTemplate) string { return t.Name },
		nkey: func(t *db.PlannerTemplate) string { return t.NKey },
		body: func(t *db.PlannerTemplate) string { return t.Body },
	},
	listFn:       (*db.DB).ListPlannerTemplates,
	createFn:     (*db.DB).CreatePlannerTemplate,
	patchFn:      (*db.DB).PatchPlannerTemplate,
	deleteFn:     (*db.DB).DeletePlannerTemplate,
	setDefaultFn: (*db.DB).SetDefaultPlannerTemplate,
}

// pgListPlannerTemplates GET /api/planner-templates → {"templates": [...]}
func (s *Server) pgListPlannerTemplates(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.list(s, w, r)
}

// pgCreatePlannerTemplate POST /api/planner-templates → 201 模板对象；nkey 重名 409；试渲染失败 400。
func (s *Server) pgCreatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.create(s, w, r)
}

// pgUpdatePlannerTemplate PATCH /api/planner-templates/{id}；内置行名称保护（改名 → 409）；body 非空才试渲染。
func (s *Server) pgUpdatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.update(s, w, r)
}

// pgDeletePlannerTemplate DELETE /api/planner-templates/{id}；内置/默认 409 禁删。
// 已选用它的任务不受影响（创建时正文已快照进 tasks.planner_prompt_override，D11-b）。
func (s *Server) pgDeletePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.delete(s, w, r)
}

// pgSetDefaultPlannerTemplate POST /api/planner-templates/{id}/default → 200 更新后的模板对象。
func (s *Server) pgSetDefaultPlannerTemplate(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.setDefault(s, w, r)
}

// 【二开·P-TPL·S11】起 —— 试渲染干跑端点：U8「试渲染校验」按钮专用（与 create/update 同标准）。
func (s *Server) pgValidatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	plannerTemplatesAPI.validateDryRun(s, w, r)
}

// 【二开·P-TPL·S11】止

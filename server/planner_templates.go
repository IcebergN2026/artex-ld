// ═══ ARTEX 二开 · [P-TPL·S11 planner 模板库服务层] ═══
// 功能介绍：planner 模板试渲染校验（P2 先落地，供 S8 spawn_task 模板 id 分支复用；
//
//	P3 在本文件追加 /api/planner-templates 五端点 + 试渲染干跑端点）。
//
// 二开日期：2026-10-21
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log" // 【二开·P-LOG·S11】INFO/WARN 级直接标准库 log（04 册 L1）
	"net/http"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/xlog"
)

// plannerTemplateVarsHint 合法变量目录文案 = 00 册 A4 PlannerVars 五字段（agent/prompt.go PlannerVars）；
// 后端校验、400 文案、前端 U8 变量 chips 三处同目录。
const plannerTemplateVarsHint = "{{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}"

const maxPlannerTemplateRequestBytes = 64 << 10 // 请求体上限（正文 32KB + JSON 开销余量）

// plannerTemplateRequest 指针字段区分 PATCH 省略与显式空值（空值仍被 db 层校验拒绝）。
type plannerTemplateRequest struct {
	Name *string `json:"name"`
	Body *string `json:"body"`
}

// decodePlannerTemplateRequest 与 decodeTaskTemplateRequest 同款：MaxBytesReader 限长读体后解码。
func decodePlannerTemplateRequest(w http.ResponseWriter, r *http.Request, req *plannerTemplateRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPlannerTemplateRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return false
	}
	if err := json.Unmarshal(body, req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// validatePlannerTemplateBody 试渲染校验——与 agent/prompt.go renderTmpl 同模式（missingkey=error）：
//  1. template.Parse 查语法：错误自带 `template: p:<行>:<列>` 定位；
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
	t, err := template.New("p").Option("missingkey=error").Parse(body)
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

// writePlannerTemplateErr 哨兵错 → 状态码映射（仿 writeTaskTemplateErr，多两个 409 保护）。
func writePlannerTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrPlannerTemplateInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrPlannerTemplateNameConflict):
		writeErr(w, http.StatusConflict, "模板名称已存在")
	case errors.Is(err, db.ErrPlannerTemplateBuiltin):
		writeErr(w, http.StatusConflict, "内置模板正文可编辑，但名称/nkey不可修改，且内置模板不能删除")
	case errors.Is(err, db.ErrPlannerTemplateDefaultProtected):
		writeErr(w, http.StatusConflict, "默认模板不能删除，请先把默认让给别的模板")
	case errors.Is(err, db.ErrPlannerTemplateNotFound):
		writeErr(w, http.StatusNotFound, "planner template not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// pgListPlannerTemplates GET /api/planner-templates → {"templates": [...]}
func (s *Server) pgListPlannerTemplates(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	templates, err := pg.ListPlannerTemplates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记列表（排障：前端下拉数据哪来的）。
	xlog.Debugf("[tpl] planner-templates list：共 %d 个", len(templates))
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

// pgCreatePlannerTemplate POST /api/planner-templates → 201 模板对象；nkey 重名 409；试渲染失败 400。
func (s *Server) pgCreatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req plannerTemplateRequest
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > db.MaxPlannerTemplateNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", db.MaxPlannerTemplateNameRunes))
		return
	}
	if err := validatePlannerTemplateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tpl, err := pg.CreatePlannerTemplate(db.PlannerTemplateInput{
		Name: stringValue(req.Name),
		Body: stringValue(req.Body),
	})
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记创建（排障：这个模板什么时候进来的）。
	log.Printf("[tpl] planner 模板创建：#%d %q（nkey=%s，%dB）", tpl.ID, tpl.Name, tpl.NKey, len(tpl.Body))
	writeJSON(w, http.StatusCreated, tpl)
}

// pgUpdatePlannerTemplate PATCH /api/planner-templates/{id}；内置行 409（只读）；body 非空才试渲染。
func (s *Server) pgUpdatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad planner template id")
		return
	}
	var req plannerTemplateRequest
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if req.Name == nil && req.Body == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name 或 body")
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > db.MaxPlannerTemplateNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", db.MaxPlannerTemplateNameRunes))
		return
	}
	if req.Body != nil {
		if err := validatePlannerTemplateBody(*req.Body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	tpl, err := pg.PatchPlannerTemplate(id, db.PlannerTemplatePatch{Name: req.Name, Body: req.Body})
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记更新（审计：谁改了提示词）。
	log.Printf("[tpl] planner 模板更新：#%d %q", tpl.ID, tpl.Name)
	writeJSON(w, http.StatusOK, tpl)
}

// pgDeletePlannerTemplate DELETE /api/planner-templates/{id}；内置/默认 409 禁删。
// 已选用它的任务不受影响（创建时正文已快照进 tasks.planner_prompt_override，D11-b）。
func (s *Server) pgDeletePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad planner template id")
		return
	}
	deleted, err := pg.DeletePlannerTemplate(id)
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "planner template not found")
		return
	}
	// 【二开·P-LOG·S11】WARN 记删除（排障：模板怎么没了；已选用任务不受影响=快照语义 D11-b）。
	log.Printf("[tpl] planner 模板删除 ⚠ #%d（已选用任务持有快照不受影响）", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// pgSetDefaultPlannerTemplate POST /api/planner-templates/{id}/default → 200 更新后的模板对象。
func (s *Server) pgSetDefaultPlannerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad planner template id")
		return
	}
	tpl, err := pg.SetDefaultPlannerTemplate(id)
	if err != nil {
		writePlannerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记默认切换（排障：默认模板什么时候换的）。
	log.Printf("[tpl] planner 默认模板切换 → #%d %q", tpl.ID, tpl.Name)
	writeJSON(w, http.StatusOK, tpl)
}

// 【二开·P-TPL·S11】起 —— 试渲染干跑：不落库，与 create/update 同标准（validatePlannerTemplateBody），
// U8「试渲染校验」按钮专用；不查库，无需 s.pg(w)。
func (s *Server) pgValidatePlannerTemplate(w http.ResponseWriter, r *http.Request) {
	var req plannerTemplateRequest
	if !decodePlannerTemplateRequest(w, r, &req) {
		return
	}
	if err := validatePlannerTemplateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记试渲染（高频，默认关）。
	xlog.Debugf("[tpl] planner 模板试渲染通过（%dB）", len(stringValue(req.Body)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 【二开·P-TPL·S11】止

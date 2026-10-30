// ═══ ARTEX 二开 · [P-TPL·S11 worker 模板库服务层] ═══
// 功能介绍：worker 模板试渲染校验（P2 先落地，供 S8 spawn_task 模板 id 分支复用；
//
//	P3 在本文件追加 /api/worker-templates 五端点 + 试渲染干跑端点）。
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

// workerTemplateVarsHint 合法变量目录文案 = 00 册 A4 WorkerVars 五字段（agent/prompt.go WorkerVars）；
// 后端校验、400 文案、前端 U8 变量 chips 三处同目录。
const workerTemplateVarsHint = "{{.ProxyAddr}} {{.WorkerName}} {{.DataDir}} {{.Now}}"

const maxWorkerTemplateRequestBytes = 64 << 10 // 请求体上限（正文 32KB + JSON 开销余量）

// workerTemplateRequest 指针字段区分 PATCH 省略与显式空值（空值仍被 db 层校验拒绝）。
type workerTemplateRequest struct {
	Name *string `json:"name"`
	Body *string `json:"body"`
}

// decodeWorkerTemplateRequest 与 decodeTaskTemplateRequest 同款：MaxBytesReader 限长读体后解码。
func decodeWorkerTemplateRequest(w http.ResponseWriter, r *http.Request, req *workerTemplateRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerTemplateRequestBytes)
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

// validateWorkerTemplateBody 试渲染校验——与 agent/prompt.go renderTmpl 同模式（missingkey=error）：
//  1. template.Parse 查语法：错误自带 `template: p:<行>:<列>` 定位；
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
	t, err := template.New("p").Option("missingkey=error").Parse(body)
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

// writeWorkerTemplateErr 哨兵错 → 状态码映射（仿 writeTaskTemplateErr，多两个 409 保护）。
func writeWorkerTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrWorkerTemplateInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrWorkerTemplateNameConflict):
		writeErr(w, http.StatusConflict, "模板名称已存在")
	case errors.Is(err, db.ErrWorkerTemplateBuiltin):
		writeErr(w, http.StatusConflict, "内置模板正文可编辑，但名称/nkey 不可修改，且内置模板不能删除")
	case errors.Is(err, db.ErrWorkerTemplateDefaultProtected):
		writeErr(w, http.StatusConflict, "默认模板不能删除，请先把默认让给别的模板")
	case errors.Is(err, db.ErrWorkerTemplateNotFound):
		writeErr(w, http.StatusNotFound, "worker template not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// pgListWorkerTemplates GET /api/worker-templates → {"templates": [...]}
func (s *Server) pgListWorkerTemplates(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	templates, err := pg.ListWorkerTemplates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记列表（排障：前端下拉数据哪来的）。
	xlog.Debugf("[tpl] worker-templates list：共 %d 个", len(templates))
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

// pgCreateWorkerTemplate POST /api/worker-templates → 201 模板对象；nkey 重名 409；试渲染失败 400。
func (s *Server) pgCreateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req workerTemplateRequest
	if !decodeWorkerTemplateRequest(w, r, &req) {
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > db.MaxWorkerTemplateNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", db.MaxWorkerTemplateNameRunes))
		return
	}
	if err := validateWorkerTemplateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tpl, err := pg.CreateWorkerTemplate(db.WorkerTemplateInput{
		Name: stringValue(req.Name),
		Body: stringValue(req.Body),
	})
	if err != nil {
		writeWorkerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记创建（排障：这个模板什么时候进来的）。
	log.Printf("[tpl] worker 模板创建：#%d %q（nkey=%s，%dB）", tpl.ID, tpl.Name, tpl.NKey, len(tpl.Body))
	writeJSON(w, http.StatusCreated, tpl)
}

// pgUpdateWorkerTemplate PATCH /api/worker-templates/{id}；内置正文可改，名称/nkey 保护；body 非空才试渲染。
func (s *Server) pgUpdateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad worker template id")
		return
	}
	var req workerTemplateRequest
	if !decodeWorkerTemplateRequest(w, r, &req) {
		return
	}
	if req.Name == nil && req.Body == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name 或 body")
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > db.MaxWorkerTemplateNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", db.MaxWorkerTemplateNameRunes))
		return
	}
	if req.Body != nil {
		if err := validateWorkerTemplateBody(*req.Body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	tpl, err := pg.PatchWorkerTemplate(id, db.WorkerTemplatePatch{Name: req.Name, Body: req.Body})
	if err != nil {
		writeWorkerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记更新（审计：谁改了提示词）。
	log.Printf("[tpl] worker 模板更新：#%d %q", tpl.ID, tpl.Name)
	writeJSON(w, http.StatusOK, tpl)
}

// pgDeleteWorkerTemplate DELETE /api/worker-templates/{id}；内置/默认 409 禁删。
// 已选用它的任务不受影响（创建时正文已快照进 tasks.worker_prompt_override，D11-b）。
func (s *Server) pgDeleteWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad worker template id")
		return
	}
	deleted, err := pg.DeleteWorkerTemplate(id)
	if err != nil {
		writeWorkerTemplateErr(w, err)
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "worker template not found")
		return
	}
	// 【二开·P-LOG·S11】WARN 记删除（排障：模板怎么没了；已选用任务不受影响=快照语义 D11-b）。
	log.Printf("[tpl] worker 模板删除 ⚠ #%d（已选用任务持有快照不受影响）", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// pgSetDefaultWorkerTemplate POST /api/worker-templates/{id}/default → 200 更新后的模板对象。
func (s *Server) pgSetDefaultWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad worker template id")
		return
	}
	tpl, err := pg.SetDefaultWorkerTemplate(id)
	if err != nil {
		writeWorkerTemplateErr(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记默认切换（排障：默认模板什么时候换的）。
	log.Printf("[tpl] worker 默认模板切换 → #%d %q", tpl.ID, tpl.Name)
	writeJSON(w, http.StatusOK, tpl)
}

// 【二开·P-TPL·S11】起 —— 试渲染干跑：不落库，与 create/update 同标准（validateWorkerTemplateBody），
// U8「试渲染校验」按钮专用；不查库，无需 s.pg(w)。
func (s *Server) pgValidateWorkerTemplate(w http.ResponseWriter, r *http.Request) {
	var req workerTemplateRequest
	if !decodeWorkerTemplateRequest(w, r, &req) {
		return
	}
	if err := validateWorkerTemplateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记试渲染（高频，默认关）。
	xlog.Debugf("[tpl] worker 模板试渲染通过（%dB）", len(stringValue(req.Body)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 【二开·P-TPL·S11】止

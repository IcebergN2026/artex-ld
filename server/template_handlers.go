// template_handlers.go：planner/worker 模板库同构 handler 的参数化共享实现。
// 主要函数/类型：
//   - templateAPI / templateEntityFields：参数化 handler 组，planner/worker 差异全部经参数注入。
//   - decode / writeErrM：限长请求体解码与错误映射。
//   - list / create / update / delete / setDefault：模板五端点的通用实现。
//   - validateDryRun：模板试渲染干跑校验端点。
//   - parseValidateTemplate：text/template 解析（missingkey=error），两侧校验共享。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"text/template"
	"unicode/utf8"

	"artex/db"
	"artex/xlog"
)

// ═══ 模板库服务层（planner/worker 参数化共享实现）═══
// planner_templates.go 与 worker_templates.go 的九组同构 handler 合并为本文件的参数化实现；
// 差异项（kind 日志前缀、哨兵 error、内置保护 409 文案、name 长度上限、db 方法）全部经参数注入。

// templateEntity 是 planner/worker 模板行的联合约束（db 公开类型不变）。
type templateEntity interface {
	db.PlannerTemplate | db.WorkerTemplate
}

// tplRequest 指针字段区分 PATCH 省略与显式空值（空值仍被 db 层校验拒绝）。
type tplRequest struct {
	Name *string `json:"name"`
	Body *string `json:"body"`
}

// templateAPI 参数化 planner/worker 模板库 handler 组。
// 内置模板保护策略在传入的 db 方法里（patchFn/deleteFn 各自编码本侧 builtin 语义，
// 经 errBuiltin 映射到本侧 409 文案），planner 与 worker 的差异（含 409 文案逐字节不同）原样保留。
type templateAPI[T templateEntity, In any, P any] struct {
	kind         string // "planner" / "worker"：日志行与错误文案前缀
	maxReqBytes  int64  // 请求体上限（正文 32KB + JSON 开销余量）
	maxNameRunes int    // name 长度上限（db 层同口径再兜一次）
	validateBody func(string) error
	errInvalid   error
	errConflict  error
	errBuiltin   error
	errDefault   error
	errNotFound  error
	builtinMsg   string // 内置保护 409 文案（两侧逐字节不同，参数注入）
	toInput      func(name, body string) In
	toPatch      func(name, body *string) P
	fields       templateEntityFields[T]
	listFn       func(*db.DB) ([]*T, error)
	createFn     func(*db.DB, In) (*T, error)
	patchFn      func(*db.DB, int64, P) (*T, error)
	deleteFn     func(*db.DB, int64) (bool, error)
	setDefaultFn func(*db.DB, int64) (*T, error)
}

func (a *templateAPI[T, In, P]) notFoundMsg() string {
	return fmt.Sprintf("%s template not found", a.kind)
}

// templateEntityFields 经注入的访问器取公共字段（Go 不允许对类型参数取字段，
// 访问器在两侧 api 定义处注入，见 planner_templates.go / worker_templates.go）。
type templateEntityFields[T any] struct {
	id   func(*T) int64
	name func(*T) string
	nkey func(*T) string
	body func(*T) string
}

// parseValidateTemplate text/template parse + missingkey=error——planner/worker 试渲染校验唯一共享的一步
// （两侧校验机制不同：本层是执行期试渲染，server_mgmt.go validateTemplate 是静态白名单，各留各的）。
func parseValidateTemplate(body string) (*template.Template, error) {
	return template.New("p").Option("missingkey=error").Parse(body)
}

// decode MaxBytesReader 限长读体后解码。
func (a *templateAPI[T, In, P]) decode(w http.ResponseWriter, r *http.Request, req *tplRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxReqBytes)
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

// writeErrM 哨兵错 → 状态码映射（错误文案两侧逐字节不同处经参数注入）。
func (a *templateAPI[T, In, P]) writeErrM(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, a.errInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, a.errConflict):
		writeErr(w, http.StatusConflict, "模板名称已存在")
	case errors.Is(err, a.errBuiltin):
		writeErr(w, http.StatusConflict, a.builtinMsg)
	case errors.Is(err, a.errDefault):
		writeErr(w, http.StatusConflict, "默认模板不能删除，请先把默认让给别的模板")
	case errors.Is(err, a.errNotFound):
		writeErr(w, http.StatusNotFound, a.notFoundMsg())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// list GET /api/{kind}-templates → {"templates": [...]}
func (a *templateAPI[T, In, P]) list(s *Server, w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	templates, err := a.listFn(pg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记列表（排障：前端下拉数据哪来的）。
	xlog.Debugf("[tpl] %s-templates list：共 %d 个", a.kind, len(templates))
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

// create POST /api/{kind}-templates → 201 模板对象；nkey 重名 409；试渲染失败 400。
func (a *templateAPI[T, In, P]) create(s *Server, w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req tplRequest
	if !a.decode(w, r, &req) {
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > a.maxNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", a.maxNameRunes))
		return
	}
	if err := a.validateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tpl, err := a.createFn(pg, a.toInput(stringValue(req.Name), stringValue(req.Body)))
	if err != nil {
		a.writeErrM(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记创建（排障：这个模板什么时候进来的）。
	id, name, nkey, body := a.fields.id(tpl), a.fields.name(tpl), a.fields.nkey(tpl), a.fields.body(tpl)
	log.Printf("[tpl] %s 模板创建：#%d %q（nkey=%s，%dB）", a.kind, id, name, nkey, len(body))
	writeJSON(w, http.StatusCreated, tpl)
}

// update PATCH /api/{kind}-templates/{id}；内置保护策略在 db 层方法（patchFn）里；body 非空才试渲染。
func (a *templateAPI[T, In, P]) update(s *Server, w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("bad %s template id", a.kind))
		return
	}
	var req tplRequest
	if !a.decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Body == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name 或 body")
		return
	}
	if req.Name != nil && utf8.RuneCountInString(strings.Join(strings.Fields(*req.Name), " ")) > a.maxNameRunes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 最多 %d 个字符", a.maxNameRunes))
		return
	}
	if req.Body != nil {
		if err := a.validateBody(*req.Body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	tpl, err := a.patchFn(pg, id, a.toPatch(req.Name, req.Body))
	if err != nil {
		a.writeErrM(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记更新（审计：谁改了提示词）。
	id, name := a.fields.id(tpl), a.fields.name(tpl)
	log.Printf("[tpl] %s 模板更新：#%d %q", a.kind, id, name)
	writeJSON(w, http.StatusOK, tpl)
}

// delete DELETE /api/{kind}-templates/{id}；内置/默认禁删（db 层方法编码）。
// 已选用它的任务不受影响（创建时正文已快照进 tasks.*_prompt_override，D11-b）。
func (a *templateAPI[T, In, P]) delete(s *Server, w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("bad %s template id", a.kind))
		return
	}
	deleted, err := a.deleteFn(pg, id)
	if err != nil {
		a.writeErrM(w, err)
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, a.notFoundMsg())
		return
	}
	// 【二开·P-LOG·S11】WARN 记删除（排障：模板怎么没了；已选用任务不受影响=快照语义 D11-b）。
	log.Printf("[tpl] %s 模板删除 ⚠ #%d（已选用任务持有快照不受影响）", a.kind, id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// setDefault POST /api/{kind}-templates/{id}/default → 200 更新后的模板对象。
func (a *templateAPI[T, In, P]) setDefault(s *Server, w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("bad %s template id", a.kind))
		return
	}
	tpl, err := a.setDefaultFn(pg, id)
	if err != nil {
		a.writeErrM(w, err)
		return
	}
	// 【二开·P-LOG·S11】INFO 记默认切换（排障：默认模板什么时候换的）。
	id, name := a.fields.id(tpl), a.fields.name(tpl)
	log.Printf("[tpl] %s 默认模板切换 → #%d %q", a.kind, id, name)
	writeJSON(w, http.StatusOK, tpl)
}

// validateDryRun 试渲染干跑：不落库，与 create/update 同标准；U8「试渲染校验」按钮专用；不查库，无需 s.pg(w)。
func (a *templateAPI[T, In, P]) validateDryRun(_ *Server, w http.ResponseWriter, r *http.Request) {
	var req tplRequest
	if !a.decode(w, r, &req) {
		return
	}
	if err := a.validateBody(stringValue(req.Body)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 【二开·P-LOG·S11】DEBUG 记试渲染（高频，默认关）。
	xlog.Debugf("[tpl] %s 模板试渲染通过（%dB）", a.kind, len(stringValue(req.Body)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

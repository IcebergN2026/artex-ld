// jsonbody.go：HTTP JSON 请求体的唯一限长解码入口。
// 主要函数/类型：
//   - decodeJSON / decodeJSONOptional / decodeJSONBounded：限长解码一个 JSON 值。
//   - readJSONBody：需要原始 body（顶层键集、自定义 Unmarshal）时的限长读取。
//   - writeJSONBodyError / writeJSONBodyTooLarge：MaxBytesError→413、其余非法 JSON→400 的统一映射。

package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// 请求体上限分档。所有 JSON 入口必须显式选择一档（或保留端点自己的更小常量），
// 不允许再出现无界 json.NewDecoder(r.Body)。上限按现有合法 payload 预留了 JSON
// 语法与转义开销的余量。
// 兼容例外：multipart 上传入口（聊天附件、工作区上传、技能 zip）不属于 JSON body，
// 各自保留既有的 MaxBytesReader 上限，不经过本文件的错误映射。
const (
	// maxAuthJSONBodyBytes：无认证或凭据类端点（/api/auth/login、/api/auth/init、
	// change-password）。密码本身受 validatePassword 72 字节限制，16 KiB 足够且能把
	// 超大 chunked 请求挡在内存之外。
	maxAuthJSONBodyBytes = 16 << 10
	// maxSmallJSONBodyBytes：控制、开关、单个 id、布尔标记等小对象。
	maxSmallJSONBodyBytes = 64 << 10
	// maxBatchJSONBodyBytes：批量 id 列表（100 条量级）与批量操作请求。
	maxBatchJSONBodyBytes = 256 << 10
	// maxMediumJSONBodyBytes：规则、提示词、agent/LLM/MCP 配置等中等对象。
	maxMediumJSONBodyBytes = 512 << 10
	// maxLargeJSONBodyBytes：资产批量导入、流量记录、自定义工具定义等大对象。
	maxLargeJSONBodyBytes = 4 << 20
	// maxFileWriteJSONBodyBytes：工作区/技能源码写入与技能创建正文。
	maxFileWriteJSONBodyBytes = 10 << 20
)

// decodeJSONBounded bounds r.Body to maxBytes and decodes exactly one JSON value.
// It never writes a response; callers map the error through writeJSONBodyError (or
// their own message when the endpoint has a documented legacy wording).
// Trailing values are deliberately tolerated here: only endpoints that document
// strict single-value semantics (deleteCompany) check for them explicitly, so
// existing lenient clients keep working. An empty body yields io.EOF.
func decodeJSONBounded(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	return json.NewDecoder(r.Body).Decode(v)
}

// decodeJSON bounds and decodes one required JSON value. On failure it writes
// exactly one error response (413 for oversized bodies, 400 otherwise).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	if err := decodeJSONBounded(w, r, v, maxBytes); err != nil {
		writeJSONBodyError(w, err)
		return false
	}
	return true
}

// decodeJSONOptional is decodeJSON with an empty body treated as success. The
// zero value of v is left untouched, matching the historical "empty body = keep
// defaults" contract of the endpoints that use it.
func decodeJSONOptional(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	if err := decodeJSONBounded(w, r, v, maxBytes); err != nil && !errors.Is(err, io.EOF) {
		writeJSONBodyError(w, err)
		return false
	}
	return true
}

// readJSONBody bounds and reads the whole request body. Callers that need the
// raw bytes (top-level key detection, custom unmarshalers) use it and then
// unmarshal themselves; read failures go through writeJSONBodyError.
func readJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	return io.ReadAll(r.Body)
}

// writeJSONBodyTooLarge writes 413 and reports true when err is a size-limit
// error. Other errors are left to the caller so endpoints with legacy wording
// (e.g. auth's 密码不能为空) can keep their message without double-writing.
func writeJSONBodyTooLarge(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		return true
	}
	return false
}

// writeJSONBodyError maps a bounded JSON read/decode failure to one response:
// oversized bodies are 413, empty required bodies and malformed JSON are 400.
func writeJSONBodyError(w http.ResponseWriter, err error) {
	if writeJSONBodyTooLarge(w, err) {
		return
	}
	if errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "请求正文为空")
		return
	}
	writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
}

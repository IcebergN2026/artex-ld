package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request struct {
		Name          *string `json:"name"`
		Pinned        *bool   `json:"pinned"`
		ReviewAuto    *bool   `json:"review_auto"`    // 【二开·P-REVIEW·D2b】自动审核开关
		ReviewEnabled *bool   `json:"review_enabled"` // 【二开·P-REVIEW·D2c】任务级审核总开关（m03934）
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if request.Name == nil && request.Pinned == nil && request.ReviewAuto == nil && request.ReviewEnabled == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name、pinned、review_auto 或 review_enabled")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "任务名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("任务名称最多 %d 个字符", maxTaskNameRunes))
			return
		}
		request.Name = &name
	}
	// 【二开·审核流 §2.9.6】关闭审核前必须清空待审队列（pending）与待修订稿（rejected）：
	// 任一存在时返回 409 + 数量，要求先批准、修订重送或显式取消；否则 ReviewRunner 停用后
	// 会留下再也无法处置的孤儿稿。开启审核不校验（不回改已有 open，见 §10.1）。
	if request.ReviewEnabled != nil && !*request.ReviewEnabled {
		if t, ok := s.m.Task(taskID); ok && t.Store != nil {
			pend, _ := t.Store.ProposedIntents(1000)
			rej, _ := t.Store.RejectedIntents(1000)
			if len(pend)+len(rej) > 0 {
				writeErr(w, http.StatusConflict, fmt.Sprintf(
					"仍有 %d 条待审、%d 条待修订意图：请先批准、修订重送或显式取消（cancel_intent）后再关闭审核",
					len(pend), len(rej)))
				return
			}
		}
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{Name: request.Name, Pinned: request.Pinned, ReviewAuto: request.ReviewAuto, ReviewEnabled: request.ReviewEnabled})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}

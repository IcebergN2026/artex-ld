package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/agentcore"
)

func attachSideCapture(ctx context.Context, opts *agentcore.Options) context.Context {
	ri := RunInfoFrom(ctx)
	p := sidequestion.Parent{TaskID: ri.TaskID, ExplorationID: ri.ExplorationID, IntentID: ri.IntentID}
	if strings.HasPrefix(ri.SessionID, "conv-") {
		p.ConversationID, _ = strconv.ParseInt(strings.TrimPrefix(ri.SessionID, "conv-"), 10, 64)
	}
	if p.ConversationID > 0 {
		// 会话型与任务型二选一（side_question_sessions CHECK）：触发运行同时带
		// taskID（P-CLEAN 168e6d1 注入）与 conv- 会话 id，两列同非空会 23514。
		// 会话型优先，与 server 端 sideParent(kind=conversation) 语义一致。
		p.TaskID, p.ExplorationID, p.IntentID = 0, 0, 0
	}
	if p.ConversationID == 0 && (p.TaskID == 0 || p.ExplorationID == 0) {
		return ctx
	}
	ctx, opts.Deps = sidequestion.Attach(ctx, p, opts.Deps, opts.Provider)
	return ctx
}

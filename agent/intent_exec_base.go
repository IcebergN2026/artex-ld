// intent_exec_base.go：worker 与自定义执行器共用的执行装配公共块（去重重构抽出的单源）。
// 主要函数/类型：
//   - baseAgentOptions：两侧共用的 agentcore.Options 公共块（权限旁路、记录代理、文件边界、溢出目录、会话待办）。
//   - wrapIntentEmit：包装 emit，给每条活动打上意图 id 与执行者名，活动流可追溯执行者。

package agent

import (
	"artex/db"
	agentcore "norma/agentcore"
	"norma/permission"
	actool "norma/tool"
)

// baseAgentOptions 返回 worker 与自定义执行器共用的 agentcore.Options 公共块：
// 权限旁路、WebFetch 走记录代理、Bash 子进程代理环境、工作目录/文件边界、大输出溢出目录、会话待办。
// 两侧各自的差异字段（Provider/SystemPrompt/Tools/预算/Settlement/webSearch/Compaction 等）由调用侧显式覆写。
func baseAgentOptions(runDir, workDir, proxyAddr, proxyCACert string) agentcore.Options {
	return agentcore.Options{
		PermissionMode: permission.ModeBypass,
		// WebFetch/Bash 走记录代理（与另一侧同款留痕语义）。
		EnableWebFetch: true,
		WebFetchProxy:  proxyAddr,
		WebFetchCACert: proxyCACert,
		BashEnv:        proxyEnv(proxyAddr, proxyCACert),
		WorkingDir:     runDir,
		Root:           workDir, // 文件工具硬边界（同另一侧）
		ToolOutputDir:  cmdOutDir(runDir),
		Todos:          actool.NewTodoStore(),
	}
}

// wrapIntentEmit 包装 emit：打上意图 id 与执行者名，活动流可追溯是谁跑的（worker 与自定义执行器逐行相同的公共逻辑）。
func wrapIntentEmit(emit func(db.Activity), workerName string, intentID int64) func(db.Activity) {
	return func(a db.Activity) {
		if emit != nil {
			a.NodeID, a.Worker = &intentID, workerName
			emit(a)
		}
	}
}

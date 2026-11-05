// task_files.go：枚举任务归档/暂存时要打包的工作区文件目标。
// 主要函数/类型：
//   - taskWorkspaceTargets：列出 tasks/<taskID> 目录与对应 exploration 的 transcript 文件。
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// taskWorkspaceTargets 枚举任务工作区文件目标：tasks/<taskID>（os.Lstat 存在才含，其他错误上抛）
// + transcripts/ 下 exp<explorationID>- 前缀项（目录或 .jsonl 后缀；ReadDir 不存在视为空）。
// 尾杠是关键：exploration 12 不得匹配 exp123-。stageTaskFiles 与 stageTaskArchiveFiles 共用。
func taskWorkspaceTargets(dataDir, taskID string, explorationID int64) ([]string, error) {
	var targets []string
	taskDir := filepath.Join(dataDir, "tasks", taskID)
	if _, err := os.Lstat(taskDir); err == nil {
		targets = append(targets, taskDir)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	transcriptDir := filepath.Join(dataDir, "transcripts")
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		entries = nil
	}
	prefix := fmt.Sprintf("exp%d-", explorationID)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (!entry.IsDir() && !strings.HasSuffix(name, ".jsonl")) {
			continue
		}
		targets = append(targets, filepath.Join(transcriptDir, name))
	}
	return targets, nil
}

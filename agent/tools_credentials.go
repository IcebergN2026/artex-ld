// tools_credentials.go：资产凭据的 worker 写工具——record_credential 登记发现，
// mark_credential_verified 标记认证成功。
// 主要函数/类型：
//   - recordCredential：写凭据身份 + 发现资产的 discovered 关系（asset_id 须在任务范围内）。
//   - markCredentialVerified：写目标资产的 verified 关系（可跨资产；凭据须对任务可见）。
//
// 两个工具只绑定 worker（custom executor 经 WorkerTools 继承）；返回结果只含
// credential_id/asset_id/relation，绝不回显 secret。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"artex/db"
	actool "norma/tool"
)

// recordCredential 是 worker 的 record_credential 工具：把在【当前任务范围内】某
// 资产上发现的凭据登记入库（凭据身份 + 该资产的 discovered 关系）。
func (t *ToolSet) recordCredential() actool.CoreTool {
	return writeTool(
		"record_credential",
		"登记在指定资产上【发现】的凭据（来自文件、配置、内存、流量等）。\n"+
			"asset_id 是凭据的发现资产，必须在当前任务范围内（可先 list_assets 拿到 id）。\n"+
			"同一凭据重复登记会自动去重并刷新 last_seen；认证成功请改用 mark_credential_verified，不要重复调用本工具表达认证。\n"+
			"返回：{credential_id, asset_id, relation:\"discovered\"}，不回显 secret。",
		obj(map[string]any{
			"asset_id":  intp("凭据的发现资产 id（必填，须在任务范围内）"),
			"cred_type": str("凭据类型，如 password/hash/token/key/cookie（必填）"),
			"username":  str("账号名（可选）"),
			"domain":    str("账号所属域/租户，如 CORP（可选）"),
			"secret":    str("凭据本体：密码/哈希/token 等（必填，仅用于写入与去重，不会在任何读取中回显）"),
			"target":    str("该凭据适用的协议/服务，如 smb/rdp/ssh（可选）"),
			"source":    str("发现来源，如 文件路径/配置项/内存区域/流量会话（可选，建议填写）"),
			"extra": map[string]any{
				"type":        "object",
				"description": "附加结构化信息（可选，如 hash 类型、过期时间）",
			},
		}, "asset_id", "cred_type", "secret"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("record_credential 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("record_credential 需要任务上下文(当前无 task)"), nil
			}
			var a struct {
				AssetID  int64          `json:"asset_id"`
				CredType string         `json:"cred_type"`
				Username string         `json:"username"`
				Domain   string         `json:"domain"`
				Secret   string         `json:"secret"`
				Target   string         `json:"target"`
				Source   string         `json:"source"`
				Extra    map[string]any `json:"extra"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if a.AssetID <= 0 {
				return actool.Errorf("asset_id 不能为空"), nil
			}
			if strings.TrimSpace(a.CredType) == "" {
				return actool.Errorf("cred_type 不能为空"), nil
			}
			if strings.TrimSpace(a.Secret) == "" {
				return actool.Errorf("secret 不能为空"), nil
			}
			// 发现资产必须位于当前任务（含直接来源任务）范围内。
			assets, err := t.as.GetByIDsInScope(t.taskID, []int64{a.AssetID})
			if err != nil {
				return actool.Errorf("校验资产范围失败: " + err.Error()), nil
			}
			if len(assets) == 0 {
				return actool.Errorf(fmt.Sprintf("asset_id=%d 不在当前任务范围内，拒绝登记（请确认资产归属或先用 list_assets 查询）", a.AssetID)), nil
			}
			credentialID, err := t.as.Credentials().UpsertDiscovered(db.CredentialInput{
				CredType: a.CredType,
				Username: a.Username,
				Domain:   a.Domain,
				Secret:   a.Secret,
				Extra:    a.Extra,
			}, db.CredentialLinkInput{
				AssetID: a.AssetID,
				Target:  a.Target,
				Source:  a.Source,
			})
			if err != nil {
				return actool.Errorf("登记凭据失败: " + err.Error()), nil
			}
			t.writes.Assets++
			t.anchorOwner(a.AssetID)
			return jsonResult(map[string]any{
				"credential_id": credentialID,
				"asset_id":      a.AssetID,
				"relation":      db.CredentialRelationDiscovered,
			})
		},
	)
}

// markCredentialVerified 是 worker 的 mark_credential_verified 工具：把已发现的
// 凭据标记为在某台【任务范围内】资产上认证成功（写该资产的 verified 关系）。
// 认证资产可以是发现本机，也可以是另一台主机；发现关系保持不动。
func (t *ToolSet) markCredentialVerified() actool.CoreTool {
	return writeTool(
		"mark_credential_verified",
		"标记凭据在指定资产上【认证成功】。\n"+
			"credential_id 来自 record_credential 的返回或 list_assets 中资产 auth 条目的 id 字段。\n"+
			"asset_id 是【实际认证成功】的资产：可以是发现本机，也可以是另一台主机（横向移动验证），但都必须在当前任务范围内。\n"+
			"evidence 必填：记录认证成功的关键证据（如 命令输出片段/返回码/会话建立信息）。\n"+
			"本工具只增加目标资产的 verified 关系，不会移动或删除原资产的发现记录。\n"+
			"返回：{credential_id, asset_id, relation:\"verified\"}。",
		obj(map[string]any{
			"credential_id": intp("凭据 id（必填，来自 record_credential 返回或资产 auth 条目）"),
			"asset_id":      intp("实际认证成功的资产 id（必填，须在任务范围内；可与发现资产不同）"),
			"target":        str("认证成功的协议/服务，如 smb/rdp/ssh/URL（可选，建议填写）"),
			"evidence":      str("认证成功的关键证据（必填）：命令输出片段、返回码、会话信息等"),
		}, "credential_id", "asset_id", "evidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("mark_credential_verified 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("mark_credential_verified 需要任务上下文(当前无 task)"), nil
			}
			var a struct {
				CredentialID int64  `json:"credential_id"`
				AssetID      int64  `json:"asset_id"`
				Target       string `json:"target"`
				Evidence     string `json:"evidence"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			if a.CredentialID <= 0 {
				return actool.Errorf("credential_id 不能为空"), nil
			}
			if a.AssetID <= 0 {
				return actool.Errorf("asset_id 不能为空"), nil
			}
			if strings.TrimSpace(a.Evidence) == "" {
				return actool.Errorf("evidence 不能为空：请记录认证成功的关键证据（命令输出片段/返回码/会话信息等）"), nil
			}
			creds := t.as.Credentials()
			// 凭据必须对当前任务可见（其关联资产在任务范围内），防止按全局 ID 修改无关凭据。
			visible, err := creds.IsVisibleForTask(t.taskID, a.CredentialID)
			if err != nil {
				return actool.Errorf("校验凭据可见性失败: " + err.Error()), nil
			}
			if !visible {
				return actool.Errorf(fmt.Sprintf("credential_id=%d 与当前任务无关（其关联资产不在任务范围内），拒绝标记", a.CredentialID)), nil
			}
			// 认证目标资产必须位于当前任务（含直接来源任务）范围内。
			assets, err := t.as.GetByIDsInScope(t.taskID, []int64{a.AssetID})
			if err != nil {
				return actool.Errorf("校验资产范围失败: " + err.Error()), nil
			}
			if len(assets) == 0 {
				return actool.Errorf(fmt.Sprintf("asset_id=%d 不在当前任务范围内，拒绝标记认证（请确认资产归属）", a.AssetID)), nil
			}
			if err := creds.MarkVerified(a.CredentialID, a.AssetID, a.Target, a.Evidence); err != nil {
				return actool.Errorf("标记认证失败: " + err.Error()), nil
			}
			t.writes.Assets++
			t.anchorOwner(a.AssetID)
			return jsonResult(map[string]any{
				"credential_id": a.CredentialID,
				"asset_id":      a.AssetID,
				"relation":      db.CredentialRelationVerified,
			})
		},
	)
}

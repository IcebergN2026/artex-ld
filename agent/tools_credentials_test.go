package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"artex/db"
	actool "norma/tool"
)

// itoa 格式化 int64 用于拼接工具入参 JSON。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// credentialToolFixture 建一个任务和 ToolSet（绑定真实 AssetStore + taskID），
// 并用 insert_assets 登记一个 HTTP 服务资产（insertAssets 会自动把主机加入任务范围）。
func credentialToolFixture(t *testing.T, d *db.DB) (*ToolSet, int64, func()) {
	t.Helper()
	task, err := d.CreateTaskWithOptions("credential tool test", "credential scope", db.TaskCreateOptions{Name: "cred tool"})
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	cleanup := func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM assets WHERE root_domain='credtool-test.example' OR domain LIKE '%.credtool-test.example'`)
		_, _ = d.Exec(`DELETE FROM asset_credentials WHERE username LIKE 'credtool-%'`)
	}
	return ts, task.ID, cleanup
}

func insertServiceAsset(t *testing.T, ts *ToolSet, url string) int64 {
	t.Helper()
	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{map[string]any{"type": "service", "url": url}},
	})
	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Fatalf("insert %s errors: %v", url, errs)
	}
	results, _ := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("insert %s results=%v", url, out)
	}
	row, _ := results[0].(map[string]any)
	id, _ := row["id"].(float64)
	if id == 0 {
		t.Fatalf("insert %s returned id=0", url)
	}
	return int64(id)
}

// TestRecordCredentialWritesDiscovered：record_credential 写入发现资产并返回
// credential_id，结果不含 secret。
func TestRecordCredentialWritesDiscovered(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ts, _, cleanup := credentialToolFixture(t, d)
	defer cleanup()

	assetID := insertServiceAsset(t, ts, "http://a.credtool-test.example/")
	tool := ts.recordCredential()
	res, err := tool.Call(context.Background(), json.RawMessage(`{
		"asset_id": `+itoa(assetID)+`,
		"cred_type": "password",
		"username": "credtool-alice",
		"domain": "CORP",
		"secret": "sup3rsecret",
		"target": "smb",
		"source": "/etc/app/config.yml"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Flatten()
	if strings.Contains(text, "sup3rsecret") {
		t.Fatalf("tool result leaks secret: %s", text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal: %v raw=%s", err, text)
	}
	credID, _ := out["credential_id"].(float64)
	if credID == 0 || out["relation"] != "discovered" {
		t.Fatalf("unexpected result: %v", out)
	}
	// DB 断言：身份一行 + 该资产 discovered 关系。
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM asset_credentials WHERE username='credtool-alice' AND secret='sup3rsecret'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("credential rows=%d err=%v", n, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM asset_credential_links
WHERE credential_id=$1 AND asset_id=$2 AND relation='discovered' AND source='/etc/app/config.yml'`,
		int64(credID), assetID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("discovered links=%d err=%v", n, err)
	}
}

// TestMarkCredentialVerifiedCrossAsset：mark_credential_verified 可将已发现凭据
// 认证到任务内另一台资产。
func TestMarkCredentialVerifiedCrossAsset(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ts, _, cleanup := credentialToolFixture(t, d)
	defer cleanup()

	assetA := insertServiceAsset(t, ts, "http://a.credtool-test.example/")
	assetB := insertServiceAsset(t, ts, "http://b.credtool-test.example/")

	recRes, err := ts.recordCredential().Call(context.Background(), json.RawMessage(
		`{"asset_id": `+itoa(assetA)+`, "cred_type": "password", "username": "credtool-bob", "secret": "pw-of-bob"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var recOut map[string]any
	if err := json.Unmarshal([]byte(recRes.Flatten()), &recOut); err != nil {
		t.Fatal(err)
	}
	credID, _ := recOut["credential_id"].(float64)
	if credID == 0 {
		t.Fatalf("record result: %s", recRes.Flatten())
	}

	markRes, err := ts.markCredentialVerified().Call(context.Background(), json.RawMessage(`{
		"credential_id": `+itoa(int64(credID))+`,
		"asset_id": `+itoa(assetB)+`,
		"target": "smb",
		"evidence": "crackmapexec: Pwn3d!"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	markText := markRes.Flatten()
	var markOut map[string]any
	if err := json.Unmarshal([]byte(markText), &markOut); err != nil {
		t.Fatalf("unmarshal: %v raw=%s", err, markText)
	}
	if markOut["relation"] != "verified" {
		t.Fatalf("mark result: %v", markOut)
	}
	// A 保留 discovered，B 只有 verified。
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM asset_credential_links WHERE credential_id=$1 AND asset_id=$2 AND relation='discovered'`,
		int64(credID), assetA).Scan(&n); err != nil || n != 1 {
		t.Fatalf("A discovered links=%d err=%v", n, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM asset_credential_links WHERE credential_id=$1 AND asset_id=$2 AND relation='verified'`,
		int64(credID), assetB).Scan(&n); err != nil || n != 1 {
		t.Fatalf("B verified links=%d err=%v", n, err)
	}
}

// TestCredentialToolScopeGuards：范围外 asset_id、不可见 credential_id、空
// evidence 均被拒绝。
func TestCredentialToolScopeGuards(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ts, _, cleanup := credentialToolFixture(t, d)
	defer cleanup()

	inScope := insertServiceAsset(t, ts, "http://a.credtool-test.example/")

	// 任务外资产：无任务上下文登记（不加入本任务范围）。
	outsideTS := NewToolSet(nil, "")
	outsideTS.SetAssetStore(d.Assets(), d.Companies())
	outsideID := insertServiceAsset(t, outsideTS, "http://outside.credtool-test.example/")

	// 范围外 asset_id 拒绝 record。
	res, err := ts.recordCredential().Call(context.Background(), json.RawMessage(
		`{"asset_id": `+itoa(outsideID)+`, "cred_type": "password", "secret": "x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("out-of-scope record accepted: %s", res.Flatten())
	}

	// 合法 credential，但关联资产在任务外 → mark 不可见拒绝。
	credID, err := d.Assets().Credentials().UpsertDiscovered(db.CredentialInput{
		CredType: "password", Username: "credtool-outside", Secret: "pw",
	}, db.CredentialLinkInput{AssetID: outsideID})
	if err != nil {
		t.Fatal(err)
	}
	res, err = ts.markCredentialVerified().Call(context.Background(), json.RawMessage(
		`{"credential_id": `+itoa(credID)+`, "asset_id": `+itoa(inScope)+`, "evidence": "ok"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("invisible credential mark accepted: %s", res.Flatten())
	}

	// 合法 credential（任务内），但目标资产在范围外 → 拒绝。
	credIn, err := d.Assets().Credentials().UpsertDiscovered(db.CredentialInput{
		CredType: "password", Username: "credtool-in", Secret: "pw2",
	}, db.CredentialLinkInput{AssetID: inScope})
	if err != nil {
		t.Fatal(err)
	}
	res, err = ts.markCredentialVerified().Call(context.Background(), json.RawMessage(
		`{"credential_id": `+itoa(credIn)+`, "asset_id": `+itoa(outsideID)+`, "evidence": "ok"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("out-of-scope mark accepted: %s", res.Flatten())
	}

	// 空 evidence 拒绝。
	res, err = ts.markCredentialVerified().Call(context.Background(), json.RawMessage(
		`{"credential_id": `+itoa(credIn)+`, "asset_id": `+itoa(inScope)+`, "evidence": " "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("blank evidence accepted: %s", res.Flatten())
	}

	// 无任务上下文拒绝。
	noTask := NewToolSet(nil, "")
	noTask.SetAssetStore(d.Assets(), d.Companies())
	res, err = noTask.recordCredential().Call(context.Background(), json.RawMessage(
		`{"asset_id": 1, "cred_type": "password", "secret": "x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("task-less record accepted: %s", res.Flatten())
	}
}

// TestCredentialToolsCatalog：custom executor 的工具目录包含两个新工具，planner 不包含。
func TestCredentialToolsCatalog(t *testing.T) {
	ts := NewToolSet(nil, "")
	names := func(tools []actool.CoreTool) map[string]bool {
		out := map[string]bool{}
		for _, tool := range tools {
			out[tool.Name()] = true
		}
		return out
	}
	worker := names(ts.WorkerTools())
	custom := names(ts.CustomExecutorTools())
	planner := names(ts.PlannerTools())
	for _, key := range []string{"record_credential", "mark_credential_verified"} {
		if !worker[key] {
			t.Errorf("worker missing %s", key)
		}
		if !custom[key] {
			t.Errorf("custom executor missing %s", key)
		}
		if planner[key] {
			t.Errorf("planner must not bind %s", key)
		}
	}
	// seed 目录包含两个新工具（worker + custom_executor 绑定）。
	var foundRec, foundMark bool
	for _, seed := range BuiltinToolSeeds() {
		if seed.Key == "record_credential" {
			foundRec = true
		}
		if seed.Key == "mark_credential_verified" {
			foundMark = true
		}
	}
	if !foundRec || !foundMark {
		t.Fatalf("BuiltinToolSeeds missing credential tools: rec=%v mark=%v", foundRec, foundMark)
	}
}

// intranet_assets_api_test.go：GET /api/credentials/intranet/assets 的参数校验
// 与响应契约测试。需要真实 PG（NewManager 不可用时按项目惯例 skip）。
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"artex/db"
)

// TestIntranetTaskAssetsBadTaskID：缺失、非数字、0、负数的 task_id 一律 400。
func TestIntranetTaskAssetsBadTaskID(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	for _, target := range []string{
		"/api/credentials/intranet/assets",
		"/api/credentials/intranet/assets?task_id=",
		"/api/credentials/intranet/assets?task_id=abc",
		"/api/credentials/intranet/assets?task_id=0",
		"/api/credentials/intranet/assets?task_id=-3",
	} {
		rec := httptest.NewRecorder()
		s.getIntranetTaskAssets(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s → status=%d want 400 (body=%s)", target, rec.Code, rec.Body.String())
		}
	}
}

// TestIntranetTaskAssetsResponse：有效任务返回 200 且 assets 为非 nil 数组；
// 范围内网资产的凭据条目含 secret。
func TestIntranetTaskAssetsResponse(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	stamp := time.Now().UnixNano()
	task, err := m.pg.CreateTaskWithOptions(
		fmt.Sprintf("intranet api %d", stamp), "intranet api contract", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assets := m.pg.Assets()
	if _, err := assets.AddAgentScope(task.ID, "cidr", "10.55.66.0/24", "intranet api test", "agent"); err != nil {
		t.Fatal(err)
	}
	assetID, err := assets.UpsertIP(db.UpsertIPReq{IP: "10.55.66.7", Extra: map[string]any{"hostname": "api-test-host"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = m.pg.DeleteTask(task.ID)
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
		_, _ = m.pg.Exec(`DELETE FROM asset_credentials WHERE username='intranet-api-user'`)
	}()
	if _, err := assets.Credentials().UpsertDiscovered(
		db.CredentialInput{CredType: "password", Username: "intranet-api-user", Secret: "intranet-api-secret"},
		db.CredentialLinkInput{AssetID: assetID, Source: "lsass"},
	); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.getIntranetTaskAssets(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/credentials/intranet/assets?task_id=%d", task.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Count  int                   `json:"count"`
		Assets []db.IntranetAssetRow `json:"assets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v raw=%s", err, rec.Body.String())
	}
	if payload.Assets == nil {
		t.Fatal("assets must serialize as [] not null")
	}
	if payload.Count != len(payload.Assets) {
		t.Fatalf("count=%d but %d assets", payload.Count, len(payload.Assets))
	}
	var found *db.IntranetAssetRow
	for i := range payload.Assets {
		if payload.Assets[i].AssetID == assetID {
			found = &payload.Assets[i]
		}
	}
	if found == nil {
		t.Fatalf("in-scope intranet asset missing: %s", rec.Body.String())
	}
	if len(found.Credentials) != 1 || found.Credentials[0].Secret != "intranet-api-secret" {
		t.Fatalf("credential projection missing secret: %+v", found.Credentials)
	}

	// 空任务：assets 为空数组而非 null。
	emptyTask, err := m.pg.CreateTaskWithOptions(fmt.Sprintf("intranet api empty %d", stamp), "empty", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(emptyTask.ID)
	rec = httptest.NewRecorder()
	s.getIntranetTaskAssets(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/credentials/intranet/assets?task_id=%d", emptyTask.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty task status=%d body=%s", rec.Code, rec.Body.String())
	}
	var emptyPayload struct {
		Count  int                   `json:"count"`
		Assets []db.IntranetAssetRow `json:"assets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &emptyPayload); err != nil {
		t.Fatalf("decode empty: %v raw=%s", err, rec.Body.String())
	}
	if emptyPayload.Count != 0 || emptyPayload.Assets == nil || len(emptyPayload.Assets) != 0 {
		t.Fatalf("empty task payload=%s want count=0 assets=[]", rec.Body.String())
	}
}

// TestIntranetTaskAssetsQueryError：底层查询失败返回 500，绝不伪装成空资产数组。
func TestIntranetTaskAssetsQueryError(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	s := &Server{m: m}
	// 关闭底层连接使查询必然失败。
	_ = m.pg.Close()

	rec := httptest.NewRecorder()
	s.getIntranetTaskAssets(rec, httptest.NewRequest(http.MethodGet,
		"/api/credentials/intranet/assets?task_id=1", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 (body=%s)", rec.Code, rec.Body.String())
	}
}

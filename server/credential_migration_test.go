package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"artex/db"
)

// resetCredentialMigrationFlag 保存并清除迁移标记，返回恢复函数。
func resetCredentialMigrationFlag(t *testing.T, m *Manager) func() {
	t.Helper()
	old, had, err := m.pg.GetSetting(legacyAssetAuthBackfillFlag)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`DELETE FROM settings WHERE key=$1`, legacyAssetAuthBackfillFlag); err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = m.pg.SetSetting(legacyAssetAuthBackfillFlag, old)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, legacyAssetAuthBackfillFlag)
		}
	}
}

// legacyAuthAsset 直接在 assets.auth 写入存量 JSONB 数组（模拟迁移前数据）。
func legacyAuthAsset(t *testing.T, m *Manager, url string, auth string) int64 {
	t.Helper()
	var id int64
	if err := m.pg.QueryRow(`
INSERT INTO assets(type, url, service_type, service_name, domain, auth)
VALUES ('service', $1, 'http', 'http', $2, $3::jsonb[])
ON CONFLICT (url) WHERE type='service' AND service_type='http' DO UPDATE SET auth = EXCLUDED.auth
RETURNING id`, url, strings.TrimSuffix(strings.TrimPrefix(strings.Split(url, "://")[1], "www."), "/"), auth).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func credentialRowCount(t *testing.T, m *Manager, where string, args ...any) int {
	t.Helper()
	var n int
	if err := m.pg.QueryRow(`SELECT count(*) FROM asset_credentials WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func linkRowCount(t *testing.T, m *Manager, where string, args ...any) int {
	t.Helper()
	var n int
	if err := m.pg.QueryRow(`SELECT count(*) FROM asset_credential_links WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLegacyAssetAuthMigration：同一 auth 凭据存在于多个资产时，迁移后每个资产
// 都有 discovered 关系；token-only 条目可迁移；password+token 皆空的条目跳过；
// 重复执行行数不增长。
func TestLegacyAssetAuthMigration(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer resetCredentialMigrationFlag(t, m)()

	stamp := time.Now().UnixNano()
	urlA := fmt.Sprintf("http://mig-a-%d.credmig-test.example/", stamp)
	urlB := fmt.Sprintf("http://mig-b-%d.credmig-test.example/", stamp)
	assetA := legacyAuthAsset(t, m, urlA, `{
		"{\"type\":\"basic\",\"username\":\"miguser\",\"password\":\"migpass\",\"description\":\"from config\"}",
		"{\"type\":\"bearer\",\"username\":\"migtoken\",\"token\":\"tok-only\"}",
		"{\"type\":\"note\",\"username\":\"migempty\"}"
	}`)
	assetB := legacyAuthAsset(t, m, urlB, `{
		"{\"type\":\"basic\",\"username\":\"miguser\",\"password\":\"migpass\",\"description\":\"same cred on B\"}"
	}`)
	defer func() {
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE id IN ($1,$2)`, assetA, assetB)
		_, _ = m.pg.Exec(`DELETE FROM asset_credentials WHERE username IN ('miguser','migtoken')`)
	}()

	if err := upgradeLegacyAssetAuthCredentials(m.pg); err != nil {
		t.Fatal(err)
	}
	// 完成标记已写入。
	if v, _, err := m.pg.GetSetting(legacyAssetAuthBackfillFlag); err != nil || v != "true" {
		t.Fatalf("migration flag=%q err=%v", v, err)
	}
	// 身份合并：miguser 一行、migtoken 一行；空条目跳过。
	if got := credentialRowCount(t, m, `username='miguser' AND secret='migpass'`); got != 1 {
		t.Fatalf("miguser identities=%d want 1", got)
	}
	if got := credentialRowCount(t, m, `username='migtoken' AND secret='tok-only'`); got != 1 {
		t.Fatalf("token-only identities=%d want 1", got)
	}
	if got := credentialRowCount(t, m, `username='migempty'`); got != 0 {
		t.Fatalf("empty entry must be skipped, got %d", got)
	}
	// 同一凭据出现在多个资产：每个原始资产都有 discovered 关系。
	miguserID := int64(0)
	if err := m.pg.QueryRow(`SELECT id FROM asset_credentials WHERE username='miguser'`).Scan(&miguserID); err != nil {
		t.Fatal(err)
	}
	if got := linkRowCount(t, m, `credential_id=$1 AND relation='discovered'`, miguserID); got != 2 {
		t.Fatalf("miguser discovered links=%d want 2 (one per original asset)", got)
	}
	var sourceA string
	if err := m.pg.QueryRow(`SELECT source FROM asset_credential_links WHERE credential_id=$1 AND asset_id=$2`, miguserID, assetA).Scan(&sourceA); err != nil {
		t.Fatal(err)
	}
	if sourceA != "from config" {
		t.Fatalf("link source=%q want original description", sourceA)
	}

	// 重复执行：行数不增长。
	credBefore := credentialRowCount(t, m, `username IN ('miguser','migtoken')`)
	linkBefore := linkRowCount(t, m, `credential_id=$1`, miguserID)
	// 清掉标记强制重跑，验证幂等。
	if _, err := m.pg.Exec(`DELETE FROM settings WHERE key=$1`, legacyAssetAuthBackfillFlag); err != nil {
		t.Fatal(err)
	}
	if err := upgradeLegacyAssetAuthCredentials(m.pg); err != nil {
		t.Fatal(err)
	}
	if got := credentialRowCount(t, m, `username IN ('miguser','migtoken')`); got != credBefore {
		t.Fatalf("re-run credentials=%d want %d", got, credBefore)
	}
	if got := linkRowCount(t, m, `credential_id=$1`, miguserID); got != linkBefore {
		t.Fatalf("re-run links=%d want %d", got, linkBefore)
	}
}

// TestLegacyAssetAuthMigrationFailureKeepsFlagUnset：中途失败（非法 JSON 元素）
// 不设置完成标记，下次启动可重试；修复数据后重跑成功。
func TestLegacyAssetAuthMigrationFailureKeepsFlagUnset(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	defer resetCredentialMigrationFlag(t, m)()

	stamp := time.Now().UnixNano()
	badURL := fmt.Sprintf("http://mig-bad-%d.credmig-test.example/", stamp)
	// 非对象元素：展开时 json.Unmarshal 到 map 失败 → 迁移中途出错。
	badID := legacyAuthAsset(t, m, badURL, `{"\"not-an-object\""}`)
	defer func() {
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, badID)
	}()

	if err := upgradeLegacyAssetAuthCredentials(m.pg); err == nil {
		t.Fatal("migration should fail on malformed auth element")
	}
	if _, had, err := m.pg.GetSetting(legacyAssetAuthBackfillFlag); err != nil || had {
		t.Fatalf("flag must stay unset after failure (had=%v err=%v)", had, err)
	}

	// 修复数据后重跑成功并写标记。
	if _, err := m.pg.Exec(`UPDATE assets SET auth='{}' WHERE id=$1`, badID); err != nil {
		t.Fatal(err)
	}
	if err := upgradeLegacyAssetAuthCredentials(m.pg); err != nil {
		t.Fatal(err)
	}
	if v, _, err := m.pg.GetSetting(legacyAssetAuthBackfillFlag); err != nil || v != "true" {
		t.Fatalf("flag after retry=%q err=%v", v, err)
	}
}

// TestListAssetsRedactsCredentialSecret：资产 API 回填 relation 且不包含
// password/token/secret。
func TestListAssetsRedactsCredentialSecret(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	url := fmt.Sprintf("http://api-redact-%d.credmig-test.example/", stamp)
	assetID, err := m.pg.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1 OR root_domain='credmig-test.example'`, assetID)
		_, _ = m.pg.Exec(`DELETE FROM asset_credentials WHERE username='api-redact-user'`)
	}()
	credID, err := m.pg.Assets().Credentials().UpsertDiscovered(
		db.CredentialInput{CredType: "password", Username: "api-redact-user", Secret: "api-redact-secret"},
		db.CredentialLinkInput{AssetID: assetID, Source: "config.py"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.Assets().Credentials().MarkVerified(credID, assetID, "http", "login 200 OK"); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	req := httptest.NewRequest(http.MethodGet, "/api/assets?type=service&limit=200", nil)
	rec := httptest.NewRecorder()
	s.listAssets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{`"password":`, `"token":`, `"secret":`, "api-redact-secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("asset API leaks %s: %s", forbidden, body)
		}
	}
	var payload struct {
		Assets []struct {
			ID   int64            `json:"id"`
			Auth []map[string]any `json:"auth"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	for _, a := range payload.Assets {
		if a.ID == assetID {
			found = a.Auth
		}
	}
	if len(found) != 2 {
		t.Fatalf("api auth entries=%d want 2 (discovered+verified)", len(found))
	}
	relations := map[string]bool{}
	for _, item := range found {
		rel, _ := item["relation"].(string)
		relations[rel] = true
		if item["id"] == nil {
			t.Fatalf("auth entry missing credential id: %v", item)
		}
	}
	if !relations["discovered"] || !relations["verified"] {
		t.Fatalf("relations=%v want discovered+verified", relations)
	}
}

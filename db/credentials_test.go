package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// credentialFixture 建两个 service 资产供凭据关系测试使用，返回清理函数。
func credentialFixture(t *testing.T, d *DB, av2 *AssetStore) (assetA, assetB int64, cleanup func()) {
	t.Helper()
	var err error
	assetA, err = av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: "http://cred-a.example.org/"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err = av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: "http://cred-b.example.org/"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup = func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain IN ('cred-a.example.org','cred-b.example.org') OR url LIKE '%cred-_.example.org'`)
		_, _ = d.Exec(`DELETE FROM asset_credentials WHERE username LIKE 'credtest-%'`)
	}
	return assetA, assetB, cleanup
}

func countCredentialLinks(t *testing.T, d *DB, where string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM asset_credential_links WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countCredentials(t *testing.T, d *DB, where string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM asset_credentials WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCredentialInputPreservesSecretWhitespace(t *testing.T) {
	input, _, ok := credentialInputFromAuthItem(map[string]any{
		"type": "password", "username": "credtest-whitespace", "password": "  p ass  ",
	})
	if !ok || input.Secret != "  p ass  " {
		t.Fatalf("password secret changed: ok=%v secret=%q", ok, input.Secret)
	}
	input, _, ok = credentialInputFromAuthItem(map[string]any{
		"type": "bearer", "password": " \t ", "token": "  token-value  ",
	})
	if !ok || input.Secret != "  token-value  " {
		t.Fatalf("token fallback secret changed: ok=%v secret=%q", ok, input.Secret)
	}
}

// TestCredentialDedupKeepsAssetRelations：同一凭据重复发现只产生一条身份记录，
// 但每次资产关系都保留。
func TestCredentialDedupKeepsAssetRelations(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, assetB, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	input := CredentialInput{CredType: "password", Username: "credtest-dedup", Secret: "s3cret"}
	idA1, err := creds.UpsertDiscovered(input, CredentialLinkInput{AssetID: assetA, Source: "config.xml"})
	if err != nil {
		t.Fatal(err)
	}
	// 同一资产重复发现：身份去重，关系不重复。
	idA2, err := creds.UpsertDiscovered(input, CredentialLinkInput{AssetID: assetA, Source: "config.xml"})
	if err != nil {
		t.Fatal(err)
	}
	if idA2 != idA1 {
		t.Fatalf("identity dedup failed: %d vs %d", idA2, idA1)
	}
	// 另一资产发现同一凭据：身份仍一行，关系各保留。
	idB, err := creds.UpsertDiscovered(input, CredentialLinkInput{AssetID: assetB, Source: "memory dump"})
	if err != nil {
		t.Fatal(err)
	}
	if idB != idA1 {
		t.Fatalf("cross-asset identity merge failed: %d vs %d", idB, idA1)
	}
	if got := countCredentials(t, d, `username='credtest-dedup'`); got != 1 {
		t.Fatalf("credential identities=%d want 1", got)
	}
	if got := countCredentialLinks(t, d, `credential_id=$1`, idA1); got != 2 {
		t.Fatalf("links=%d want 2 (one per asset)", got)
	}
}

// TestCredentialCrossAssetVerified：A 发现、B 认证后，A 只有 discovered，B 只有 verified。
func TestCredentialCrossAssetVerified(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, assetB, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-cross", Domain: "CORP", Secret: "pw"},
		CredentialLinkInput{AssetID: assetA, Target: "smb", Source: "lsass"})
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetB, "smb", "smbclient session established"); err != nil {
		t.Fatal(err)
	}

	relations := map[int64][]string{}
	rows, err := d.Query(`SELECT asset_id, relation FROM asset_credential_links WHERE credential_id=$1 ORDER BY asset_id, relation`, credID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID int64
		var relation string
		if err := rows.Scan(&assetID, &relation); err != nil {
			t.Fatal(err)
		}
		relations[assetID] = append(relations[assetID], relation)
	}
	if len(relations[assetA]) != 1 || relations[assetA][0] != "discovered" {
		t.Fatalf("asset A relations=%v want [discovered]", relations[assetA])
	}
	if len(relations[assetB]) != 1 || relations[assetB][0] != "verified" {
		t.Fatalf("asset B relations=%v want [verified]", relations[assetB])
	}
}

// TestCredentialSameAssetDualRelation：A 发现并本机认证后，A 同时具有两种关系。
func TestCredentialSameAssetDualRelation(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, _, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "hash", Username: "credtest-local", Secret: "aaf4c61ddcc"},
		CredentialLinkInput{AssetID: assetA, Source: "NTDS.dit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetA, "ssh", "psexec shell spawned"); err != nil {
		t.Fatal(err)
	}
	got := countCredentialLinks(t, d, `credential_id=$1 AND asset_id=$2`, credID, assetA)
	if got != 2 {
		t.Fatalf("same-asset links=%d want 2 (discovered+verified)", got)
	}
}

// TestCredentialMarkVerifiedIdempotent：重复 MarkVerified 不产生重复关系，
// evidence/last_seen 正常更新。
func TestCredentialMarkVerifiedIdempotent(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, _, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-idem", Secret: "pw"},
		CredentialLinkInput{AssetID: assetA})
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetA, "rdp", "first evidence"); err != nil {
		t.Fatal(err)
	}
	var firstSeen string
	if err := d.QueryRow(`SELECT last_seen::text FROM asset_credential_links
WHERE credential_id=$1 AND asset_id=$2 AND relation='verified'`, credID, assetA).Scan(&firstSeen); err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetA, "rdp", "second evidence"); err != nil {
		t.Fatal(err)
	}
	if got := countCredentialLinks(t, d, `credential_id=$1 AND asset_id=$2 AND relation='verified'`, credID, assetA); got != 1 {
		t.Fatalf("verified links=%d want 1", got)
	}
	var evidence, secondSeen string
	if err := d.QueryRow(`SELECT evidence, last_seen::text FROM asset_credential_links
WHERE credential_id=$1 AND asset_id=$2 AND relation='verified'`, credID, assetA).Scan(&evidence, &secondSeen); err != nil {
		t.Fatal(err)
	}
	if evidence != "second evidence" {
		t.Fatalf("evidence=%q want updated %q", evidence, "second evidence")
	}
	if secondSeen < firstSeen {
		t.Fatalf("last_seen not refreshed: %q -> %q", firstSeen, secondSeen)
	}

	// 错误输入：凭据不存在 / 证据为空。
	if err := creds.MarkVerified(999999999, assetA, "rdp", "evidence"); err == nil {
		t.Fatal("MarkVerified on missing credential should fail")
	}
	if err := creds.MarkVerified(credID, assetA, "rdp", "  "); err == nil {
		t.Fatal("MarkVerified with blank evidence should fail")
	}
	// 失败调用不得改动已有关系。
	if got := countCredentialLinks(t, d, `credential_id=$1`, credID); got != 2 {
		t.Fatalf("links after failed calls=%d want 2", got)
	}
}

// TestAggregateAuthIncludesSecretAndGrouped：AggregateAuth 同时返回两种关系，
// 且按已确认需求包含 secret（不返回 password/token 键）。
func TestAggregateAuthIncludesSecretAndGrouped(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, _, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-agg", Domain: "CORP", Secret: "topsecret"},
		CredentialLinkInput{AssetID: assetA, Target: "smb", Source: "web.config"})
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetA, "smb", "crackmapexec Pwn3d!"); err != nil {
		t.Fatal(err)
	}

	assets, err := av2.GetByIDs([]int64{assetA})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 {
		t.Fatalf("GetByIDs returned %d assets", len(assets))
	}
	auth := assets[0].Auth
	if len(auth) != 2 {
		t.Fatalf("auth entries=%d want 2 (discovered+verified)", len(auth))
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	// secret 放开到读取投影：必须携带 secret 键与准确值；但绝不出现 password/token 键。
	for _, forbidden := range []string{`"password":`, `"token":`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("auth projection leaks legacy key %s: %s", forbidden, raw)
		}
	}
	byRelation := map[string]map[string]any{}
	for _, item := range auth {
		relation, _ := item["relation"].(string)
		byRelation[relation] = item
	}
	disc, ok := byRelation["discovered"]
	if !ok {
		t.Fatalf("missing discovered entry: %v", auth)
	}
	// id 供 mark_credential_verified 引用。
	if id, _ := disc["id"].(int64); id != credID {
		t.Fatalf("discovered id=%v want %d", disc["id"], credID)
	}
	if disc["description"] != "web.config" {
		t.Fatalf("discovered description=%v want source", disc["description"])
	}
	if disc["secret"] != "topsecret" {
		t.Fatalf("discovered secret=%v want %q", disc["secret"], "topsecret")
	}
	ver, ok := byRelation["verified"]
	if !ok {
		t.Fatalf("missing verified entry: %v", auth)
	}
	if ver["description"] != "crackmapexec Pwn3d!" {
		t.Fatalf("verified description=%v want evidence", ver["description"])
	}
	if ver["secret"] != "topsecret" {
		t.Fatalf("verified secret=%v want %q", ver["secret"], "topsecret")
	}
	if ver["domain"] != "CORP" || ver["target"] != "smb" {
		t.Fatalf("verified projection missing domain/target: %v", ver)
	}
}

// TestCredentialDeleteCascadeAndPrune：删除资产级联删除关系；无关系凭据可被清理。
func TestCredentialDeleteCascadeAndPrune(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	assetA, _, cleanup := credentialFixture(t, d, av2)
	defer cleanup()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "token", Username: "credtest-cascade", Secret: "tok"},
		CredentialLinkInput{AssetID: assetA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := av2.DeleteByIDs([]int64{assetA}); err != nil {
		t.Fatal(err)
	}
	if got := countCredentialLinks(t, d, `credential_id=$1`, credID); got != 0 {
		t.Fatalf("links after asset delete=%d want 0", got)
	}
	// DeleteByIDs 已在删除路径清理孤儿凭据。
	if got := countCredentials(t, d, `id=$1`, credID); got != 0 {
		t.Fatalf("orphan credential survived prune: id=%d", credID)
	}

	// 显式 PruneOrphaned 幂等（已无孤儿时返回 0）。
	if n, err := creds.PruneOrphaned(); err != nil || n != 0 {
		t.Fatalf("idempotent prune=(%d,%v) want (0,nil)", n, err)
	}
}

// TestCredentialArchiveRestore：任务归档快照包含凭据及关系；归档压实清理热库后，
// 恢复按凭据唯一键合并身份、重映射 asset/credential ID 恢复 discovered/verified。
// 资产 A 在恢复前被另一任务以自然键重建（新 ID），验证关系挂到合并后的资产、
// 不产生错误的跨资产关系。
func TestCredentialArchiveRestore(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}

	task, err := d.CreateTaskWithOptions("credential archive roundtrip", "restore credentials", TaskCreateOptions{Name: "cred archive"})
	if err != nil {
		t.Fatal(err)
	}
	urlA := "http://arc-a.credarc-test.example/"
	urlB := "http://arc-b.credarc-test.example/"
	defer func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM assets WHERE root_domain='credarc-test.example' OR domain LIKE '%.credarc-test.example'`)
		_, _ = d.Exec(`DELETE FROM asset_credentials WHERE username='credtest-archive'`)
	}()

	assetA, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: urlA, TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: urlB, TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-archive", Secret: "archive-secret"},
		CredentialLinkInput{AssetID: assetA, Source: "archive source"})
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, assetB, "ssh", "archive evidence"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || claimed == nil || claimed.ID != archive.ID {
		t.Fatalf("archive claim = %+v, %v", claimed, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 归档快照包含凭据身份与两种关系。
	if got := rawRowCount(snapshot.Tables["asset_credentials"]); got != 1 {
		t.Fatalf("snapshot asset_credentials rows=%d want 1", got)
	}
	if got := rawRowCount(snapshot.Tables["asset_credential_links"]); got != 2 {
		t.Fatalf("snapshot asset_credential_links rows=%d want 2", got)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/cred-archive.tar.zst", "sha", 10, 5); err != nil {
		t.Fatal(err)
	}
	// 热库压实：资产删除、关系级联删除、孤儿凭据被清理。
	if got := countCredentialLinks(t, d, `credential_id=$1`, credID); got != 0 {
		t.Fatalf("links after compaction=%d want 0", got)
	}
	if got := countCredentials(t, d, `id=$1`, credID); got != 0 {
		t.Fatalf("orphan credential survived compaction: id=%d", got)
	}

	// 资产 A 在恢复前被以自然键（URL）重建为新 ID —— 恢复必须挂到合并后的资产。
	assetA2, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: urlA})
	if err != nil {
		t.Fatal(err)
	}
	if assetA2 == assetA {
		t.Fatalf("expected a fresh id for recreated asset A, got %d", assetA2)
	}

	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 600); err != nil {
		t.Fatal(err)
	}

	// URL A 仍然只有一个资产（自然键合并，不产生重复行）。
	var urlACount int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE type='service' AND url=$1`, normalizeURL(urlA)).Scan(&urlACount); err != nil {
		t.Fatal(err)
	}
	if urlACount != 1 {
		t.Fatalf("assets with url A=%d want 1 (natural-key merge)", urlACount)
	}
	var restoredB int64
	if err := d.QueryRow(`SELECT id FROM assets WHERE type='service' AND url=$1`, normalizeURL(urlB)).Scan(&restoredB); err != nil {
		t.Fatalf("asset B not restored: %v", err)
	}

	grouped, err := av2.Credentials().AggregateAuth([]int64{assetA2, restoredB})
	if err != nil {
		t.Fatal(err)
	}
	// A（合并后的新 ID）只有 discovered；B 只有 verified —— 不产生跨资产错位关系。
	relOf := func(items []map[string]any) []string {
		out := []string{}
		for _, item := range items {
			rel, _ := item["relation"].(string)
			out = append(out, rel)
		}
		return out
	}
	if got := relOf(grouped[assetA2]); len(got) != 1 || got[0] != "discovered" {
		t.Fatalf("merged asset A relations=%v want [discovered]", got)
	}
	if got := relOf(grouped[restoredB]); len(got) != 1 || got[0] != "verified" {
		t.Fatalf("restored asset B relations=%v want [verified]", got)
	}
	// 身份按唯一键合并：库中仍只有一行该凭据。
	if got := countCredentials(t, d, `username='credtest-archive'`); got != 1 {
		t.Fatalf("credential identities after restore=%d want 1", got)
	}
	// 恢复的 verified 关系投影正确（description 取认证证据）。
	if item := grouped[restoredB][0]; item["description"] != "archive evidence" {
		t.Fatalf("restored verified evidence=%v", item["description"])
	}
}

// TestQueryIntranetAssetsByTask：任务范围内网资产视图——范围内 RFC1918 资产
// 返回且凭据含 secret；C 段、extra、open_ports 正确解码；范围外内网资产与
// 公网资产不返回；无凭据内网资产返回且 credentials 为空；同一资产双关系均保留。
func TestQueryIntranetAssetsByTask(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	task, err := d.CreateTaskWithOptions("intranet asset view", "task scoped intranet assets", TaskCreateOptions{Name: "intranet view"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteTask(task.ID) }()
	// 范围：一个内网 C 段 + 一个公网 C 段（公网资产用于验证 RFC1918 排除）。
	if _, err := av2.AddAgentScope(task.ID, "cidr", "10.99.77.0/24", "intranet view test", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := av2.AddAgentScope(task.ID, "cidr", "192.0.2.0/24", "intranet view test", "agent"); err != nil {
		t.Fatal(err)
	}

	companyID, _, err := av2.Companies().UpsertCompany("credtest-intranet-企业", "")
	if err != nil {
		t.Fatal(err)
	}

	// 范围内：带凭据（双关系）的内网资产。
	ipPrivate, err := av2.UpsertIP(UpsertIPReq{
		IP:        "10.99.77.5",
		OpenPorts: []PortService{{Port: 445, Service: "smb"}},
		Extra:     map[string]any{"hostname": "web01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 范围内：无凭据的内网资产。
	ipNoCred, err := av2.UpsertIP(UpsertIPReq{IP: "10.99.77.6"})
	if err != nil {
		t.Fatal(err)
	}
	// 范围内：公网资产（不得返回）。
	ipPublic, err := av2.UpsertIP(UpsertIPReq{IP: "192.0.2.77"})
	if err != nil {
		t.Fatal(err)
	}
	// 范围外：内网资产（不得返回）。
	ipOutside, err := av2.UpsertIP(UpsertIPReq{IP: "10.98.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, ipPrivate)
	defer deleteAsset(d, ipNoCred)
	defer deleteAsset(d, ipPublic)
	defer deleteAsset(d, ipOutside)

	// 同 IP 的 service 资产（真实数据模型下 IP 资产与 HTTP 服务是两行）：
	// 内网视图按 IP 一行展示，service 行不得作为重复资产行返回。
	ipSvcDup, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: "http://10.99.77.5:8080/"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, ipSvcDup)

	// 企业归属投影。
	if _, err := d.Exec(`UPDATE assets SET company_id=$1, company_source='explicit' WHERE id=$2`, companyID, ipPrivate); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM companies WHERE name='credtest-intranet-企业'`)
	}()

	creds := av2.Credentials()
	credID, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-intranet", Secret: "topsecret"},
		CredentialLinkInput{AssetID: ipPrivate, Target: "smb", Source: "lsass"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.MarkVerified(credID, ipPrivate, "smb", "crackmapexec Pwn3d!"); err != nil {
		t.Fatal(err)
	}
	// 范围外资产上的凭据不得出现。
	if _, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-intranet-outside", Secret: "outsidesecret"},
		CredentialLinkInput{AssetID: ipOutside, Source: "config"},
	); err != nil {
		t.Fatal(err)
	}
	// 公网资产上的凭据不得出现。
	if _, err := creds.UpsertDiscovered(
		CredentialInput{CredType: "password", Username: "credtest-intranet-public", Secret: "publicsecret"},
		CredentialLinkInput{AssetID: ipPublic, Source: "config"},
	); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM asset_credentials WHERE username LIKE 'credtest-intranet%'`)
	}()

	rows, err := creds.QueryIntranetAssetsByTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]IntranetAssetRow{}
	for _, row := range rows {
		byID[row.AssetID] = row
		for _, cred := range row.Credentials {
			if cred.Username == "credtest-intranet-public" || cred.Username == "credtest-intranet-outside" {
				t.Fatalf("out-of-view credential leaked: %+v", cred)
			}
		}
	}
	if _, ok := byID[ipPublic]; ok {
		t.Fatalf("public-IP asset leaked into intranet view: %+v", byID[ipPublic])
	}
	if _, ok := byID[ipOutside]; ok {
		t.Fatalf("out-of-scope intranet asset leaked: %+v", byID[ipOutside])
	}

	// 范围内带凭据资产：投影、C 段、extra、open_ports、双关系与 secret。
	found, ok := byID[ipPrivate]
	if !ok {
		t.Fatalf("in-scope asset missing: rows=%v", rows)
	}
	if found.AssetIP != "10.99.77.5" || found.AssetType != "ip" {
		t.Fatalf("wrong asset projection: %+v", found)
	}
	if found.AssetCSegment != "10.99.77.0/24" {
		t.Fatalf("c_segment=%q want %q", found.AssetCSegment, "10.99.77.0/24")
	}
	if found.Extra["hostname"] != "web01" {
		t.Fatalf("extra not decoded: %+v", found.Extra)
	}
	if len(found.OpenPorts) != 1 || found.OpenPorts[0].Port != 445 || found.OpenPorts[0].Service != "smb" {
		t.Fatalf("open_ports not decoded: %+v", found.OpenPorts)
	}
	if found.CompanyID != companyID || found.CompanyName != "credtest-intranet-企业" {
		t.Fatalf("company projection wrong: %+v", found)
	}
	if len(found.Credentials) != 2 {
		t.Fatalf("credentials=%d want 2 (discovered+verified): %+v", len(found.Credentials), found.Credentials)
	}
	relations := map[string]CredentialFullView{}
	for _, cred := range found.Credentials {
		relations[cred.Relation] = cred
		if cred.Secret != "topsecret" {
			t.Fatalf("secret not projected: %+v", cred)
		}
	}
	if relations[CredentialRelationDiscovered].Source != "lsass" {
		t.Fatalf("discovered projection wrong: %+v", relations[CredentialRelationDiscovered])
	}
	if relations[CredentialRelationVerified].Evidence != "crackmapexec Pwn3d!" {
		t.Fatalf("verified projection wrong: %+v", relations[CredentialRelationVerified])
	}

	// 范围内无凭据资产：保留且 credentials 为空数组（非 null）。
	noCred, ok := byID[ipNoCred]
	if !ok {
		t.Fatalf("credential-less in-scope asset missing: rows=%v", rows)
	}
	if noCred.Credentials == nil || len(noCred.Credentials) != 0 {
		t.Fatalf("credential-less asset credentials=%v want empty slice", noCred.Credentials)
	}
}

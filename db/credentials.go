// credentials.go：资产凭据身份与发现/认证关系的存储。
// 主要函数/类型：
//   - CredentialStore：asset_credentials（凭据身份）+ asset_credential_links（资产关系）的存储入口。
//   - CredentialInput / CredentialLinkInput：写入输入（含 secret，绝不用于读取投影）。
//   - CredentialView：脱敏读取视图（无 Secret 字段）。
//   - UpsertDiscovered / MarkVerified：发现关系写入与认证关系标记。
//   - AggregateAuth / PopulateAuth：按资产批量聚合脱敏凭据并回填 Asset.Auth。
//   - IsVisibleForTask / PruneOrphaned：任务可见性校验与孤儿凭据清理。
//
// 语义约束（见 dev/assets-credentials-clues-plan.md）：
//   - 凭据身份只表达「这个凭据是什么」，认证状态是资产相关的，只能由
//     links.relation='verified' 表达；asset_credentials 上没有全局 verified 字段。
//   - A 发现、B 认证：A 保留 discovered，B 增加 verified；绝不移动或删除 A 的来源。
//   - secret 只进入 asset_credentials 用于写入与唯一键去重；所有 SELECT 投影（含
//     AggregateAuth/PopulateAuth 回填）一律不取 secret 列。

package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// 关系类型常量。
const (
	CredentialRelationDiscovered = "discovered"
	CredentialRelationVerified   = "verified"
)

// CredentialInput 是凭据身份的写入输入。
type CredentialInput struct {
	CredType string // 为空时落库为 'unknown'
	Username string
	Domain   string
	Secret   string // 只用于写入与唯一键去重，任何读取路径不得回填
	Extra    map[string]any
}

// CredentialLinkInput 是凭据与资产关系的写入输入。
type CredentialLinkInput struct {
	AssetID  int64
	Relation string // discovered | verified
	Target   string // 认证协议/服务，如 smb/rdp/ssh/URL
	Source   string // discovered：文件、配置、内存等来源
	Evidence string // verified：认证成功的关键证据
}

// CredentialView 是凭据的脱敏读取视图：刻意没有 Secret 字段，避免被误序列化。
type CredentialView struct {
	ID        int64          `json:"id"`
	CredType  string         `json:"type"`
	Username  string         `json:"username"`
	Domain    string         `json:"domain"`
	Relation  string         `json:"relation"`
	Target    string         `json:"target"`
	Source    string         `json:"source"`
	Evidence  string         `json:"evidence"`
	Extra     map[string]any `json:"extra,omitempty"`
	CreatedAt string         `json:"created_at"`
	LastSeen  string         `json:"last_seen"`
}

// CredentialStore operates on asset_credentials + asset_credential_links.
// 与 AssetStore 一样支持 tx 复用：从带事务的 AssetStore 派生时，写入落在同一事务里。
type CredentialStore struct {
	db *DB
	tx *sql.Tx
}

// Credentials returns the credential store derived from this asset store,
// sharing its db handle and (when present) its open transaction.
func (s *AssetStore) Credentials() *CredentialStore {
	return &CredentialStore{db: s.db, tx: s.tx}
}

// credentialQuerier 抽象 *db.DB / *sql.Tx 的公共查询面。
type credentialQuerier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (s *CredentialStore) querier() credentialQuerier {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// withTx 保证写入落在单事务里：已有事务直接复用，否则新开一个。
func (s *CredentialStore) withTx(fn func(tx *sql.Tx) error) error {
	if s.tx != nil {
		return fn(s.tx)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertCredentialIdentity 按唯一键 (cred_type,username,domain,secret) Upsert
// 凭据身份：冲突时更新 last_seen 并返回已有 ID。
func upsertCredentialIdentity(tx *sql.Tx, c CredentialInput) (int64, error) {
	credType := strings.TrimSpace(c.CredType)
	if credType == "" {
		credType = "unknown"
	}
	extra := "{}"
	if len(c.Extra) > 0 {
		raw, err := json.Marshal(c.Extra)
		if err != nil {
			return 0, err
		}
		extra = string(raw)
	}
	var id int64
	err := tx.QueryRow(`
INSERT INTO asset_credentials(cred_type, username, domain, secret, extra)
VALUES ($1, $2, $3, $4, $5::jsonb)
ON CONFLICT (cred_type, username, domain, secret) DO UPDATE SET last_seen = now()
RETURNING id`,
		credType, c.Username, c.Domain, c.Secret, extra,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// upsertCredentialLink 写入 (credential_id, asset_id, relation) 关系：冲突时只更新
// 非空 target/source/evidence 与 last_seen，不重复插行。
func upsertCredentialLink(tx *sql.Tx, credentialID int64, link CredentialLinkInput) error {
	_, err := tx.Exec(`
INSERT INTO asset_credential_links(credential_id, asset_id, relation, target, source, evidence)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (credential_id, asset_id, relation) DO UPDATE SET
    target    = COALESCE(NULLIF(EXCLUDED.target, ''),   asset_credential_links.target),
    source    = COALESCE(NULLIF(EXCLUDED.source, ''),   asset_credential_links.source),
    evidence  = COALESCE(NULLIF(EXCLUDED.evidence, ''), asset_credential_links.evidence),
    last_seen = now()`,
		credentialID, link.AssetID, link.Relation, link.Target, link.Source, link.Evidence,
	)
	return err
}

// UpsertDiscovered 在一个数据库事务内 Upsert 凭据身份，再写 link.AssetID 指向
// 资产的 discovered 关系。身份冲突更新 last_seen 并返回已有 ID；关系冲突更新
// 非空 target/source 与 last_seen，不重复插行。
func (s *CredentialStore) UpsertDiscovered(c CredentialInput, link CredentialLinkInput) (int64, error) {
	if link.AssetID <= 0 {
		return 0, fmt.Errorf("asset_id is required")
	}
	link.Relation = CredentialRelationDiscovered
	link.Evidence = "" // discovered 关系不携带认证证据
	var id int64
	err := s.withTx(func(tx *sql.Tx) error {
		credID, err := upsertCredentialIdentity(tx, c)
		if err != nil {
			return err
		}
		if err := upsertCredentialLink(tx, credID, link); err != nil {
			return err
		}
		id = credID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// MarkVerified 只新增或更新指定资产的 verified 关系：凭据不存在或证据为空时
// 返回错误；绝不修改任何 discovered 关系。
func (s *CredentialStore) MarkVerified(credentialID, assetID int64, target, evidence string) error {
	if credentialID <= 0 {
		return fmt.Errorf("credential_id is required")
	}
	if assetID <= 0 {
		return fmt.Errorf("asset_id is required")
	}
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("evidence is required")
	}
	return s.withTx(func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM asset_credentials WHERE id=$1)`, credentialID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("credential %d not found", credentialID)
		}
		return upsertCredentialLink(tx, credentialID, CredentialLinkInput{
			AssetID:  assetID,
			Relation: CredentialRelationVerified,
			Target:   target,
			Evidence: evidence,
		})
	})
}

// AggregateAuth 一次查询 asset_credential_links JOIN asset_credentials，同时取回
// 每个资产的 discovered 与 verified 关系。SELECT 列表刻意不包含 secret。
// 返回的每项形状与 Asset.Auth 回填一致：
//
//	{id, type, username, domain, relation, target, description, created_at, last_seen}
//
// description 对 discovered 取来源、对 verified 取认证证据。
func (s *CredentialStore) AggregateAuth(assetIDs []int64) (map[int64][]map[string]any, error) {
	out := map[int64][]map[string]any{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(assetIDs))
	args := make([]any, len(assetIDs))
	for i, id := range assetIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.querier().Query(`
SELECT l.asset_id, c.id, c.cred_type, c.username, c.domain,
       l.relation, l.target, l.source, l.evidence, c.extra,
       to_char(l.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
       to_char(l.last_seen  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
FROM asset_credential_links l
JOIN asset_credentials c ON c.id = l.credential_id
WHERE l.asset_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY l.asset_id, l.relation, l.last_seen DESC, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID int64
		var view CredentialView
		var extraRaw []byte
		if err := rows.Scan(
			&assetID, &view.ID, &view.CredType, &view.Username, &view.Domain,
			&view.Relation, &view.Target, &view.Source, &view.Evidence, &extraRaw,
			&view.CreatedAt, &view.LastSeen,
		); err != nil {
			return nil, err
		}
		if len(extraRaw) > 0 {
			_ = json.Unmarshal(extraRaw, &view.Extra)
		}
		description := view.Source
		if view.Relation == CredentialRelationVerified {
			description = view.Evidence
		}
		item := map[string]any{
			"id":          view.ID,
			"type":        view.CredType,
			"username":    view.Username,
			"domain":      view.Domain,
			"relation":    view.Relation,
			"target":      view.Target,
			"description": description,
			"created_at":  view.CreatedAt,
			"last_seen":   view.LastSeen,
		}
		out[assetID] = append(out[assetID], item)
	}
	return out, rows.Err()
}

// PopulateAuth 批量回填 assets 的 Auth 字段（脱敏）。空数组直接返回；聚合失败
// 向调用方返回错误，绝不把数据库异常当作「资产没有凭据」。
func (s *CredentialStore) PopulateAuth(assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(assets))
	seen := map[int64]bool{}
	for _, a := range assets {
		if a == nil || a.ID <= 0 || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		ids = append(ids, a.ID)
	}
	grouped, err := s.AggregateAuth(ids)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if a == nil {
			continue
		}
		if items, ok := grouped[a.ID]; ok {
			a.Auth = items
		} else {
			a.Auth = nil
		}
	}
	return nil
}

// IntranetCredentialRow 是内网凭据表的一行：脱敏凭据关系 + 所在资产/企业投影。
type IntranetCredentialRow struct {
	CredentialView
	AssetID     int64  `json:"asset_id"`
	AssetIP     string `json:"asset_ip"`
	AssetDomain string `json:"asset_domain,omitempty"`
	CompanyID   int64  `json:"company_id,omitempty"`
	CompanyName string `json:"company_name,omitempty"`
}

// intranetCredentialFilter 是内网凭据查询的公共 WHERE：资产 IP 落在 RFC1918
// 私网段（10/8、172.16/12、192.168/16，与 topologyInternalIPv4 同一口径），
// companyID=0 不按企业过滤。try_inet 容忍脏 IP：非法值返回 NULL 被自然排除。
const intranetCredentialFilter = `
FROM asset_credential_links l
JOIN asset_credentials c ON c.id = l.credential_id
JOIN assets a ON a.id = l.asset_id
LEFT JOIN companies co ON co.id = a.company_id
WHERE (try_inet(a.ip) <<= '10.0.0.0/8'::inet
    OR try_inet(a.ip) <<= '172.16.0.0/12'::inet
    OR try_inet(a.ip) <<= '192.168.0.0/16'::inet)
  AND ($1 <= 0 OR a.company_id = $1)`

// QueryIntranet 分页返回内网资产上的 discovered/verified 凭据关系（脱敏，无
// secret）。limit<=0 不分页；同时返回符合条件的总行数供分页展示。
func (s *CredentialStore) QueryIntranet(companyID int64, limit, offset int) ([]IntranetCredentialRow, int, error) {
	total := 0
	countRows, err := s.querier().Query(`SELECT count(*) `+intranetCredentialFilter, companyID)
	if err != nil {
		return nil, 0, err
	}
	if countRows.Next() {
		if err := countRows.Scan(&total); err != nil {
			countRows.Close()
			return nil, 0, err
		}
	}
	if err := countRows.Close(); err != nil {
		return nil, 0, err
	}
	if total == 0 || offset >= total {
		return []IntranetCredentialRow{}, total, nil
	}

	query := `
SELECT c.id, c.cred_type, c.username, c.domain,
       l.relation, l.target, l.source, l.evidence, c.extra,
       to_char(l.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
       to_char(l.last_seen  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
       a.id, a.ip, COALESCE(a.domain, ''), COALESCE(a.company_id, 0), COALESCE(co.name, '')` +
		intranetCredentialFilter + `
ORDER BY l.last_seen DESC, c.id`
	args := []any{companyID}
	if limit > 0 {
		query += ` LIMIT $2 OFFSET $3`
		args = append(args, limit, offset)
	}

	rows, err := s.querier().Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []IntranetCredentialRow{}
	for rows.Next() {
		var row IntranetCredentialRow
		var extraRaw []byte
		if err := rows.Scan(
			&row.ID, &row.CredType, &row.Username, &row.Domain,
			&row.Relation, &row.Target, &row.Source, &row.Evidence, &extraRaw,
			&row.CreatedAt, &row.LastSeen,
			&row.AssetID, &row.AssetIP, &row.AssetDomain, &row.CompanyID, &row.CompanyName,
		); err != nil {
			return nil, 0, err
		}
		if len(extraRaw) > 0 {
			_ = json.Unmarshal(extraRaw, &row.Extra)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// IsVisibleForTask 仅当凭据已有关联资产位于当前任务（或直接来源任务）范围内时
// 返回 true。供 worker 工具校验，防止按全局 ID 修改无关凭据。
func (s *CredentialStore) IsVisibleForTask(taskID, credentialID int64) (bool, error) {
	if taskID <= 0 || credentialID <= 0 {
		return false, nil
	}
	var visible bool
	err := s.querier().QueryRow(`WITH `+scopeTargetCTE+`
SELECT EXISTS(
    SELECT 1 FROM asset_credential_links l
    JOIN target ON target.id = l.asset_id
    WHERE l.credential_id = $2
)`, taskID, credentialID).Scan(&visible)
	if err != nil {
		return false, err
	}
	return visible, nil
}

// PruneOrphaned 删除已无任何资产关系的孤儿凭据，避免留下不可见的 secret。
// 在资产删除路径（级联删除关系之后）调用；返回删除行数。
func (s *CredentialStore) PruneOrphaned() (int64, error) {
	res, err := s.querier().Exec(`
DELETE FROM asset_credentials c
WHERE NOT EXISTS (SELECT 1 FROM asset_credential_links l WHERE l.credential_id = c.id)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// pruneOrphanedCredentialsTx 是 PruneOrphaned 的事务内版本，供已有打开事务的
// 删除路径（任务删除级联、公司删除、归档压实）复用。
func pruneOrphanedCredentialsTx(tx *sql.Tx) error {
	_, err := tx.Exec(`
DELETE FROM asset_credentials c
WHERE NOT EXISTS (SELECT 1 FROM asset_credential_links l WHERE l.credential_id = c.id)`)
	return err
}

// credentialInputFromAuthItem 把存量/兼容入口的 auth 条目转换为凭据写入输入。
// password 为空但 token 非空时使用 token；两者都为空时返回 false（跳过，不生成
// 可用凭据）。
func credentialInputFromAuthItem(item map[string]any) (CredentialInput, string, bool) {
	str := func(key string) string {
		v, _ := item[key].(string)
		return strings.TrimSpace(v)
	}
	raw := func(key string) string {
		v, _ := item[key].(string)
		return v
	}
	secret := raw("password")
	if strings.TrimSpace(secret) == "" {
		secret = raw("token")
	}
	if strings.TrimSpace(secret) == "" {
		return CredentialInput{}, "", false
	}
	return CredentialInput{
		CredType: str("type"),
		Username: str("username"),
		Domain:   str("domain"),
		Secret:   secret,
	}, str("description"), true
}

// recordDiscoveredAuth 把兼容入口（UpsertHTTPService/UpsertOtherService 的 Auth
// 参数）逐条登记为当前资产的 discovered 关系。复用调用方已打开的事务。
// target 取资产的服务名（如 http/ssh），source 取原 description。
func (s *AssetStore) recordDiscoveredAuth(assetID int64, target string, auth []map[string]any) error {
	if len(auth) == 0 || assetID <= 0 {
		return nil
	}
	creds := s.Credentials()
	for _, item := range auth {
		input, description, ok := credentialInputFromAuthItem(item)
		if !ok {
			continue
		}
		if _, err := creds.UpsertDiscovered(input, CredentialLinkInput{
			AssetID: assetID,
			Target:  target,
			Source:  description,
		}); err != nil {
			return err
		}
	}
	return nil
}

// credential_migration.go：存量 assets.auth JSONB 数组向 asset_credentials /
// asset_credential_links 的一次性迁移。
// 主要函数/类型：
//   - upgradeLegacyAssetAuthCredentials：展开每个资产的 auth 数组，按凭据唯一键
//     Upsert 身份、为每个原始资产补写 discovered 关系，同事务写入完成标记。
//
// 语义：迁移只产生 discovered 关系（历史数据无法证明认证成功）；同一凭据出现在
// 多个资产时，身份合并为一行，但每个原始资产都保留各自的 discovered 归属——
// 因此不能用 ON CONFLICT DO NOTHING 收尾，冲突时仍须取得凭据 ID 补写关系。
// 数据迁移与标记写入在同一事务：失败整体回滚，下次启动重试。

package server

import (
	"encoding/json"
	"log"
	"strings"

	"artex/db"
)

// legacyAssetAuthBackfillFlag 是迁移完成标记（settings 表）。
const legacyAssetAuthBackfillFlag = "asset_credential_auth_backfill_v1"

// upgradeLegacyAssetAuthCredentials 把存量 assets.auth 迁移为当前资产的
// discovered 凭据关系（幂等，只做一次）。失败返回 error 且不写标记。
func upgradeLegacyAssetAuthCredentials(pg *db.DB) error {
	if pg == nil {
		return nil
	}
	if v, _, err := pg.GetSetting(legacyAssetAuthBackfillFlag); err == nil && v == "true" {
		return nil
	}
	tx, err := pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.Query(`SELECT id, array_to_json(auth)::text FROM assets WHERE cardinality(auth) > 0 ORDER BY id`)
	if err != nil {
		return err
	}
	type assetAuth struct {
		id    int64
		items []map[string]any
	}
	var pending []assetAuth
	for rows.Next() {
		var a assetAuth
		var raw []byte
		if err := rows.Scan(&a.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &a.items); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, a)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, asset := range pending {
		for _, item := range asset.items {
			str := func(key string) string {
				v, _ := item[key].(string)
				return strings.TrimSpace(v)
			}
			raw := func(key string) string {
				v, _ := item[key].(string)
				return v
			}
			// password 为空但 token 非空时使用 token；secret 保留原始字符，不做 TrimSpace。
			secret := raw("password")
			if strings.TrimSpace(secret) == "" {
				secret = raw("token")
			}
			if strings.TrimSpace(secret) == "" {
				continue
			}
			credType := str("type")
			if credType == "" {
				credType = "unknown"
			}
			var credentialID int64
			if err := tx.QueryRow(`
INSERT INTO asset_credentials(cred_type, username, domain, secret)
VALUES ($1, $2, $3, $4)
ON CONFLICT (cred_type, username, domain, secret) DO UPDATE SET last_seen = now()
RETURNING id`,
				credType, str("username"), str("domain"), secret,
			).Scan(&credentialID); err != nil {
				return err
			}
			// 为每一个原始资产补写 discovered 关系（source 取原 description）；
			// 关系冲突只更新非空 source 与 last_seen，重复运行不增加行数。
			if _, err := tx.Exec(`
INSERT INTO asset_credential_links(credential_id, asset_id, relation, source)
VALUES ($1, $2, 'discovered', $3)
ON CONFLICT (credential_id, asset_id, relation) DO UPDATE SET
    source    = COALESCE(NULLIF(EXCLUDED.source, ''), asset_credential_links.source),
    last_seen = now()`,
				credentialID, asset.id, str("description"),
			); err != nil {
				return err
			}
		}
	}

	// 数据迁移与标记写入在同一事务中提交；失败整体回滚，下次启动重试。
	if _, err := tx.Exec(`
INSERT INTO settings(key, value) VALUES ($1, 'true')
ON CONFLICT (key) DO UPDATE SET value = 'true', updated_at = now()`,
		legacyAssetAuthBackfillFlag,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateLegacyAssetAuth 是启动迁移区接线：迁移失败只记日志，下次启动重试。
// 历史 assets.auth 列保留作回滚核查，迁移完成后的正常读取不再依赖它。
func (s *Server) migrateLegacyAssetAuth() {
	if s == nil || s.m == nil {
		return
	}
	if err := upgradeLegacyAssetAuthCredentials(s.m.pg); err != nil {
		log.Printf("[credentials] 存量 assets.auth 迁移失败（下次启动重试）: %v", err)
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// resolveSpawnTaskName gives every internal child a stable, asset-based display
// name. The asset key has already been validated by findingDispatchAssetKeys.
func resolveSpawnTaskName(scenarioNKey, raw string, assetKeys []string) string {
	name := strings.TrimSpace(raw)
	if scenarioNKey != "内网渗透" || len(assetKeys) != 1 {
		return name
	}
	host := strings.TrimPrefix(strings.TrimPrefix(assetKeys[0], "ip:"), "domain:")
	if host == "" {
		return name
	}
	return "内网渗透·" + host
}

// findingDispatchAssetKeys resolves the actual host linked to the standalone
// Finding, not every asset inherited from the parent task. Multiple services or
// endpoints on one host collapse to one key; multiple hosts require an explicit
// target_asset selected from those linked to the Finding.
func (s *Server) findingDispatchAssetKeys(findingID int64, targetAsset string) ([]string, error) {
	if findingID <= 0 {
		return nil, fmt.Errorf("内网派发必须提供 finding_id，用于按实际资产去重")
	}
	finding, err := s.m.PG().GetFinding(findingID)
	if err != nil {
		return nil, fmt.Errorf("读取 Finding #%d 失败：%w", findingID, err)
	}
	if finding == nil {
		return nil, fmt.Errorf("Finding #%d 不存在", findingID)
	}
	assets, err := s.m.PG().Assets().GetByIDs(finding.AssetIDs)
	if err != nil {
		return nil, fmt.Errorf("读取 Finding #%d 关联资产失败：%w", findingID, err)
	}
	keys := db.CanonicalAssetKeys(assets)
	if len(keys) == 0 {
		return nil, fmt.Errorf("Finding #%d 没有可规范化的 IP/域名资产，不能按资产派发", findingID)
	}
	targetAsset = strings.TrimSpace(targetAsset)
	if targetAsset == "" {
		if len(keys) > 1 {
			return nil, fmt.Errorf("Finding #%d 关联多个主机 %v，请用 target_asset 选择本次主目标", findingID, keys)
		}
		return keys, nil
	}
	selected := db.CanonicalAssetKey(&db.Asset{IP: targetAsset})
	if selected == "" {
		return nil, fmt.Errorf("target_asset %q 不是有效 IP/域名", targetAsset)
	}
	for _, key := range keys {
		if key == selected {
			return []string{key}, nil
		}
	}
	return nil, fmt.Errorf("target_asset %q 不在 Finding #%d 的关联资产 %v 中", targetAsset, findingID, keys)
}

var dispatchFindingIDPattern = regexp.MustCompile(`(?i)finding(?:_id)?\s*(?:#|=)\s*([0-9]+)`)

// backfillTaskDispatchTargets registers legacy internal tasks whose seven-part
// description contains finding #N or finding_id=N. Tasks are processed oldest-first, so a
// historical duplicate keeps the earliest task as the canonical dispatch
// target. Ambiguous multi-host tasks are skipped rather than guessed.
func (s *Server) backfillTaskDispatchTargets() {
	if s == nil || s.m == nil {
		return
	}
	rows, err := s.m.PG().Query(`SELECT t.id,t.description,t.goal FROM tasks t
		JOIN task_categories c ON c.id=t.category_id
		WHERE c.nkey='内网渗透'
		AND NOT EXISTS (SELECT 1 FROM task_dispatch_targets d WHERE d.task_id=t.id)
		ORDER BY t.id`)
	if err != nil {
		log.Printf("[dispatch] 历史资产目标扫描失败: %v", err)
		return
	}
	defer rows.Close()
	type candidate struct {
		id   int64
		text string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var description, goal string
		if err := rows.Scan(&c.id, &description, &goal); err == nil {
			c.text = description + "\n" + goal
			candidates = append(candidates, c)
		}
	}
	for _, c := range candidates {
		matches := dispatchFindingIDPattern.FindAllStringSubmatch(c.text, -1)
		keys := map[string]bool{}
		valid := len(matches) > 0
		for _, match := range matches {
			findingID, _ := strconv.ParseInt(match[1], 10, 64)
			found, err := s.findingDispatchAssetKeys(findingID, "")
			if err != nil {
				valid = false
				break
			}
			for _, key := range found {
				keys[key] = true
			}
		}
		if !valid || len(keys) != 1 {
			continue
		}
		for key := range keys {
			var findingID int64
			findingID, _ = strconv.ParseInt(matches[0][1], 10, 64)
			if _, err := s.m.PG().Exec(`INSERT INTO task_dispatch_targets
				(task_id,finding_id,asset_key) VALUES ($1,$2,$3) ON CONFLICT(asset_key) DO NOTHING`, c.id, findingID, key); err != nil {
				log.Printf("[dispatch] 历史任务 #%d 资产目标回填失败: %v", c.id, err)
			}
		}
	}
}

func (s *Server) toolCheckInternalAssetTask() actool.CoreTool {
	return roTool("check_internal_asset_task",
		"按 Finding 实际关联的 IP/域名检查是否已创建内网渗透子任务。端口不同的同一主机使用同一资产键；必须在 spawn_task 前调用。",
		objSchema(map[string]any{
			"finding_id":   map[string]any{"type": "integer", "description": "触发派发的全局 Finding id"},
			"target_asset": strParam("可选：Finding 关联多个主机时，选择本次主目标 IP/域名；必须属于该 Finding"),
		}, "finding_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FindingID   int64  `json:"finding_id"`
				TargetAsset string `json:"target_asset"`
			}
			_ = json.Unmarshal(in, &a)
			keys, err := s.findingDispatchAssetKeys(a.FindingID, a.TargetAsset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			targets, err := s.m.PG().ListTaskDispatchTargets(keys)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("查询内网资产派发记录失败：%v", err)), nil
			}
			return jsonResult(map[string]any{
				"finding_id": a.FindingID,
				"asset_keys": keys,
				"exists":     len(targets) > 0,
				"tasks":      targets,
			})
		})
}

// finding_chain_copy.go：把选中 finding 的 intent/fact/finding 祖先链复制到目标探索。
// 主要函数/类型：
//   - copyFindingChainTx：在调用方事务内复制 finding 祖先链，新任务与图同事务提交/回滚。
//   - findingIDsByNodeTx：按源探索的节点批量查询 finding id，缺行即报来源不可用。
//   - copiedFindingPayload：复制节点 payload，finding 节点补 source_finding_id/source_finding_node_id。

package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// copyFindingChainTx copies only the selected finding's intent/fact/finding
// ancestry. The caller owns the transaction so the new task and graph commit or
// roll back together.
func copyFindingChainTx(tx *sql.Tx, dstExplorationID, findingID int64) error {
	var sourceExplorationID, sourceFindingNodeID int64
	err := tx.QueryRow(`SELECT t.exploration_id, f.node_id
		FROM findings f
		JOIN tasks t ON t.id=f.task_id
		JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=t.exploration_id
		WHERE f.id=$1 AND t.deleted_at IS NULL AND n.kind='finding'
		FOR SHARE OF f, t, n`, findingID).Scan(&sourceExplorationID, &sourceFindingNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("finding %d: %w", findingID, ErrFindingOriginUnavailable)
	}
	if err != nil {
		return fmt.Errorf("read finding %d source: %w", findingID, err)
	}

	var dstOriginID int64
	if err := tx.QueryRow(`SELECT id FROM exploration_nodes
		WHERE exploration_id=$1 AND kind='fact' AND state='origin'
		ORDER BY id LIMIT 1`, dstExplorationID).Scan(&dstOriginID); err != nil {
		return fmt.Errorf("read destination origin: %w", err)
	}
	nodes, edges, err := queryFindingLineage(tx, sourceExplorationID, sourceFindingNodeID, true)
	if err != nil {
		return fmt.Errorf("read finding %d lineage: %w", findingID, err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("finding %d: %w", findingID, ErrFindingOriginUnavailable)
	}

	findingNodeIDs := make([]int64, 0, 1)
	selectedFound := false
	for _, node := range nodes {
		if node.ID == sourceFindingNodeID {
			selectedFound = true
		}
		if node.Kind == KindFinding {
			findingNodeIDs = append(findingNodeIDs, node.ID)
		}
	}
	if !selectedFound {
		return fmt.Errorf("finding %d: %w", findingID, ErrFindingOriginUnavailable)
	}
	sourceFindingIDs, err := findingIDsByNodeTx(tx, sourceExplorationID, findingNodeIDs)
	if err != nil {
		return err
	}

	idMap := make(map[int64]int64, len(nodes))
	for _, node := range nodes {
		if node.Kind == KindFact && node.State == "origin" {
			idMap[node.ID] = dstOriginID
		} else {
			state := node.State
			var blockedReason, deleteReason any
			if node.Kind == KindIntent {
				state = "done"
			} else {
				if node.BlockedReason != "" {
					blockedReason = node.BlockedReason
				}
				if node.DeleteReason != "" {
					deleteReason = node.DeleteReason
				}
			}
			payload, err := copiedFindingPayload(node, sourceFindingIDs)
			if err != nil {
				return err
			}
			var newID int64
			if err := tx.QueryRow(`INSERT INTO exploration_nodes(
				exploration_id, kind, payload, priority, state, origin, owner,
				blocked_reason, delete_reason, created_at)
				VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10)
				RETURNING id`, dstExplorationID, node.Kind, string(jsonbClean(payload)),
				node.Priority, state, node.Origin, node.Owner, blockedReason, deleteReason,
				node.CreatedAt).Scan(&newID); err != nil {
				return fmt.Errorf("copy finding %d node %d: %w", findingID, node.ID, err)
			}
			idMap[node.ID] = newID
		}
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id)
			SELECT $1, anchor.asset_id
			FROM exploration_anchors anchor
			JOIN exploration_nodes source ON source.id=anchor.node_id
			WHERE anchor.node_id=$2 AND source.exploration_id=$3
			ON CONFLICT DO NOTHING`, idMap[node.ID], node.ID, sourceExplorationID); err != nil {
			return fmt.Errorf("copy finding %d node %d anchors: %w", findingID, node.ID, err)
		}
	}

	for _, edge := range edges {
		from, fromOK := idMap[edge.From]
		to, toOK := idMap[edge.To]
		if !fromOK || !toOK {
			return fmt.Errorf("finding %d lineage edge %d->%d has an unmapped endpoint", findingID, edge.From, edge.To)
		}
		if from == to {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id,created_at)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			dstExplorationID, from, edge.Rel, to, edge.CreatedAt); err != nil {
			return fmt.Errorf("copy finding %d edge %d->%d: %w", findingID, edge.From, edge.To, err)
		}
	}
	return nil
}

func findingIDsByNodeTx(tx *sql.Tx, sourceExplorationID int64, nodeIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(`SELECT f.node_id, f.id
		FROM findings f
		JOIN tasks t ON t.id=f.task_id
		WHERE t.exploration_id=$1 AND f.node_id=ANY($2::bigint[])`, sourceExplorationID, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("read source finding records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, findingID int64
		if err := rows.Scan(&nodeID, &findingID); err != nil {
			return nil, err
		}
		out[nodeID] = findingID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, nodeID := range nodeIDs {
		if out[nodeID] <= 0 {
			return nil, fmt.Errorf("finding node %d: %w", nodeID, ErrFindingOriginUnavailable)
		}
	}
	return out, nil
}

func copiedFindingPayload(node *Node, sourceFindingIDs map[int64]int64) ([]byte, error) {
	if node.Kind != KindFinding {
		return node.Payload, nil
	}
	findingID := sourceFindingIDs[node.ID]
	if findingID <= 0 {
		return nil, fmt.Errorf("finding node %d: %w", node.ID, ErrFindingOriginUnavailable)
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode finding node %d payload: %w", node.ID, err)
	}
	findingJSON, _ := json.Marshal(findingID)
	nodeJSON, _ := json.Marshal(node.ID)
	payload["source_finding_id"] = findingJSON
	payload["source_finding_node_id"] = nodeJSON
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode finding node %d payload: %w", node.ID, err)
	}
	return raw, nil
}

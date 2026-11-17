// task_topology_editor.go：内网拓扑编辑器的节点/边/分组/便签编辑 HTTP 端点。
// 主要函数/类型：
//   - topologyTask / topologyEditorBody：任务存在性与「内网渗透」类目校验、限长 JSON 解码。
//   - topologyEditorGet / seedTopologyEditor：读取整张拓扑图与种子初始化。
//   - addTopologyEditorNode / patchTopologyEditorNode / deleteTopologyEditorNode：节点建/改/删（含资产回填）。
//   - addTopologyEditorEdge / patchTopologyEditorEdge / deleteTopologyEditorEdge：边的增删改。
//   - upsertTopologyEditorGroup / deleteTopologyEditorGroup：分组（大框）的写入与删除。
//   - topologyNodeGroupPatch：区分 group_id 缺省/null/数字三种语义。
package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	db "artex/db"
	"artex/xlog"
)

// 内网拓扑编辑器（阶段 A）HTTP 层：所有点都先过 s.topologyTask 做任务存在性
// 与"内网渗透"类目校验，且统一返回更新后的整张图。
const maxTopologyEditorBytes = 16 << 10

// topologyTask resolves the task id of an internal-pentest task. A non-empty
// reason means the request must be rejected with 404.
func (s *Server) topologyTask(r *http.Request) (int64, string) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok || s.m.pg == nil {
		return 0, "task not found"
	}
	if t.CategoryNKey != "内网渗透" {
		return 0, "内网拓扑仅适用于内网渗透任务"
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	if id <= 0 {
		return 0, "task not found"
	}
	return id, ""
}

// topologyEditorBody decodes a bounded JSON body for the editor endpoints.
func topologyEditorBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTopologyEditorBytes)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func (s *Server) topologyEditorGet(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	g, err := s.m.pg.TaskTopologyEditor(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// addTopologyEditorNode 建节点 + 首条边（同事务）。节点建立成功后回填任务资产；
// 资产回填失败只记日志——资产存储与拓扑不是同一连接，不能同事务。
func (s *Server) addTopologyEditorNode(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input struct {
		Node      db.TopologyNode `json:"node"`
		FirstEdge db.TopologyEdge `json:"first_edge"`
	}
	if !topologyEditorBody(w, r, &input) {
		return
	}
	if net.ParseIP(strings.TrimSpace(input.Node.IP)) == nil {
		writeErr(w, http.StatusBadRequest, "节点 IP 无效")
		return
	}
	g, err := s.m.pg.AddTopologyNode(taskID, "ARTEX", input.Node, input.FirstEdge)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.topologyAssetBackflow(taskID, input.Node.IP)
	writeJSON(w, http.StatusCreated, g)
}

// topologyAssetBackflow makes a manually added topology IP a task asset so it
// also shows up in the asset list and the coverage view (plan §1.1-12 / §7-0).
// RegisterTaskAssetScopes 是项目既有的“手工新增资产+范围”入口：一个事务内
// upsert type=ip 资产、写 task_asset_links(source=manual) 与 task_scope(ip)，同 IP 自动复用不重复建。
// 资产存储与拓扑表不是同一连接，所以这步失败只记日志、不回滚已提交的节点。
func (s *Server) topologyAssetBackflow(taskID int64, ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	store := s.m.Assets()
	if store == nil {
		return
	}
	if _, err := store.RegisterTaskAssetScopes(taskID, []db.ScopeInput{{Kind: "ip", Value: ip}}); err != nil {
		xlog.Errorf("[topology] 回填任务资产失败 task=%d ip=%s: %v", taskID, ip, err)
	}
}

// topologyNodeGroupPatch 区分 group_id 的三种语义：缺省=不改，null=移出大框，
// 数字=移入该大框。*int64 无法表达"缺省 vs null"，所以这里收原始 JSON。
func topologyNodeGroupPatch(raw json.RawMessage) (groupID *int64, clear bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	text := strings.TrimSpace(string(raw))
	if text == "null" {
		return nil, true, nil
	}
	id, parseErr := strconv.ParseInt(text, 10, 64)
	if parseErr != nil {
		return nil, false, errors.New("group_id 必须是数字或 null")
	}
	return &id, false, nil
}

func (s *Server) patchTopologyEditorNode(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input struct {
		Hostname     *string         `json:"hostname"`
		DisplayLabel *string         `json:"display_label"`
		X            *float64        `json:"x"`
		Y            *float64        `json:"y"`
		GroupID      json.RawMessage `json:"group_id"`
		EntryStatus  *string         `json:"entry_status"`
		Color        *string         `json:"color"`
		Description  *string         `json:"description"`
	}
	if !topologyEditorBody(w, r, &input) {
		return
	}
	groupID, clearGroup, err := topologyNodeGroupPatch(input.GroupID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := strings.TrimSpace(r.PathValue("ip"))
	if net.ParseIP(ip) == nil {
		writeErr(w, http.StatusBadRequest, "节点 IP 无效")
		return
	}
	g, err := s.m.pg.PatchTopologyNode(taskID, ip, "ARTEX", db.TopologyNodePatch{
		Hostname:     input.Hostname,
		DisplayLabel: input.DisplayLabel,
		X:            input.X,
		Y:            input.Y,
		GroupID:      groupID,
		ClearGroup:   clearGroup,
		EntryStatus:  input.EntryStatus,
		Color:        input.Color,
		Description:  input.Description,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteTopologyEditorNode(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	ip := strings.TrimSpace(r.PathValue("ip"))
	if net.ParseIP(ip) == nil {
		writeErr(w, http.StatusBadRequest, "节点 IP 无效")
		return
	}
	g, err := s.m.pg.DeleteTopologyNode(taskID, ip, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) addTopologyEditorEdge(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input db.TopologyEdge
	if !topologyEditorBody(w, r, &input) {
		return
	}
	g, err := s.m.pg.AddTopologyEdge(taskID, "ARTEX", input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) patchTopologyEditorEdge(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	edgeID, ok := pathInt(r, "eid")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad edge id")
		return
	}
	var input struct {
		SrcIP *string `json:"src_ip"`
		DstIP *string `json:"dst_ip"`
		Label *string `json:"label"`
		Kind  *string `json:"kind"`
		Color *string `json:"color"`
	}
	if !topologyEditorBody(w, r, &input) {
		return
	}
	g, err := s.m.pg.PatchTopologyEdge(taskID, edgeID, "ARTEX", db.TopologyEdgePatch{
		SrcIP: input.SrcIP,
		DstIP: input.DstIP,
		Label: input.Label,
		Kind:  input.Kind,
		Color: input.Color,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteTopologyEditorEdge(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	edgeID, ok := pathInt(r, "eid")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad edge id")
		return
	}
	g, err := s.m.pg.DeleteTopologyEdge(taskID, edgeID, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// upsertTopologyEditorGroup 建大框（无 id）或改大框（带 id）。
func (s *Server) upsertTopologyEditorGroup(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input db.TopologyGroup
	if !topologyEditorBody(w, r, &input) {
		return
	}
	if gid := r.PathValue("gid"); gid != "" {
		id, ok := pathInt(r, "gid")
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad group id")
			return
		}
		input.ID = id
	}
	g, err := s.m.pg.UpsertTopologyGroup(taskID, "ARTEX", input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.ID > 0 {
		writeJSON(w, http.StatusOK, g)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) deleteTopologyEditorGroup(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	groupID, ok := pathInt(r, "gid")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad group id")
		return
	}
	g, err := s.m.pg.DeleteTopologyGroup(taskID, groupID, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) addTopologyEditorNote(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input struct {
		NodeIP string `json:"node_ip"`
		Body   string `json:"body"`
	}
	if !topologyEditorBody(w, r, &input) {
		return
	}
	g, err := s.m.pg.AddTopologyNote(taskID, input.NodeIP, input.Body, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) seedTopologyEditor(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	g, err := s.m.pg.SeedTopologyFromAssets(taskID, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

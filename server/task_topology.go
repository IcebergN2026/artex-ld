package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	db "github.com/Autumn-27/artex/db"
)

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

func (s *Server) taskTopology(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	g, err := s.m.pg.TaskTopology(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) updateTaskTopologyNode(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input struct {
		RelationLabel string `json:"relation_label"`
		ParentIP      string `json:"parent_ip"`
		DisplayLabel  string `json:"display_label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	g, err := s.m.pg.UpdateTaskTopologyNode(taskID, db.TaskTopologyOverride{NodeIP: r.PathValue("ip"), RelationLabel: input.RelationLabel, ParentIP: input.ParentIP, DisplayLabel: input.DisplayLabel}, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) addTaskTopologyNote(w http.ResponseWriter, r *http.Request) {
	taskID, reason := s.topologyTask(r)
	if reason != "" {
		writeErr(w, http.StatusNotFound, reason)
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	g, err := s.m.pg.AddTaskTopologyNote(taskID, r.PathValue("ip"), input.Body, "ARTEX")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

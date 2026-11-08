package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMgmtAPI exercises the PostgreSQL-backed management API through the real mux.
func TestMgmtAPI(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping management API test", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping management API test")
	}
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	do := func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// agents list — must include at least the seeded builtins
	code, out := do("GET", "/api/agents", nil)
	if code != 200 {
		t.Fatalf("GET agents: %d", code)
	}
	if ags, _ := out["agents"].([]any); len(ags) < 5 {
		t.Fatalf("want >=5 agents (seeded builtins), got %d", len(ags))
	}

	// invalid template var → 400 (catalog whitelist)
	code, out = do("PUT", "/api/agents/planner/prompt", map[string]string{"template": "hi {{.Nope}}"})
	if code != 400 {
		t.Fatalf("bad var should be 400, got %d (%v)", code, out)
	}

	// valid template using seeded catalog vars → 200
	code, _ = do("PUT", "/api/agents/planner/prompt", map[string]string{"template": "你是规划者，目标：{{.Goal}}，摘要 {{.AssetSummary}}"})
	if code != 200 {
		t.Fatalf("valid prompt save: %d", code)
	}

	// preview renders with catalog examples
	code, out = do("POST", "/api/agents/planner/prompt/preview", map[string]any{})
	if code != 200 {
		t.Fatalf("preview: %d", code)
	}
	if rendered, _ := out["rendered"].(string); rendered == "" || rendered == "你是规划者，目标：{{.Goal}}，摘要 {{.AssetSummary}}" {
		t.Fatalf("preview did not substitute: %q", out["rendered"])
	}

	// mcp create → visible to planner → resource-side sees planner → delete clears it
	// Remove any leftover from prior run to keep the test idempotent.
	m.pg.Exec(`DELETE FROM mcp_servers WHERE name = 't-itest'`)
	code, out = do("POST", "/api/mcp", map[string]any{"name": "t-itest", "transport": "stdio", "command": "x", "enabled": true})
	if code != 200 {
		t.Fatalf("create mcp: %d", code)
	}
	mid := int64(out["id"].(float64))

	ag, _ := m.pg.GetAgentByKey("planner")
	code, _ = do("PUT", "/api/agents/planner/visibility", map[string]any{"mcp": []int64{mid}, "skill": []int64{}})
	if code != 200 {
		t.Fatalf("set visibility: %d", code)
	}
	code, out = do("GET", "/api/visibility/mcp/"+itoaTest(mid), nil)
	if code != 200 {
		t.Fatalf("resource visibility: %d", code)
	}
	if agents, _ := out["agents"].([]any); len(agents) != 1 || agents[0].(string) != itoaTest(ag.ID) {
		t.Fatalf("resource-side should list planner, got %v", out["agents"])
	}

	// cleanup
	do("DELETE", "/api/mcp/"+itoaTest(mid), nil)
	m.pg.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1`, ag.ID)
	m.pg.Exec(`UPDATE agents SET current_prompt_id=NULL WHERE id=$1`, ag.ID)
}

func itoaTest(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestAgentPlannerControlledPatch exercises the planner_controlled chain through
// the real mux: create defaults true, PATCH false round-trips to the DTO, an
// unrelated PATCH leaves it alone (pointer semantics), and built-in agents are
// rejected. Synthetic agent key only; no historical tasks touched.
func TestAgentPlannerControlledPatch(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping")
	}
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	m.pg.Exec(`DELETE FROM agents WHERE key = 't_pcpatch'`)

	do := func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	getPC := func() (bool, bool) {
		_, out := do("GET", "/api/agents", nil)
		for _, ag := range out["agents"].([]any) {
			am := ag.(map[string]any)
			if am["key"] == "t_pcpatch" {
				v, ok := am["planner_controlled"].(bool)
				return v, ok
			}
		}
		t.Fatal("t_pcpatch missing from /api/agents")
		return false, false
	}
	defer do("DELETE", "/api/agents/t_pcpatch", nil)

	// create → default true in DTO
	code, out := do("POST", "/api/agents", map[string]any{"key": "t_pcpatch", "name": "t pcpatch"})
	if code != 200 {
		t.Fatalf("create agent: %d (%v)", code, out)
	}
	if v, ok := out["planner_controlled"].(bool); !ok || !v {
		t.Fatalf("create DTO planner_controlled=%v (present=%v), want true", v, ok)
	}

	// PATCH false → DTO shows false
	code, _ = do("PUT", "/api/agents/t_pcpatch/config", map[string]any{"planner_controlled": false})
	if code != 200 {
		t.Fatalf("patch planner_controlled=false: %d", code)
	}
	if v, ok := getPC(); !ok || v {
		t.Fatalf("after patch false: planner_controlled=%v (present=%v), want false", v, ok)
	}

	// unrelated PATCH (no planner_controlled key) must not reset it
	code, _ = do("PUT", "/api/agents/t_pcpatch/config", map[string]any{"max_turns": 0})
	if code != 200 {
		t.Fatalf("patch max_turns: %d", code)
	}
	if v, ok := getPC(); !ok || v {
		t.Fatalf("after unrelated patch: planner_controlled=%v (present=%v), want still false", v, ok)
	}

	// PATCH true restores
	code, _ = do("PUT", "/api/agents/t_pcpatch/config", map[string]any{"planner_controlled": true})
	if code != 200 {
		t.Fatalf("patch planner_controlled=true: %d", code)
	}
	if v, _ := getPC(); !v {
		t.Fatal("after patch true: planner_controlled should be true")
	}

	// built-in agent rejected
	code, _ = do("PUT", "/api/agents/planner/config", map[string]any{"planner_controlled": false})
	if code != 400 {
		t.Fatalf("patch built-in planner_controlled: got %d, want 400", code)
	}
}

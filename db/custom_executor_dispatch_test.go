package db

import (
	"fmt"
	"testing"
)

// mustCustomProposedIntent creates a proposed intent with a custom executor key.
func mustCustomProposedIntent(t *testing.T, es *ExplorationStore, summary, executor string) int64 {
	t.Helper()
	payload := map[string]any{"summary": summary}
	if executor != "" {
		payload["executor"] = executor
	}
	id, err := es.AddNode(KindIntent, payload, 1, "proposed", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mustReviewProposedIntent creates a proposed intent with executor=review (human review).
func mustReviewProposedIntent(t *testing.T, es *ExplorationStore, summary string) int64 {
	t.Helper()
	id, err := es.AddProposedIntent(map[string]any{"summary": summary, "executor": "review"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDispatchFrontierAndExecutorClaim verifies candidate filtering, ordering,
// review exclusion, and CAS claim semantics for custom executor dispatch.
func TestDispatchFrontierAndExecutorClaim(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("dispatch frontier", "测试分派")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	customProposed := mustCustomProposedIntent(t, es, "custom proposed", "recon_scout")
	emptyProposed := mustCustomProposedIntent(t, es, "empty executor proposed", "")
	reviewProposed := mustReviewProposedIntent(t, es, "review proposed")
	customOpen := mustIntent(t, es, "custom open")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{executor}','"recon_scout"') WHERE id=$1`, customOpen); err != nil {
		t.Fatal(err)
	}
	defaultOpen := mustIntent(t, es, "default open")

	// Frontier must NOT include any proposed.
	fr, err := es.Frontier(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range fr {
		if n.State == "proposed" {
			t.Fatalf("Frontier returned proposed intent %d", n.ID)
		}
	}

	// DispatchFrontier includes open + custom/empty proposed, excludes review proposed.
	dfr, err := es.DispatchFrontier(50)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, n := range dfr {
		ids[n.ID] = true
	}
	if !ids[customProposed] || !ids[emptyProposed] || !ids[customOpen] || !ids[defaultOpen] {
		t.Fatalf("DispatchFrontier missing expected intents: %v", ids)
	}
	if ids[reviewProposed] {
		t.Fatal("DispatchFrontier must exclude review proposed")
	}

	// Ordering: priority DESC, id ASC. All have priority 1 here, so id ASC.
	for i := 1; i < len(dfr); i++ {
		if dfr[i-1].ID > dfr[i].ID {
			t.Fatalf("DispatchFrontier not ordered by id ASC at %d", i)
		}
	}

	// ClaimExecutorIntent on custom proposed.
	ok, err := es.ClaimExecutorIntent(customProposed, "proposed", "recon_scout", "exec:recon_scout")
	if err != nil || !ok {
		t.Fatalf("claim custom proposed: ok=%v err=%v", ok, err)
	}
	n, _ := es.GetNode(customProposed)
	if n.State != "running" || n.Owner != "exec:recon_scout" {
		t.Fatalf("custom proposed not claimed correctly: state=%s owner=%s", n.State, n.Owner)
	}

	// Duplicate claim fails.
	ok, err = es.ClaimExecutorIntent(customProposed, "proposed", "recon_scout", "exec:other")
	if err != nil || ok {
		t.Fatalf("duplicate claim should fail: ok=%v err=%v", ok, err)
	}

	// Empty executor proposed claimed by exec:<unassigned>.
	ok, err = es.ClaimExecutorIntent(emptyProposed, "proposed", "", "exec:<unassigned>")
	if err != nil || !ok {
		t.Fatalf("claim empty proposed: ok=%v err=%v", ok, err)
	}
	n, _ = es.GetNode(emptyProposed)
	if n.State != "running" || n.Owner != "exec:<unassigned>" {
		t.Fatalf("empty proposed not claimed correctly: state=%s owner=%s", n.State, n.Owner)
	}

	// Custom open claimed via ClaimExecutorIntent.
	ok, err = es.ClaimExecutorIntent(customOpen, "open", "recon_scout", "exec:recon_scout")
	if err != nil || !ok {
		t.Fatalf("claim custom open: ok=%v err=%v", ok, err)
	}

	// Default open claimed via ClaimIntent.
	ok, err = es.ClaimIntent(defaultOpen, "work#1")
	if err != nil || !ok {
		t.Fatalf("claim default open: ok=%v err=%v", ok, err)
	}

	// Review proposed cannot be claimed by ClaimExecutorIntent.
	ok, err = es.ClaimExecutorIntent(reviewProposed, "proposed", "review", "exec:review")
	if err == nil || ok {
		t.Fatalf("review proposed must not be claimable: ok=%v err=%v", ok, err)
	}

	// Concurrent CAS: only one winner.
	raceIntent := mustCustomProposedIntent(t, es, "race proposed", "racer")
	const goroutines = 8
	results := make(chan bool, goroutines)
	for i := range goroutines {
		go func(idx int) {
			ok, _ := es.ClaimExecutorIntent(raceIntent, "proposed", "racer", fmt.Sprintf("exec:racer-%d", idx))
			results <- ok
		}(i)
	}
	var winners int
	for range goroutines {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS winners=%d, want 1", winners)
	}
}

// TestCustomExecutorQueuedRecovery verifies that all recovery paths return custom
// intents to proposed and default intents to open, with owner cleared.
func TestCustomExecutorQueuedRecovery(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("queued recovery", "测试恢复")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// ResetRunningIntents: custom running → proposed, default running → open.
	customRunning := mustCustomProposedIntent(t, es, "custom running", "recon_scout")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='running', owner='exec:recon_scout' WHERE id=$1`, customRunning); err != nil {
		t.Fatal(err)
	}
	defaultRunning := mustIntent(t, es, "default running")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='running', owner='work#1' WHERE id=$1`, defaultRunning); err != nil {
		t.Fatal(err)
	}
	if n, _ := es.ResetRunningIntents(); n != 2 {
		t.Fatalf("ResetRunningIntents n=%d, want 2", n)
	}
	cn, _ := es.GetNode(customRunning)
	dn, _ := es.GetNode(defaultRunning)
	if cn.State != "proposed" || cn.Owner != "" {
		t.Fatalf("custom running recovery: state=%s owner=%s", cn.State, cn.Owner)
	}
	if dn.State != "open" || dn.Owner != "" {
		t.Fatalf("default running recovery: state=%s owner=%s", dn.State, dn.Owner)
	}

	// ReopenIntent: custom blocked → proposed, default blocked → open.
	customBlocked := mustCustomProposedIntent(t, es, "custom blocked", "recon_scout")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked' WHERE id=$1`, customBlocked); err != nil {
		t.Fatal(err)
	}
	defaultBlocked := mustIntent(t, es, "default blocked")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked' WHERE id=$1`, defaultBlocked); err != nil {
		t.Fatal(err)
	}
	if ok, _ := es.ReopenIntent(customBlocked); !ok {
		t.Fatal("ReopenIntent custom blocked failed")
	}
	if ok, _ := es.ReopenIntent(defaultBlocked); !ok {
		t.Fatal("ReopenIntent default blocked failed")
	}
	cn, _ = es.GetNode(customBlocked)
	dn, _ = es.GetNode(defaultBlocked)
	if cn.State != "proposed" {
		t.Fatalf("custom blocked recovery: state=%s", cn.State)
	}
	if dn.State != "open" {
		t.Fatalf("default blocked recovery: state=%s", dn.State)
	}

	// ReopenBlockedIntents: same semantics in batch.
	customBlocked2 := mustCustomProposedIntent(t, es, "custom blocked 2", "recon_scout")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked' WHERE id=$1`, customBlocked2); err != nil {
		t.Fatal(err)
	}
	defaultBlocked2 := mustIntent(t, es, "default blocked 2")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked' WHERE id=$1`, defaultBlocked2); err != nil {
		t.Fatal(err)
	}
	if n, _ := es.ReopenBlockedIntents(); n != 2 {
		t.Fatalf("ReopenBlockedIntents n=%d, want 2", n)
	}
	cn, _ = es.GetNode(customBlocked2)
	dn, _ = es.GetNode(defaultBlocked2)
	if cn.State != "proposed" {
		t.Fatalf("batch custom blocked recovery: state=%s", cn.State)
	}
	if dn.State != "open" {
		t.Fatalf("batch default blocked recovery: state=%s", dn.State)
	}

	// ReopenIntentsByBlockedReason: same semantics.
	customBlocked3 := mustCustomProposedIntent(t, es, "custom blocked 3", "recon_scout")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked', blocked_reason='quota' WHERE id=$1`, customBlocked3); err != nil {
		t.Fatal(err)
	}
	defaultBlocked3 := mustIntent(t, es, "default blocked 3")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='blocked', blocked_reason='quota' WHERE id=$1`, defaultBlocked3); err != nil {
		t.Fatal(err)
	}
	if n, _ := es.ReopenIntentsByBlockedReason("quota"); n != 2 {
		t.Fatalf("ReopenIntentsByBlockedReason n=%d, want 2", n)
	}
	cn, _ = es.GetNode(customBlocked3)
	dn, _ = es.GetNode(defaultBlocked3)
	if cn.State != "proposed" {
		t.Fatalf("reason custom blocked recovery: state=%s", cn.State)
	}
	if dn.State != "open" {
		t.Fatalf("reason default blocked recovery: state=%s", dn.State)
	}

	// CompareAndSetIntentState to open/proposed clears owner.
	ownerIntent := mustCustomProposedIntent(t, es, "owner clear", "recon_scout")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='running', owner='exec:recon_scout' WHERE id=$1`, ownerIntent); err != nil {
		t.Fatal(err)
	}
	if ok, _ := es.CompareAndSetIntentState(ownerIntent, "running", "proposed"); !ok {
		t.Fatal("CAS to proposed failed")
	}
	on, _ := es.GetNode(ownerIntent)
	if on.Owner != "" {
		t.Fatalf("CAS to proposed did not clear owner: %s", on.Owner)
	}

	// Review intent fields must remain unchanged.
	review := mustReviewProposedIntent(t, es, "review unchanged")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='running', owner='work#1' WHERE id=$1`, review); err != nil {
		t.Fatal(err)
	}
	if _, err := es.ResetRunningIntents(); err != nil {
		t.Fatal(err)
	}
	rn, _ := es.GetNode(review)
	if rn.State != "open" {
		t.Fatalf("review intent should recover to open (no custom executor): %s", rn.State)
	}
}

// mustGateIntent creates a custom-executor intent (any origin); state is always
// proposed; tests move it via SetIntentState or SQL UPDATE. policy only exercises
// that payload.planner_policy no longer affects the gate.
func mustGateIntent(t *testing.T, es *ExplorationStore, summary, executor, policy, origin string) int64 {
	t.Helper()
	payload := map[string]any{"summary": summary}
	if executor != "" {
		payload["executor"] = executor
	}
	if policy != "" {
		payload["planner_policy"] = policy
	}
	id, err := es.AddNode(KindIntent, payload, 1, "proposed", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mustGateAgent creates a custom agent declaring executorKey with the given
// planner_controlled value. Cleanup deletes it so subtests stay isolated.
func mustGateAgent(t *testing.T, d *DB, executorKey string, plannerControlled bool) {
	t.Helper()
	key := "t_gate_" + executorKey
	if _, err := d.Exec(`INSERT INTO agents(key, name, role, builtin, enabled, executor_key, planner_controlled)
VALUES ($1, $2, 'assistant', false, true, $3, $4)
ON CONFLICT (key) DO UPDATE SET executor_key=EXCLUDED.executor_key, planner_controlled=EXCLUDED.planner_controlled`,
		key, "gate agent "+executorKey, executorKey, plannerControlled); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM agents WHERE key=$1`, key) })
}

// setGateAgentPlannerControlled flips an existing gate agent's toggle to prove
// the gate reads the Agent's current configuration, not any intent snapshot.
func setGateAgentPlannerControlled(t *testing.T, d *DB, executorKey string, plannerControlled bool) {
	t.Helper()
	if _, err := d.Exec(`UPDATE agents SET planner_controlled=$2 WHERE executor_key=$1`, executorKey, plannerControlled); err != nil {
		t.Fatal(err)
	}
}

// TestHasPendingUncontrolledCustomIntent verifies the global "不受 Planner 控制" gate:
// any kind=intent with a custom (non-review, non-empty) executor whose Agent's
// CURRENT planner_controlled=false blocks the planner until done/deleted — for every
// origin (not just seeds), with payload.planner_policy ignored; missing Agent rows
// default to planner-controlled and never trigger the gate; with multiple intents any
// single pending one keeps the gate closed. All payloads/agents are synthetic.
func TestHasPendingUncontrolledCustomIntent(t *testing.T) {
	// Each subtest gets its own exploration so cases stay independent; the
	// defer follows the package's existing DSN/isolation cleanup pattern.
	newStore := func(t *testing.T, name string) (*DB, *ExplorationStore) {
		t.Helper()
		d, err := Open(testDSN(t))
		if err != nil {
			t.Skipf("postgres unavailable (%v) — skipping", err)
		}
		t.Cleanup(func() { d.Close() })
		expID, err := d.CreateExploration(name, "测试不受控意图门控")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
		return d, d.Exploration(expID)
	}

	// assertGate is the single observable: the gate bool for this exploration.
	assertGate := func(t *testing.T, es *ExplorationStore, want bool) {
		t.Helper()
		got, err := es.HasPendingUncontrolledCustomIntent()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("HasPendingUncontrolledCustomIntent=%v, want %v", got, want)
		}
	}

	t.Run("state matrix", func(t *testing.T) {
		for _, state := range []string{"proposed", "open", "running", "paused", "stopped", "blocked", "exhausted"} {
			t.Run(state, func(t *testing.T) {
				d, es := newStore(t, "gate state "+state)
				mustGateAgent(t, d, "recon_scout", false)
				id := mustGateIntent(t, es, "synthetic uncontrolled "+state, "recon_scout", "", "seed")
				if state != "proposed" {
					if err := es.SetIntentState(id, state); err != nil {
						t.Fatal(err)
					}
				}
				assertGate(t, es, true)
			})
		}
		for _, state := range []string{"done", "deleted"} {
			t.Run(state, func(t *testing.T) {
				d, es := newStore(t, "gate terminal "+state)
				mustGateAgent(t, d, "recon_scout", false)
				id := mustGateIntent(t, es, "synthetic terminal "+state, "recon_scout", "", "seed")
				if err := es.SetIntentState(id, state); err != nil {
					t.Fatal(err)
				}
				assertGate(t, es, false)
			})
		}
		t.Run("physically deleted", func(t *testing.T) {
			d, es := newStore(t, "gate physical delete")
			mustGateAgent(t, d, "recon_scout", false)
			id := mustGateIntent(t, es, "synthetic hard delete", "recon_scout", "", "seed")
			if _, err := es.db.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			assertGate(t, es, false)
		})
	})

	t.Run("agent toggle immediate", func(t *testing.T) {
		// The same intent flips with the Agent's CURRENT config — no snapshot.
		d, es := newStore(t, "gate toggle")
		mustGateAgent(t, d, "recon_scout", false)
		mustGateIntent(t, es, "synthetic toggled intent", "recon_scout", "", "seed")
		assertGate(t, es, true)
		setGateAgentPlannerControlled(t, d, "recon_scout", true)
		assertGate(t, es, false)
		setGateAgentPlannerControlled(t, d, "recon_scout", false)
		assertGate(t, es, true)
	})

	t.Run("planner policy ignored", func(t *testing.T) {
		// payload.planner_policy is no longer read: hold_until_done/planner/missing
		// all follow the Agent's current config.
		for _, policy := range []string{"hold_until_done", "planner", ""} {
			t.Run("uncontrolled/"+policy, func(t *testing.T) {
				d, es := newStore(t, "gate policy uncontrolled "+policy)
				mustGateAgent(t, d, "recon_scout", false)
				mustGateIntent(t, es, "synthetic policy "+policy, "recon_scout", policy, "seed")
				assertGate(t, es, true)
			})
		}
		t.Run("controlled hold_until_done", func(t *testing.T) {
			d, es := newStore(t, "gate policy controlled")
			mustGateAgent(t, d, "recon_scout", true)
			mustGateIntent(t, es, "synthetic held but controlled", "recon_scout", "hold_until_done", "seed")
			assertGate(t, es, false)
		})
	})

	t.Run("origin and executor filtering", func(t *testing.T) {
		// Non-seed origin (planner-created custom intent) is gated too: the gate
		// applies to ALL custom intents, not just the first seed.
		t.Run("non seed origin", func(t *testing.T) {
			d, es := newStore(t, "gate non seed")
			mustGateAgent(t, d, "recon_scout", false)
			mustGateIntent(t, es, "synthetic planner origin", "recon_scout", "", "planner")
			assertGate(t, es, true)
		})
		// review executor is never a custom executor ⇒ no gate, even if the Agent
		// row for it would be uncontrolled.
		t.Run("review executor", func(t *testing.T) {
			_, es := newStore(t, "gate review executor")
			mustGateIntent(t, es, "synthetic review intent", "review", "", "seed")
			assertGate(t, es, false)
		})
		// Empty executor (plain worker shape) ⇒ no gate.
		t.Run("empty executor", func(t *testing.T) {
			_, es := newStore(t, "gate empty executor")
			mustGateIntent(t, es, "synthetic default intent", "", "", "seed")
			assertGate(t, es, false)
		})
		// Agent row missing (executor_key declared by no agent) defaults to
		// planner-controlled ⇒ no false gate.
		t.Run("agent missing", func(t *testing.T) {
			_, es := newStore(t, "gate agent missing")
			mustGateIntent(t, es, "synthetic orphan executor", "unregistered_exec", "", "seed")
			assertGate(t, es, false)
		})
	})

	t.Run("multiple intents", func(t *testing.T) {
		// All done ⇒ gate open.
		t.Run("all done", func(t *testing.T) {
			d, es := newStore(t, "gate multi all done")
			mustGateAgent(t, d, "recon_scout", false)
			for i := range 2 {
				id := mustGateIntent(t, es, fmt.Sprintf("synthetic done intent %d", i), "recon_scout", "", "seed")
				if err := es.SetIntentState(id, "done"); err != nil {
					t.Fatal(err)
				}
			}
			assertGate(t, es, false)
		})
		// Any single non-terminal intent among done ones keeps the gate closed.
		for _, state := range []string{"running", "paused", "stopped", "blocked", "exhausted", "proposed", "open"} {
			t.Run("one "+state, func(t *testing.T) {
				d, es := newStore(t, "gate multi "+state)
				mustGateAgent(t, d, "recon_scout", false)
				done := mustGateIntent(t, es, "synthetic done peer", "recon_scout", "", "seed")
				if err := es.SetIntentState(done, "done"); err != nil {
					t.Fatal(err)
				}
				pending := mustGateIntent(t, es, "synthetic pending peer", "recon_scout", "", "seed")
				if state != "proposed" {
					if err := es.SetIntentState(pending, state); err != nil {
						t.Fatal(err)
					}
				}
				assertGate(t, es, true)
			})
		}
		// One physically deleted, one still paused ⇒ gate stays closed.
		t.Run("one physically deleted", func(t *testing.T) {
			d, es := newStore(t, "gate multi physical delete")
			mustGateAgent(t, d, "recon_scout", false)
			gone := mustGateIntent(t, es, "synthetic removed intent", "recon_scout", "", "seed")
			if _, err := es.db.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, gone); err != nil {
				t.Fatal(err)
			}
			held := mustGateIntent(t, es, "synthetic held peer", "recon_scout", "", "seed")
			if err := es.SetIntentState(held, "paused"); err != nil {
				t.Fatal(err)
			}
			assertGate(t, es, true)
		})
		// One controlled intent + one uncontrolled pending ⇒ gate closed by the
		// uncontrolled one alone.
		t.Run("mixed agents", func(t *testing.T) {
			d, es := newStore(t, "gate multi mixed agents")
			mustGateAgent(t, d, "recon_scout", true)
			mustGateAgent(t, d, "probe_deep", false)
			mustGateIntent(t, es, "synthetic controlled intent", "recon_scout", "", "seed")
			pending := mustGateIntent(t, es, "synthetic uncontrolled peer", "probe_deep", "", "seed")
			if err := es.SetIntentState(pending, "running"); err != nil {
				t.Fatal(err)
			}
			assertGate(t, es, true)
		})
	})
}

// TestIntentDeleteExpectedStateRace verifies that claim and soft/hard cancel
// racing never produces a deleted node that is still running.
func TestIntentDeleteExpectedStateRace(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("delete race", "测试删除竞态")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	for _, startState := range []string{"proposed", "open"} {
		t.Run(startState, func(t *testing.T) {
			// Claim wins first: delete with stale expectedState must conflict.
			intent := mustCustomProposedIntent(t, es, "race "+startState, "racer")
			if startState == "open" {
				if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, intent); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := es.ClaimExecutorIntent(intent, startState, "racer", "exec:racer")
			if err != nil || !ok {
				t.Fatalf("claim: ok=%v err=%v", ok, err)
			}
			// Now state is running; expectedState=startState must conflict.
			if _, err := es.SoftDeleteIntent(intent, "stale delete", startState); err == nil {
				t.Fatal("soft delete with stale expectedState unexpectedly succeeded")
			}
			if _, err := es.CancelIntent(intent, startState); err == nil {
				t.Fatal("hard cancel with stale expectedState unexpectedly succeeded")
			}
			n, _ := es.GetNode(intent)
			if n.State != "running" {
				t.Fatalf("intent should still be running after conflicting deletes: %s", n.State)
			}

			// Delete wins first: claim must fail.
			intent2 := mustCustomProposedIntent(t, es, "race2 "+startState, "racer")
			if startState == "open" {
				if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, intent2); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := es.SoftDeleteIntent(intent2, "win delete", startState); err != nil {
				t.Fatal(err)
			}
			ok, err = es.ClaimExecutorIntent(intent2, startState, "racer", "exec:racer")
			if err != nil || ok {
				t.Fatalf("claim after delete should fail: ok=%v err=%v", ok, err)
			}
			n, _ = es.GetNode(intent2)
			if n.State != "deleted" {
				t.Fatalf("intent2 should be deleted: %s", n.State)
			}

			// Hard delete wins first: claim must fail, node physically gone.
			intent3 := mustCustomProposedIntent(t, es, "race3 "+startState, "racer")
			if startState == "open" {
				if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, intent3); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := es.CancelIntent(intent3, startState); err != nil {
				t.Fatal(err)
			}
			ok, err = es.ClaimExecutorIntent(intent3, startState, "racer", "exec:racer")
			if err != nil || ok {
				t.Fatalf("claim after hard delete should fail: ok=%v err=%v", ok, err)
			}
			if !gone(t, es, intent3) {
				t.Fatal("intent3 should be physically deleted")
			}
		})
	}
}

// TestHasCustomExecutorIntentActive verifies the per-executor activity gate used to
// decide whether a custom executor phase has ended: only intents whose
// payload.executor equals the key (non-empty, non-review) in state
// proposed/open/running/paused count as active; done/blocked/exhausted/stopped/deleted
// do not; intents for other executors are irrelevant; origin (e.g. planner) is not
// filtered. All payloads are synthetic.
func TestHasCustomExecutorIntentActive(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("custom active gate", "测试执行器活动门控")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// Active states: proposed/open/running/paused all report active. Each case uses
	// its own executor key so cases stay independent within one exploration.
	for _, st := range []string{"proposed", "open", "running", "paused"} {
		key := "act_" + st
		id := mustGateIntent(t, es, "active "+st, key, "", "planner")
		if st != "proposed" {
			if _, err := es.db.Exec(`UPDATE exploration_nodes SET state=$2 WHERE id=$1`, id, st); err != nil {
				t.Fatal(err)
			}
		}
		active, err := es.HasCustomExecutorIntentActive(key)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			t.Fatalf("state %s must report active", st)
		}
	}

	// Inactive states: done/blocked/exhausted/stopped/deleted all report inactive.
	for _, st := range []string{"done", "blocked", "exhausted", "stopped", "deleted"} {
		key := "inact_" + st
		id := mustGateIntent(t, es, "inactive "+st, key, "", "planner")
		if _, err := es.db.Exec(`UPDATE exploration_nodes SET state=$2 WHERE id=$1`, id, st); err != nil {
			t.Fatal(err)
		}
		active, err := es.HasCustomExecutorIntentActive(key)
		if err != nil {
			t.Fatal(err)
		}
		if active {
			t.Fatalf("state %s must report inactive", st)
		}
	}

	// Hard-deleted (no row) also reports inactive.
	goneID := mustGateIntent(t, es, "hard deleted", "gone_key", "", "planner")
	if _, err := es.CancelIntent(goneID, "proposed"); err != nil {
		t.Fatal(err)
	}
	if active, err := es.HasCustomExecutorIntentActive("gone_key"); err != nil || active {
		t.Fatalf("hard-deleted intent must be inactive: active=%v err=%v", active, err)
	}

	// Different executor: an active intent for executor B must not make A active.
	idA := mustGateIntent(t, es, "other executor", "execB_active", "", "planner")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='running' WHERE id=$1`, idA); err != nil {
		t.Fatal(err)
	}
	if active, err := es.HasCustomExecutorIntentActive("execA_quiet"); err != nil || active {
		t.Fatalf("other executor must be inactive: active=%v err=%v", active, err)
	}

	// origin=planner custom intent counts as active (origin not filtered).
	originKey := "origin_planner"
	id := mustGateIntent(t, es, "planner origin", originKey, "", "planner")
	if _, err := es.db.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if active, err := es.HasCustomExecutorIntentActive(originKey); err != nil || !active {
		t.Fatalf("planner-origin custom intent must be active: active=%v err=%v", active, err)
	}

	// review / empty executor keys are never active, even with a matching review row present.
	mustReviewProposedIntent(t, es, "review not active")
	for _, key := range []string{"", "review"} {
		if active, err := es.HasCustomExecutorIntentActive(key); err != nil || active {
			t.Fatalf("key %q must never be active: active=%v err=%v", key, active, err)
		}
	}
}

package api

import (
	"clonarr/internal/core"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestAppWithRules builds a minimal app + Server seeded with the
// given instances, rules, and history. Used by the restore-handler tests
// to exercise the early-return code paths (404, 412) that don't make
// any Arr HTTP calls — those need an httptest mock and live elsewhere.
func setupTestAppWithRules(t *testing.T, instances []core.Instance, rules []core.AutoSyncRule, history []core.SyncHistoryEntry) *core.App {
	t.Helper()
	tempDir := t.TempDir()
	config := core.NewConfigStore(tempDir)
	dummyCfg := core.Config{
		Instances:   instances,
		AutoSync:    core.AutoSyncConfig{Rules: rules},
		SyncHistory: history,
	}
	cfgData, _ := json.MarshalIndent(dummyCfg, "", "  ")
	os.WriteFile(filepath.Join(tempDir, "clonarr.json"), cfgData, 0644)
	if err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return &core.App{
		Config:   config,
		DebugLog: core.NewDebugLogger(tempDir),
	}
}

// TestHandleRestoreAutoSyncRule_NotFound covers the 404 path: a rule ID
// that doesn't match any rule in the config.
func TestHandleRestoreAutoSyncRule_NotFound(t *testing.T) {
	app := setupTestAppWithRules(t, nil, nil, nil)
	server := &Server{Core: app}

	r := httptest.NewRequest(http.MethodPost, "/api/auto-sync/rules/missing/restore", nil)
	r.SetPathValue("id", "missing")
	w := httptest.NewRecorder()

	server.handleRestoreAutoSyncRule(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleRestoreAutoSyncRule_NotOrphaned covers the 412 invariant
// guard: Restore only applies to orphaned rules. A live rule shouldn't
// be re-creatable — the user should manage it through the normal sync
// path. Returning 412 (Precondition Failed) signals that the resource
// isn't in the right state for this action.
func TestHandleRestoreAutoSyncRule_NotOrphaned(t *testing.T) {
	app := setupTestAppWithRules(t,
		[]core.Instance{{ID: "inst-A", Name: "Radarr", Type: "radarr", URL: "http://x", APIKey: "k"}},
		[]core.AutoSyncRule{
			{ID: "rule-1", InstanceID: "inst-A", ArrProfileID: 10}, // OrphanedAt == ""
		},
		nil,
	)
	server := &Server{Core: app}

	r := httptest.NewRequest(http.MethodPost, "/api/auto-sync/rules/rule-1/restore", nil)
	r.SetPathValue("id", "rule-1")
	w := httptest.NewRecorder()

	server.handleRestoreAutoSyncRule(w, r)

	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", w.Code)
	}
}

// TestHandleRestoreAutoSyncRule_NoHistory covers the 412 case where a
// rule IS orphaned but has no SyncHistoryEntry to restore from. Without
// history we don't know what intent to push to Arr. Refuse cleanly.
func TestHandleRestoreAutoSyncRule_NoHistory(t *testing.T) {
	app := setupTestAppWithRules(t,
		[]core.Instance{{ID: "inst-A", Name: "Radarr", Type: "radarr", URL: "http://x", APIKey: "k"}},
		[]core.AutoSyncRule{
			{ID: "rule-1", InstanceID: "inst-A", ArrProfileID: 10, OrphanedAt: "2026-04-26"},
		},
		nil, // no history
	)
	server := &Server{Core: app}

	r := httptest.NewRequest(http.MethodPost, "/api/auto-sync/rules/rule-1/restore", nil)
	r.SetPathValue("id", "rule-1")
	w := httptest.NewRecorder()

	server.handleRestoreAutoSyncRule(w, r)

	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", w.Code)
	}
	body := w.Body.String()
	if !contains(body, "No sync history") {
		t.Errorf("expected 'No sync history' in response, got: %s", body)
	}
}

// TestHandleRestoreAutoSyncRule_InstanceMissing covers the case where
// a rule's InstanceID points to an instance that's been deleted from
// clonarr's config. Defensive guard — without a target instance we
// can't reach Arr to recreate anything.
func TestHandleRestoreAutoSyncRule_InstanceMissing(t *testing.T) {
	app := setupTestAppWithRules(t,
		nil, // no instances
		[]core.AutoSyncRule{
			{ID: "rule-1", InstanceID: "ghost", ArrProfileID: 10, OrphanedAt: "2026-04-26"},
		},
		[]core.SyncHistoryEntry{
			{InstanceID: "ghost", ArrProfileID: 10, ProfileName: "Old"},
		},
	)
	server := &Server{Core: app}

	r := httptest.NewRequest(http.MethodPost, "/api/auto-sync/rules/rule-1/restore", nil)
	r.SetPathValue("id", "rule-1")
	w := httptest.NewRecorder()

	server.handleRestoreAutoSyncRule(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// contains reports whether substr appears within s. Avoids importing strings
// just for this helper in the test file.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// The "Customize this profile" toggle is editor state: it tells the editor
// what to show, never the sync engine what to push. Two rules that differ
// only there must count as equivalent, otherwise a save that just flips the
// toggle bumps UpdatedAt and the rule card claims unsynced changes that do
// not exist.
func TestAutoSyncRuleEquivalent_IgnoresCustomizeEnabled(t *testing.T) {
	on, off := true, false
	base := core.AutoSyncRule{
		ID:             "r1",
		InstanceID:     "i1",
		ArrProfileID:   7,
		TrashProfileID: "remux-web-1080p",
		SelectedCFs:    []string{"cf-a", "cf-b"},
		ScoreOverrides: map[string]int{"cf-a": 120},
	}
	withOn, withOff := base, base
	withOn.CustomizeEnabled = &on
	withOff.CustomizeEnabled = &off

	if !autoSyncRuleEquivalent(withOn, withOff) {
		t.Error("rules differing only in CustomizeEnabled must be equivalent")
	}
	if !autoSyncRuleEquivalent(withOn, base) {
		t.Error("a rule with the toggle set must equal the same rule without it")
	}

	// Sanity: a real sync-input difference still registers.
	changed := base
	changed.CustomizeEnabled = &on
	changed.ScoreOverrides = map[string]int{"cf-a": 500}
	if autoSyncRuleEquivalent(withOn, changed) {
		t.Error("a changed score override must not be equivalent")
	}
}

// The rule PUT handler replaces the stored rule wholesale, so it has to
// re-preserve anything the caller does not own. Callers that only flip
// `enabled`, or that echo a rule loaded earlier, must not wipe the profile
// editor's Customize toggle: the editor would then fall back to inferring it
// and could hide custom formats that still sync.
func TestHandleUpdateAutoSyncRule_PreservesCustomizeEnabled(t *testing.T) {
	on := true
	app := setupTestAppWithRules(t,
		[]core.Instance{{ID: "inst-preserve", Name: "Radarr", Type: "radarr", URL: "http://x", APIKey: "k"}},
		[]core.AutoSyncRule{
			{ID: "rule-1", InstanceID: "inst-preserve", ArrProfileID: 10, CustomizeEnabled: &on},
		},
		nil,
	)
	server := &Server{Core: app}

	// Body omits customizeEnabled entirely - the shape a partial caller sends.
	body := `{"id":"rule-1","instanceId":"inst-preserve","arrProfileId":10,"enabled":true}`
	r := httptest.NewRequest(http.MethodPut, "/api/auto-sync/rules/rule-1", strings.NewReader(body))
	r.SetPathValue("id", "rule-1")
	w := httptest.NewRecorder()

	server.handleUpdateAutoSyncRule(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	stored := app.Config.Get().AutoSync.Rules[0]
	if stored.CustomizeEnabled == nil {
		t.Fatal("CustomizeEnabled was wiped by a PUT that did not carry it")
	}
	if !*stored.CustomizeEnabled {
		t.Error("CustomizeEnabled = false, want true preserved from disk")
	}

	// A body that DOES carry the field still overwrites it.
	body = `{"id":"rule-1","instanceId":"inst-preserve","arrProfileId":10,"customizeEnabled":false}`
	r = httptest.NewRequest(http.MethodPut, "/api/auto-sync/rules/rule-1", strings.NewReader(body))
	r.SetPathValue("id", "rule-1")
	w = httptest.NewRecorder()

	server.handleUpdateAutoSyncRule(w, r)

	stored = app.Config.Get().AutoSync.Rules[0]
	if stored.CustomizeEnabled == nil || *stored.CustomizeEnabled {
		t.Error("an explicit customizeEnabled:false must be stored")
	}
}

// Config.Get() hands out a copy. Pointer fields have to be cloned, or a caller
// holding that copy can write through the pointer and mutate the canonical
// config outside the store's lock and without a save.
func TestConfigGet_ClonesCustomizeEnabledPointer(t *testing.T) {
	on := true
	app := setupTestAppWithRules(t,
		[]core.Instance{{ID: "inst-clone", Name: "Radarr", Type: "radarr", URL: "http://x", APIKey: "k"}},
		[]core.AutoSyncRule{
			{ID: "rule-1", InstanceID: "inst-clone", ArrProfileID: 10, CustomizeEnabled: &on},
		},
		nil,
	)
	first := app.Config.Get().AutoSync.Rules[0]
	if first.CustomizeEnabled == nil {
		t.Fatal("CustomizeEnabled did not survive load")
	}
	*first.CustomizeEnabled = false

	second := app.Config.Get().AutoSync.Rules[0]
	if second.CustomizeEnabled == nil || !*second.CustomizeEnabled {
		t.Error("writing through a Get() copy changed the stored config - pointer is shared")
	}
}

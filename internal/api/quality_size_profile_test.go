package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"clonarr/internal/arr"
	"clonarr/internal/core"
)

func qsItem(name string, allowed bool, min, pref, max *float64) arr.ArrQualityItem {
	return arr.ArrQualityItem{
		Quality:       &arr.ArrQualityRef{Name: name},
		Items:         []arr.ArrQualityItem{},
		Allowed:       allowed,
		MinSize:       min,
		PreferredSize: pref,
		MaxSize:       max,
	}
}

func f(v float64) *float64 { return &v }

// buildQSProfileRows is what backs the tab's table, and it has to work for a
// profile that exists and for the blank template the app offers for one that
// does not. Both come in as the same []ArrQualityItem, so one test covers it.
func TestBuildQSProfileRowsFlattensLeavesAndAttributes(t *testing.T) {
	items := []arr.ArrQualityItem{
		qsItem("SDTV", false, f(2), f(95), f(100)),
		{ID: 1000, Name: "WEB 1080p", Allowed: true, Items: []arr.ArrQualityItem{
			qsItem("WEBRip-1080p", true, f(4), f(95), f(130)),
			qsItem("WEBDL-1080p", true, f(4), f(95), f(130)),
		}},
		qsItem("Bluray-1080p", true, f(4), f(95), f(155)),
	}
	sizes := map[string]core.QualitySizeTriple{
		"WEBDL-1080p":  {Min: f(15), Preferred: f(995), Max: f(1000)},
		"Bluray-1080p": {Min: f(7), Preferred: f(40), Max: f(60)},
	}
	overrides := map[string]core.QSOverride{"Bluray-1080p": {Min: 7, Preferred: 40, Max: 60}}

	rows := buildQSProfileRows(items, sizes, overrides, nil)
	if len(rows) != 4 {
		t.Fatalf("want four leaves, got %d", len(rows))
	}
	by := map[string]qsProfileRow{}
	for _, r := range rows {
		by[r.Quality] = r
	}
	// A group row is not a row of its own; it is context on its members.
	if _, ok := by["WEB 1080p"]; ok {
		t.Error("the group row must not become a row")
	}
	if by["WEBDL-1080p"].Group != "WEB 1080p" {
		t.Errorf("group: got %q", by["WEBDL-1080p"].Group)
	}
	// Attribution is computed here, not inferred by the frontend.
	if by["WEBDL-1080p"].Source != "set" {
		t.Errorf("WEBDL-1080p: want source set, got %q", by["WEBDL-1080p"].Source)
	}
	if by["Bluray-1080p"].Source != "override" {
		t.Errorf("Bluray-1080p: want source override, got %q", by["Bluray-1080p"].Source)
	}
	// A quality the chosen set does not name gets no resolved value and no
	// source, which is how the tab says "left as is".
	if by["SDTV"].Source != "" || by["SDTV"].Resolved.Min != nil {
		t.Errorf("SDTV: want nothing written, got source %q", by["SDTV"].Source)
	}
	// The instance's own values come through for every row either way.
	if by["SDTV"].Current.Min == nil || *by["SDTV"].Current.Min != 2 {
		t.Errorf("SDTV current: want 2, got %v", by["SDTV"].Current.Min)
	}
	// Highest priority first, which is the order both Arr UIs show.
	if rows[0].Quality != "Bluray-1080p" || rows[len(rows)-1].Quality != "SDTV" {
		t.Errorf("order: got %s … %s", rows[0].Quality, rows[len(rows)-1].Quality)
	}
}

// The blank-profile template the app offers for a profile that does not exist
// yet has every quality switched off, so its allowed flags say nothing about
// the guide profile the user is looking at. Without the override every row
// claimed "not allowed" about a profile that does allow some, and the default
// view, which lists what the profile allows, came out empty.
func TestBuildQSProfileRowsTakesAllowedFromTheGuideProfile(t *testing.T) {
	blank := []arr.ArrQualityItem{
		qsItem("SDTV", false, nil, nil, nil),
		{ID: 1000, Name: "WEB 1080p", Allowed: false, Items: []arr.ArrQualityItem{
			qsItem("WEBRip-1080p", false, nil, nil, nil),
			qsItem("WEBDL-1080p", false, nil, nil, nil),
		}},
	}
	rows := buildQSProfileRows(blank, nil, nil, map[string]bool{
		"WEBDL-1080p":  true,
		"WEBRip-1080p": true,
	})
	by := map[string]qsProfileRow{}
	for _, r := range rows {
		by[r.Quality] = r
	}
	for _, name := range []string{"WEBDL-1080p", "WEBRip-1080p"} {
		if !by[name].Allowed {
			t.Errorf("%s: the guide profile allows it, want allowed", name)
		}
	}
	if by["SDTV"].Allowed {
		t.Error("SDTV: the guide profile does not allow it, want not allowed")
	}
}

// A group's members inherit its allowed state, which is what the profile does.
func TestGuideAllowedQualitiesExpandsGroups(t *testing.T) {
	ad := &core.AppData{Profiles: []*core.TrashQualityProfile{{
		TrashID: "web-1080p",
		Items: []core.QualityItem{
			{Name: "SDTV", Allowed: false},
			{Name: "WEB 1080p", Allowed: true, Items: []string{"WEBDL-1080p", "WEBRip-1080p"}},
			{Name: "Bluray-1080p", Allowed: true},
		},
	}}}
	got := guideAllowedQualities(ad, "web-1080p")
	want := map[string]bool{"WEBDL-1080p": true, "WEBRip-1080p": true, "Bluray-1080p": true}
	if len(got) != len(want) {
		t.Fatalf("want %d allowed, got %d (%v)", len(want), len(got), got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("%s: want allowed", name)
		}
	}
	// A profile the guide does not have must not be reported as "allows
	// nothing", which would hide every row. Nil means "no opinion".
	if guideAllowedQualities(ad, "not-a-profile") != nil {
		t.Error("unknown trash id: want nil")
	}
	if guideAllowedQualities(nil, "web-1080p") != nil {
		t.Error("no app data: want nil")
	}
}

// Deleting a set a rule points at used to be refused. It now goes through and
// the rule keeps writing the same numbers as its own per-quality values, which
// is the inverse of what "Save as set" did. The rule must not be left pointing
// at a set that no longer exists, because that resolves to nothing and would
// quietly stop it writing sizes.
func TestDeleteQualitySizeSetConvertsRulesToOwnValues(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.QualitySizeSets = []core.QualitySizeSet{{
		ID:      "set-1",
		Name:    "Min Test",
		AppType: "sonarr",
		Qualities: []core.QualitySizeEntry{
			{Quality: "WEBDL-1080p", Min: 7, Preferred: 40, Max: 60},
			{Quality: "WEBRip-1080p", Min: 15, Preferred: 995, Max: 1000},
		},
	}}
	cfg.Instances = []core.Instance{{ID: "inst-1", Name: "Sonarr v5", Type: "sonarr", URL: "http://x", APIKey: "k"}}
	cfg.AutoSync.Rules = []core.AutoSyncRule{
		{
			ID: "r-points-at-set", InstanceID: "inst-1", ArrProfileID: 4,
			QualitySizeSource: "custom:set-1",
			// An explicit value already beat the set, so it has to survive.
			QualitySizeOverrides: map[string]core.QSOverride{
				"WEBDL-1080p": {Min: 1, Preferred: 2, Max: 3},
			},
		},
		{ID: "r-elsewhere", InstanceID: "inst-1", ArrProfileID: 5, QualitySizeSource: "guide:abc"},
	}

	s, _ := setupConfigScheduleServer(t, cfg)

	if used := s.qsSetUsedBy("set-1"); len(used) != 1 || used[0] != "Sonarr v5" {
		t.Fatalf("qsSetUsedBy = %v, want [Sonarr v5]", used)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/quality-size-sets/set-1", nil)
	req.SetPathValue("id", "set-1")
	rec := httptest.NewRecorder()
	s.handleDeleteQualitySizeSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	got := s.Core.Config.Get()
	if len(got.QualitySizeSets) != 0 {
		t.Errorf("the set is still there: %+v", got.QualitySizeSets)
	}
	var converted, untouched *core.AutoSyncRule
	for i := range got.AutoSync.Rules {
		switch got.AutoSync.Rules[i].ID {
		case "r-points-at-set":
			converted = &got.AutoSync.Rules[i]
		case "r-elsewhere":
			untouched = &got.AutoSync.Rules[i]
		}
	}
	if converted == nil || untouched == nil {
		t.Fatal("rules missing after delete")
	}
	// "off" and not "", or no partial writer could tell it apart from a field
	// that was never sent.
	if converted.QualitySizeSource != core.QSSourceOff {
		t.Errorf("source = %q, want %q", converted.QualitySizeSource, core.QSSourceOff)
	}
	if n := len(converted.QualitySizeOverrides); n != 2 {
		t.Fatalf("overrides = %d, want 2: %+v", n, converted.QualitySizeOverrides)
	}
	if ov := converted.QualitySizeOverrides["WEBRip-1080p"]; ov.Min != 15 || ov.Preferred != 995 || ov.Max != 1000 {
		t.Errorf("WEBRip-1080p = %+v, want the set's 15/995/1000", ov)
	}
	if ov := converted.QualitySizeOverrides["WEBDL-1080p"]; ov.Min != 1 || ov.Preferred != 2 || ov.Max != 3 {
		t.Errorf("WEBDL-1080p = %+v, want the rule's own 1/2/3 to win over the set", ov)
	}
	if untouched.QualitySizeSource != "guide:abc" || untouched.QualitySizeOverrides != nil {
		t.Errorf("a rule on another source was touched: %+v", untouched)
	}
}

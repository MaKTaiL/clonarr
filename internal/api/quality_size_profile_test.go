package api

import (
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

	rows := buildQSProfileRows(items, sizes, overrides)
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

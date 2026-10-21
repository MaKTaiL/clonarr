package core

import (
	"clonarr/internal/arr"
	"testing"
)

func TestFlattenGroups(t *testing.T) {
	items := []arr.ArrQualityItem{
		arrQ(1, "Flat1", true),
		arrG(100, "Group1", true,
			arrQ(2, "Member1", true),
			arrQ(3, "Member2", true),
		),
		arrQ(4, "Flat2", false),
	}

	groups := flattenGroups(items)

	if groups["Flat1"] != "" {
		t.Errorf("expected Flat1 to have no group, got %q", groups["Flat1"])
	}
	if groups["Flat2"] != "" {
		t.Errorf("expected Flat2 to have no group, got %q", groups["Flat2"])
	}
	if groups["Member1"] != "Group1" {
		t.Errorf("expected Member1 to be in Group1, got %q", groups["Member1"])
	}
	if groups["Member2"] != "Group1" {
		t.Errorf("expected Member2 to be in Group1, got %q", groups["Member2"])
	}
}

func TestDiffArrProfile_StructureChanges(t *testing.T) {
	current := &arr.ArrQualityProfile{
		Items: []arr.ArrQualityItem{
			{Name: "A", Allowed: true},
			{Name: "B", Allowed: true},
			{Name: "Group", Allowed: true, Items: []arr.ArrQualityItem{{Name: "C", Allowed: true}}},
		},
	}
	// Target has C ungrouped, and B and A swapped
	target := &arr.ArrQualityProfile{
		Items: []arr.ArrQualityItem{
			{Name: "B", Allowed: true},
			{Name: "A", Allowed: true},
			{Name: "C", Allowed: true},
		},
	}
	// diffArrProfile(current, target, cfs, rule, priorAvailableGroups, scoreOverrides, extras, ad)
	details := diffArrProfile(current, target, nil, &AutoSyncRule{}, map[string]bool{}, map[string]int{}, nil, nil)

	groupDriftFound := false
	orderDriftFound := false

	for _, d := range details {
		if d.Field == "group" && d.CFName == "C" && d.Current == "Group" && d.Target == "Ungrouped" {
			groupDriftFound = true
		}
	}

	if !groupDriftFound {
		t.Error("expected group drift for C")
	}

	// Target2 has order AND group changes (B and A swapped, C inside Group instead of Ungrouped)
	target2 := &arr.ArrQualityProfile{
		Items: []arr.ArrQualityItem{
			{Name: "B", Allowed: true},
			{Name: "A", Allowed: true},
			{Name: "Group", Allowed: true, Items: []arr.ArrQualityItem{}},
			{Name: "C", Allowed: true},
		},
	}

	details2 := diffArrProfile(current, target2, nil, &AutoSyncRule{}, map[string]bool{}, map[string]int{}, nil, nil)

	groupDriftFound = false
	orderDriftFound = false
	t.Logf("details2: %+v", details2)
	for _, d := range details2 {
		if d.Field == "group" && d.CFName == "C" && d.Current == "Group" && d.Target == "Ungrouped" {
			groupDriftFound = true
		}
		if d.Field == "quality_order" && d.CFName == "structure" {
			orderDriftFound = true
		}
	}

	if !groupDriftFound {
		t.Error("expected group drift for C in Target2")
	}
	if !orderDriftFound {
		t.Error("expected order drift in Target2")
	}
}

// A size changed by hand in the Arr was never noticed: the drift pass is the
// only one that looks at the Arr side, and it compared everything about a
// quality item except its three size fields.
func TestDiffArrProfileDetectsSizeDrift(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	current := &arr.ArrQualityProfile{
		Items: []arr.ArrQualityItem{
			{Name: "WEBDL-1080p", Allowed: true, MinSize: f(4), PreferredSize: f(95), MaxSize: f(130)},
			{Name: "Bluray-1080p", Allowed: true, MinSize: f(50), PreferredSize: f(995), MaxSize: f(1000)},
			{Name: "SDTV", Allowed: true, MinSize: f(2), PreferredSize: f(95), MaxSize: f(100)},
		},
	}
	target := &arr.ArrQualityProfile{
		Items: []arr.ArrQualityItem{
			// The rule wants the guide's numbers here: drift.
			{Name: "WEBDL-1080p", Allowed: true, MinSize: f(15), PreferredSize: f(995), MaxSize: f(1000)},
			// Already correct.
			{Name: "Bluray-1080p", Allowed: true, MinSize: f(50), PreferredSize: f(995), MaxSize: f(1000)},
			// Not covered by the chosen set, so nil means "say nothing" and
			// the sync leaves the instance alone. Reporting this as drift
			// would flag every uncovered quality on every check forever.
			{Name: "SDTV", Allowed: true},
		},
	}
	details := diffArrProfile(current, target, nil, &AutoSyncRule{}, map[string]bool{}, map[string]int{}, nil, nil)

	var sizes []DriftDetail
	for _, d := range details {
		if d.Field == "quality_size" {
			sizes = append(sizes, d)
		}
	}
	if len(sizes) != 1 {
		t.Fatalf("want one size drift, got %d: %+v", len(sizes), sizes)
	}
	if sizes[0].CFName != "WEBDL-1080p" {
		t.Errorf("CFName = %q, want WEBDL-1080p", sizes[0].CFName)
	}
	if sizes[0].Current != "4/95/130" || sizes[0].Target != "15/995/1000" {
		t.Errorf("got %q → %q, want 4/95/130 → 15/995/1000", sizes[0].Current, sizes[0].Target)
	}

	// It has to survive into something the UI can show, and count as a size
	// rather than be folded in with the quality items.
	pc := driftDetailToPendingChange(sizes[0], "2026-10-21T00:00:00Z")
	if pc.ChangeType != "qs-modified" || pc.AffectedID != "quality_size:WEBDL-1080p" {
		t.Errorf("pending change = %+v", pc)
	}
	summary := summariseDrift(sizes)
	if len(summary) != 1 || summary[0] != "1 quality size differs" {
		t.Errorf("summary = %v, want [1 quality size differs]", summary)
	}
}

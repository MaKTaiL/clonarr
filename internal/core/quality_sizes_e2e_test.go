package core

import (
	"clonarr/internal/arr"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wipe this guards against happened on the path from a guide structure,
// through resolveQualityItems, to the profile PUT. The tests in internal/arr
// hand-write the request body, so none of them covers that path: if
// resolveQualityItems stopped setting Quality, or named its group rows
// differently, they would all still pass and the bug would be back.
//
// This builds the body the way the sync engine does and checks every size the
// instance had is still there when the request lands.
func TestResolvedItemsKeepInstanceQualitySizes(t *testing.T) {
	size := func(v float64) *float64 { return &v }

	// The instance: sizes on a plain quality, a group member, and the row with
	// quality id 0, which is the one an id-based guard drops.
	instance := arr.ArrQualityProfile{
		ID:   7,
		Name: "WEB-1080p",
		Items: []arr.ArrQualityItem{
			{Quality: &arr.ArrQualityRef{ID: 0, Name: "Unknown"}, MinSize: size(1), MaxSize: size(199.9)},
			{Quality: &arr.ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}, Allowed: true, MinSize: size(4), MaxSize: size(130), PreferredSize: size(95)},
			{ID: 1000, Name: "WEB 720p", Allowed: true, Items: []arr.ArrQualityItem{
				{Quality: &arr.ArrQualityRef{ID: 5, Name: "WEBRip-720p"}, Allowed: true, MinSize: size(3), MaxSize: size(125)},
			}},
		},
	}

	defs := []arr.ArrQualityDefinition{
		{ID: 1, Quality: arr.ArrQualityRef{ID: 0, Name: "Unknown"}, Title: "Unknown"},
		{ID: 2, Quality: arr.ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}, Title: "WEBDL-1080p"},
		{ID: 3, Quality: arr.ArrQualityRef{ID: 5, Name: "WEBRip-720p"}, Title: "WEBRip-720p"},
	}
	byName := map[string]*arr.ArrQualityDefinition{}
	for i := range defs {
		byName[defs[i].Quality.Name] = &defs[i]
	}

	// The guide's structure for this profile.
	trashItems := []QualityItem{
		{Name: "WEBDL-1080p", Allowed: true},
		{Name: "WEB 720p", Allowed: true, Items: []string{"WEBRip-720p"}},
		{Name: "Unknown", Allowed: false},
	}

	built, err := resolveQualityItems(trashItems, byName)
	if err != nil {
		t.Fatalf("resolveQualityItems: %v", err)
	}
	// Nothing in the built body carries a size: that is the whole problem.
	var hasSize func([]arr.ArrQualityItem) bool
	hasSize = func(its []arr.ArrQualityItem) bool {
		for _, it := range its {
			if it.MinSize != nil || it.MaxSize != nil || it.PreferredSize != nil {
				return true
			}
			if hasSize(it.Items) {
				return true
			}
		}
		return false
	}
	if hasSize(built) {
		t.Fatal("fixture is wrong: the built items already carry sizes")
	}

	var sent arr.ArrQualityProfile
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(instance)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Errorf("PUT body: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	outgoing := instance
	outgoing.Items = built
	client := arr.NewArrClient(srv.URL, "key", "", "", srv.Client())
	if err := client.UpdateProfile(&outgoing); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	got := map[string][3]*float64{}
	var walk func([]arr.ArrQualityItem)
	walk = func(its []arr.ArrQualityItem) {
		for _, it := range its {
			name := it.Name
			if it.Quality != nil {
				name = it.Quality.Name
			}
			if it.MinSize != nil || it.MaxSize != nil || it.PreferredSize != nil {
				got[name] = [3]*float64{it.MinSize, it.MaxSize, it.PreferredSize}
			}
			walk(it.Items)
		}
	}
	walk(sent.Items)

	for name, want := range map[string][2]float64{
		"Unknown":     {1, 199.9},
		"WEBDL-1080p": {4, 130},
		"WEBRip-720p": {3, 125},
	} {
		s, ok := got[name]
		if !ok {
			t.Errorf("%s lost its sizes on the way through the body builder", name)
			continue
		}
		if s[0] == nil || *s[0] != want[0] {
			t.Errorf("%s minSize = %v, want %v", name, s[0], want[0])
		}
		if s[1] == nil || *s[1] != want[1] {
			t.Errorf("%s maxSize = %v, want %v", name, s[1], want[1])
		}
	}
}

// TestCloneQualityItemsKeepsSizes locks in that the config deep copy carries
// per-profile quality sizes. Config.Get() clones every rule's quality
// structure, so a field missed here is silently dropped on every read.
func TestCloneQualityItemsKeepsSizes(t *testing.T) {
	min, max, pref := 2.0, 100.0, 95.0
	src := []QualityItem{{
		Name:          "WEB 1080p",
		Allowed:       true,
		Items:         []string{"WEBDL-1080p", "WEBRip-1080p"},
		MinSize:       &min,
		MaxSize:       &max,
		PreferredSize: &pref,
	}}

	out := cloneQualityItems(src)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	got := out[0]
	if got.MinSize == nil || got.MaxSize == nil || got.PreferredSize == nil {
		t.Fatalf("sizes dropped by the deep copy: %+v", got)
	}
	if *got.MinSize != min || *got.MaxSize != max || *got.PreferredSize != pref {
		t.Errorf("sizes changed: got %v/%v/%v want %v/%v/%v",
			*got.MinSize, *got.MaxSize, *got.PreferredSize, min, max, pref)
	}
	if got.MinSize == src[0].MinSize {
		t.Error("deep copy shares the pointer with the original")
	}

	// Mutating the copy must not reach the original.
	*out[0].MinSize = 7
	if *src[0].MinSize != min {
		t.Errorf("original mutated through the copy: %v", *src[0].MinSize)
	}
}

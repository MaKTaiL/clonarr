package core

import (
	"net/http"
	"os"
	"testing"
	"time"

	"clonarr/internal/arr"
)

// Live end-to-end checks for per-profile quality sizes. Every test here skips
// unless its instance credentials are in the environment, so an ordinary
// `go test ./...` and CI run them as no-ops. Enabled, they talk to real Arr
// containers and TestLiveV5WriteRoundTrip writes to a quality profile and
// restores it, so point them only at throwaway instances.
//
//	V5_URL/V5_KEY   a Sonarr that keeps sizes per profile (v5 and later)
//	S4_URL/S4_KEY   a Sonarr that keeps them in the shared list (v4)
//	R_URL/R_KEY     a Radarr
//
// The command, and the reason for the --network flag, is in
// docs/clonarr/v5-quality-size-test-plan.md.
func TestLiveV5PerProfileSizes(t *testing.T) {
	url, key := os.Getenv("V5_URL"), os.Getenv("V5_KEY")
	if url == "" || key == "" {
		t.Skip("V5_URL/V5_KEY not set")
	}
	inst := Instance{ID: "live", Type: "sonarr", URL: url, APIKey: key}
	client := NewArrClientFor(inst, &http.Client{Timeout: 15 * time.Second})

	defs, err := client.ListQualityDefinitions()
	if err != nil {
		t.Fatalf("list definitions: %v", err)
	}
	t.Logf("definition rows: %d, carry sizes: %v", len(defs), arr.QualityDefinitionsCarrySizes(defs))
	if arr.QualityDefinitionsCarrySizes(defs) {
		t.Fatal("expected a per-profile instance")
	}

	// The gate says yes, so the resolver runs.
	guide := []*TrashQualitySize{{
		TrashID: "bef99584217af744e404ed44a33af589",
		Type:    "series",
		Qualities: []QualitySizeEntry{
			{Quality: "WEBDL-1080p", Min: 15, Preferred: 995, Max: 1000},
			{Quality: "Bluray-720p", Min: 17.1, Preferred: 995, Max: 1000},
		},
	}}
	ad := &AppData{QualitySizes: guide}
	req := SyncRequest{QualitySizeSource: QSSourceGuide}
	sizes := profileQualitySizes(req, ad, inst, defs)
	if sizes == nil {
		t.Fatal("resolver returned nothing on a per-profile instance")
	}
	t.Logf("resolved %d qualities", len(sizes))

	// Build the body the sync would send for profile 4 and write it.
	before, err := client.GetProfile(4)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	t.Logf("profile %q: %d top-level items", before.Name, len(before.Items))

	byName := map[string]*arr.ArrQualityDefinition{}
	for i := range defs {
		byName[defs[i].Quality.Name] = &defs[i]
	}
	items := []QualityItem{
		{Name: "WEBDL-1080p", Allowed: true},
		{Name: "WEB 720p", Allowed: true, Items: []string{"WEBDL-720p", "WEBRip-720p"}},
		{Name: "Bluray-720p", Allowed: false},
	}
	built, err := resolveQualityItems(items, byName, sizes)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, it := range built {
		name := it.Name
		if it.Quality != nil {
			name = it.Quality.Name
		}
		t.Logf("built %-14s min=%v pref=%v max=%v members=%d",
			name, fmtp(it.MinSize), fmtp(it.PreferredSize), fmtp(it.MaxSize), len(it.Items))
		for _, m := range it.Items {
			t.Logf("   member %-14s min=%v pref=%v max=%v", m.Quality.Name, fmtp(m.MinSize), fmtp(m.PreferredSize), fmtp(m.MaxSize))
		}
	}
}

func fmtp(f *float64) any {
	if f == nil {
		return "nil"
	}
	return *f
}

// Proves the whole write path: our resolved sizes survive UpdateProfile's
// read-modify-write round trip (preserveQualitySizes fills only nil fields),
// and Sonarr v5 stores 995 and 1000 verbatim rather than treating them as the
// Unlimited sentinel. Restores the profile afterwards.
func TestLiveV5WriteRoundTrip(t *testing.T) {
	url, key := os.Getenv("V5_URL"), os.Getenv("V5_KEY")
	if url == "" || key == "" {
		t.Skip("V5_URL/V5_KEY not set")
	}
	inst := Instance{ID: "live", Type: "sonarr", URL: url, APIKey: key}
	client := NewArrClientFor(inst, &http.Client{Timeout: 15 * time.Second})

	const profileID = 4
	// Two separate reads, not one struct copy. `next := *before` copies the
	// struct but shares the Items backing array, so stamping next would
	// mutate the snapshot meant for the restore and put the modified values
	// back. That happened on the first run of this test and left the
	// container's profile 4 changed; the same slice-aliasing trap the
	// vpn-gateway credential-masking fix turned on.
	original, err := client.GetProfile(profileID)
	if err != nil {
		t.Fatalf("get profile for the restore snapshot: %v", err)
	}
	before, err := client.GetProfile(profileID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}

	// A value nobody would pick by accident, so a false pass is impossible.
	target := 123.4
	next := *before
	var touched string
	var stamp func(items []arr.ArrQualityItem)
	stamp = func(items []arr.ArrQualityItem) {
		for i := range items {
			if len(items[i].Items) > 0 {
				stamp(items[i].Items)
				continue
			}
			if items[i].Quality != nil && items[i].Quality.Name == "WEBDL-1080p" {
				min, pref, max := target, 995.0, 1000.0
				items[i].MinSize, items[i].PreferredSize, items[i].MaxSize = &min, &pref, &max
				touched = items[i].Quality.Name
			}
		}
	}
	stamp(next.Items)
	if touched == "" {
		t.Fatal("WEBDL-1080p not found in the profile")
	}

	if err := client.UpdateProfile(&next); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	after, err := client.GetProfile(profileID)
	if err != nil {
		t.Fatalf("re-read profile: %v", err)
	}

	var check func(items []arr.ArrQualityItem) bool
	check = func(items []arr.ArrQualityItem) bool {
		for _, it := range items {
			if len(it.Items) > 0 {
				if check(it.Items) {
					return true
				}
				continue
			}
			if it.Quality != nil && it.Quality.Name == "WEBDL-1080p" {
				if it.MinSize == nil || *it.MinSize != target {
					t.Errorf("min: want %v, got %v", target, fmtp(it.MinSize))
				}
				if it.PreferredSize == nil || *it.PreferredSize != 995 {
					t.Errorf("preferred: want 995 stored verbatim, got %v", fmtp(it.PreferredSize))
				}
				if it.MaxSize == nil || *it.MaxSize != 1000 {
					t.Errorf("max: want 1000 stored verbatim, got %v", fmtp(it.MaxSize))
				}
				t.Logf("stored: min=%v pref=%v max=%v", fmtp(it.MinSize), fmtp(it.PreferredSize), fmtp(it.MaxSize))
				return true
			}
		}
		return false
	}
	if !check(after.Items) {
		t.Error("WEBDL-1080p missing after the write")
	}

	// Another profile must be untouched: sizes are per profile on v5, and a
	// write that leaked to its neighbours would be the worst outcome here.
	other, err := client.GetProfile(3)
	if err == nil {
		var leaked bool
		var scan func(items []arr.ArrQualityItem)
		scan = func(items []arr.ArrQualityItem) {
			for _, it := range items {
				if len(it.Items) > 0 {
					scan(it.Items)
					continue
				}
				if it.Quality != nil && it.Quality.Name == "WEBDL-1080p" && it.MinSize != nil && *it.MinSize == target {
					leaked = true
				}
			}
		}
		scan(other.Items)
		if leaked {
			t.Errorf("the write reached profile %d as well", other.ID)
		} else {
			t.Logf("profile %q untouched, as expected", other.Name)
		}
	}

	if err := client.UpdateProfile(original); err != nil {
		t.Errorf("restore failed, sonarr-v5-test profile %d is left modified: %v", profileID, err)
	} else {
		t.Log("profile restored")
	}
}

// The non-regression side, live: on the versions that keep sizes in the
// shared list the gate must answer nil, so a profile write carries no size
// fields at all and behaves exactly as it did before this feature.
func TestLiveOlderVersionsGetNoSizes(t *testing.T) {
	for _, c := range []struct{ envURL, envKey, app string }{
		{"S4_URL", "S4_KEY", "sonarr"},
		{"R_URL", "R_KEY", "radarr"},
	} {
		url, key := os.Getenv(c.envURL), os.Getenv(c.envKey)
		if url == "" || key == "" {
			t.Logf("%s: skipped, no credentials", c.app)
			continue
		}
		inst := Instance{ID: "live", Type: c.app, URL: url, APIKey: key}
		client := NewArrClientFor(inst, &http.Client{Timeout: 15 * time.Second})
		defs, err := client.ListQualityDefinitions()
		if err != nil {
			t.Errorf("%s: list definitions: %v", c.app, err)
			continue
		}
		carries := arr.QualityDefinitionsCarrySizes(defs)
		t.Logf("%s: %d definition rows, shared list carries sizes: %v", c.app, len(defs), carries)
		if !carries {
			t.Errorf("%s: expected the shared list to carry sizes on this version", c.app)
		}
		// Even with a source set and a guide set available, the gate closes.
		ad := &AppData{QualitySizes: []*TrashQualitySize{{
			TrashID:   "bef99584217af744e404ed44a33af589",
			Type:      "series",
			Qualities: []QualitySizeEntry{{Quality: "WEBDL-1080p", Min: 15, Preferred: 995, Max: 1000}},
		}}}
		if got := profileQualitySizes(SyncRequest{QualitySizeSource: QSSourceGuide}, ad, inst, defs); got != nil {
			t.Errorf("%s: want nil sizes on a shared-list instance, got %v", c.app, got)
		}
	}
}

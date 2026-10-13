package arr

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sonarr v5 keeps quality sizes on each profile's items and fills them in for
// every quality in the profile. Our profile body is built from the guide's
// structure and carries none, and the API reads an absent field as "clear it",
// so a plain PUT wipes the whole size table. Measured against Sonarr
// 5.0.0.2569: 22 of 22 qualities lost their sizes on one sync, HTTP 202, no
// error reported. These tests pin the round trip that prevents it.

func f(v float64) *float64 { return &v }

// capture holds what the fake instance received.
type capture struct {
	profile ArrQualityProfile
	raw     string
}

// newProfileServer serves `current` on GET and records the PUT body.
func newProfileServer(t *testing.T, current ArrQualityProfile) (*httptest.Server, *capture) {
	t.Helper()
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			got.raw = string(body)
			if err := json.Unmarshal(body, &got.profile); err != nil {
				t.Errorf("PUT body is not valid JSON: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// itemSizes flattens every item that carries a size, keyed by quality name or
// group name, so assertions read clearly.
func itemSizes(items []ArrQualityItem) map[string][3]*float64 {
	out := map[string][3]*float64{}
	var walk func([]ArrQualityItem)
	walk = func(its []ArrQualityItem) {
		for _, it := range its {
			name := it.Name
			if it.Quality != nil && it.Quality.Name != "" {
				name = it.Quality.Name
			}
			if it.MinSize != nil || it.MaxSize != nil || it.PreferredSize != nil {
				out[name] = [3]*float64{it.MinSize, it.MaxSize, it.PreferredSize}
			}
			walk(it.Items)
		}
	}
	walk(items)
	return out
}

// v5Profile is a profile as Sonarr v5 returns it: sizes on a plain quality and
// on a group member, which is where the wipe showed up in practice.
func v5Profile() ArrQualityProfile {
	return ArrQualityProfile{
		ID:   7,
		Name: "WEB-1080p",
		Items: []ArrQualityItem{
			// Unknown is quality ID 0. It belongs in the fixture: a guard that
			// reads 0 as "no quality" drops exactly this row.
			{Quality: &ArrQualityRef{ID: 0, Name: "Unknown"}, Allowed: false,
				MinSize: f(1), MaxSize: f(199.9), PreferredSize: f(95)},
			{Quality: &ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}, Allowed: true,
				MinSize: f(4), MaxSize: f(130), PreferredSize: f(95)},
			{ID: 1000, Name: "WEB 720p", Allowed: true, Items: []ArrQualityItem{
				{Quality: &ArrQualityRef{ID: 5, Name: "WEBRip-720p"}, Allowed: true,
					MinSize: f(3), MaxSize: f(125), PreferredSize: f(90)},
			}},
		},
	}
}

// syncBody mirrors what the sync engine builds: the guide's structure, fresh
// items, no sizes anywhere.
func syncBody() *ArrQualityProfile {
	return &ArrQualityProfile{
		ID:   7,
		Name: "WEB-1080p",
		Items: []ArrQualityItem{
			{Quality: &ArrQualityRef{ID: 0, Name: "Unknown"}, Allowed: false, Items: []ArrQualityItem{}},
			{Quality: &ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}, Allowed: true, Items: []ArrQualityItem{}},
			{ID: 1000, Name: "WEB 720p", Allowed: true, Items: []ArrQualityItem{
				{Quality: &ArrQualityRef{ID: 5, Name: "WEBRip-720p"}, Allowed: true, Items: []ArrQualityItem{}},
			}},
		},
	}
}

func TestUpdateProfilePreservesQualitySizes(t *testing.T) {
	srv, got := newProfileServer(t, v5Profile())
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	if err := c.UpdateProfile(syncBody()); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	sizes := itemSizes(got.profile.Items)
	if len(sizes) != 3 {
		t.Fatalf("expected sizes on 3 items, got %d: %v", len(sizes), sizes)
	}
	for name, want := range map[string][3]float64{
		"Unknown":     {1, 199.9, 95},
		"WEBDL-1080p": {4, 130, 95},
		"WEBRip-720p": {3, 125, 90},
	} {
		s, ok := sizes[name]
		if !ok {
			t.Errorf("%s lost its sizes", name)
			continue
		}
		for i, w := range want {
			if s[i] == nil || *s[i] != w {
				t.Errorf("%s size[%d] = %v, want %v", name, i, s[i], w)
			}
		}
	}
}

// Radarr and Sonarr v4 have no size fields. Nothing to copy, and the body must
// not grow keys those versions do not understand.
func TestUpdateProfileOmitsSizesWhenSourceHasNone(t *testing.T) {
	current := v5Profile()
	for i := range current.Items {
		current.Items[i].MinSize, current.Items[i].MaxSize, current.Items[i].PreferredSize = nil, nil, nil
		for j := range current.Items[i].Items {
			current.Items[i].Items[j].MinSize = nil
			current.Items[i].Items[j].MaxSize = nil
			current.Items[i].Items[j].PreferredSize = nil
		}
	}
	srv, got := newProfileServer(t, current)
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	if err := c.UpdateProfile(syncBody()); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	for _, key := range []string{"minSize", "maxSize", "preferredSize"} {
		if strings.Contains(got.raw, key) {
			t.Errorf("body carries %q although the instance has no sizes", key)
		}
	}
}

// A size set deliberately by the caller wins; only empty fields get filled.
func TestUpdateProfileKeepsExplicitSizes(t *testing.T) {
	srv, got := newProfileServer(t, v5Profile())
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	body.Items[1].MinSize = f(42)

	if err := c.UpdateProfile(body); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	s := itemSizes(got.profile.Items)["WEBDL-1080p"]
	if s[0] == nil || *s[0] != 42 {
		t.Errorf("explicit minSize = %v, want 42", s[0])
	}
	// the untouched fields still come from the instance
	if s[1] == nil || *s[1] != 130 {
		t.Errorf("maxSize = %v, want 130 carried over", s[1])
	}
}

// Losing the read must stop the update: sending the body regardless is what
// wipes the sizes.
func TestUpdateProfileFailsWhenCurrentProfileUnreadable(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	if err := c.UpdateProfile(syncBody()); err == nil {
		t.Error("expected an error when the profile cannot be read")
	}
	if putCalled {
		t.Error("PUT was sent even though the current profile could not be read")
	}
}

// With nothing to copy, the caller's own values must survive. The old code
// stripped them here, which would have made every profile the earlier wiping
// bug emptied impossible to repair: a profile with no sizes looks exactly like
// a version that has no size fields.
func TestUpdateProfileKeepsCallerSizesWhenInstanceHasNone(t *testing.T) {
	current := v5Profile()
	var clear func([]ArrQualityItem)
	clear = func(its []ArrQualityItem) {
		for i := range its {
			its[i].MinSize, its[i].MaxSize, its[i].PreferredSize = nil, nil, nil
			clear(its[i].Items)
		}
	}
	clear(current.Items)

	srv, got := newProfileServer(t, current)
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	body.Items[1].MinSize = f(4)

	if err := c.UpdateProfile(body); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	s := itemSizes(got.profile.Items)["WEBDL-1080p"]
	if s[0] == nil || *s[0] != 4 {
		t.Errorf("caller minSize = %v, want 4 kept", s[0])
	}
}

// A size of zero is a real value, not "unset". The whole fix rests on nil
// checks rather than truthiness: a refactor to `*it.MinSize > 0` would read a
// zero as absent and reintroduce the wipe.
func TestUpdateProfileTreatsZeroAsPresent(t *testing.T) {
	current := v5Profile()
	current.Items[0].MinSize, current.Items[0].MaxSize, current.Items[0].PreferredSize = f(0), f(0), f(0)

	srv, got := newProfileServer(t, current)
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	if err := c.UpdateProfile(syncBody()); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	s, ok := itemSizes(got.profile.Items)["Unknown"]
	if !ok {
		t.Fatal("a zero size was dropped from the body")
	}
	for i := range s {
		if s[i] == nil || *s[i] != 0 {
			t.Errorf("size[%d] = %v, want 0", i, s[i])
		}
	}
}

// Everything that is not a size must come back unchanged: the preservation
// pass walks the same items the body is built from.
func TestUpdateProfileChangesNothingButSizes(t *testing.T) {
	srv, got := newProfileServer(t, v5Profile())
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	if err := c.UpdateProfile(body); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	sent := got.profile
	if sent.Name != body.Name || sent.ID != body.ID {
		t.Errorf("identity changed: %q/%d", sent.Name, sent.ID)
	}
	if len(sent.Items) != len(body.Items) {
		t.Fatalf("item count %d, want %d", len(sent.Items), len(body.Items))
	}
	for i := range body.Items {
		a, b := body.Items[i], sent.Items[i]
		if a.Allowed != b.Allowed || a.Name != b.Name || a.ID != b.ID {
			t.Errorf("item %d changed: %+v vs %+v", i, a, b)
		}
		if (a.Quality == nil) != (b.Quality == nil) {
			t.Errorf("item %d quality presence changed", i)
			continue
		}
		if a.Quality != nil && a.Quality.ID != b.Quality.ID {
			t.Errorf("item %d quality id %d, want %d", i, b.Quality.ID, a.Quality.ID)
		}
		if len(a.Items) != len(b.Items) {
			t.Errorf("item %d member count %d, want %d", i, len(b.Items), len(a.Items))
		}
	}
}

// A quality in the body that the instance's profile does not have gets no
// size, and must not fall back to matching on a name.
func TestUpdateProfileLeavesUnknownQualityAlone(t *testing.T) {
	srv, got := newProfileServer(t, v5Profile())
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	body.Items = append(body.Items, ArrQualityItem{
		Quality: &ArrQualityRef{ID: 99, Name: "Bluray-2160p"}, Allowed: true, Items: []ArrQualityItem{},
	})
	if err := c.UpdateProfile(body); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if _, ok := itemSizes(got.profile.Items)["Bluray-2160p"]; ok {
		t.Error("a quality absent from the instance was given a size")
	}
}

// Creating a profile takes its sizes from the app's own schema. Without this a
// profile created on a version that keeps sizes per profile is born with no
// limits at all, because there is no shared list left to fall back on.
func TestCreateProfileFillsSizesFromSchema(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schema") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v5Profile())
			return
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			raw = string(body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":12,"name":"WEB-1080p"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	body.ID = 0 // create
	if _, err := c.CreateProfile(body); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	var sent ArrQualityProfile
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatalf("POST body: %v", err)
	}
	sizes := itemSizes(sent.Items)
	for _, name := range []string{"Unknown", "WEBDL-1080p", "WEBRip-720p"} {
		if _, ok := sizes[name]; !ok {
			t.Errorf("%s was created without a size", name)
		}
	}
}

// A schema that carries no sizes (Radarr, Sonarr v4) must leave the body alone.
func TestCreateProfileOmitsSizesWhenSchemaHasNone(t *testing.T) {
	schema := v5Profile()
	var clear func([]ArrQualityItem)
	clear = func(its []ArrQualityItem) {
		for i := range its {
			its[i].MinSize, its[i].MaxSize, its[i].PreferredSize = nil, nil, nil
			clear(its[i].Items)
		}
	}
	clear(schema.Items)

	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schema") {
			_ = json.NewEncoder(w).Encode(schema)
			return
		}
		body, _ := io.ReadAll(r.Body)
		raw = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":12}`))
	}))
	defer srv.Close()
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	body := syncBody()
	body.ID = 0
	if _, err := c.CreateProfile(body); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	for _, key := range []string{"minSize", "maxSize", "preferredSize"} {
		if strings.Contains(raw, key) {
			t.Errorf("create body carries %q although the schema has none", key)
		}
	}
}

// The three fields have to survive the list response too: six PUT paths hand
// back a profile they got from ListProfiles without rebuilding its items, and
// they depend on the sizes being in there.
func TestListProfilesCarriesQualitySizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]ArrQualityProfile{v5Profile()})
	}))
	defer srv.Close()
	c := NewArrClient(srv.URL, "key", "", "", srv.Client())

	profiles, err := c.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	sizes := itemSizes(profiles[0].Items)
	if len(sizes) != 3 {
		t.Fatalf("sizes survived on %d items, want 3: %v", len(sizes), sizes)
	}
}

// QualityDefinitionsCarrySizes is what every "where do sizes live" decision
// reads, so pin the two shapes the apps actually return.
func TestQualityDefinitionsCarrySizes(t *testing.T) {
	withSizes := []ArrQualityDefinition{
		{ID: 1, Quality: ArrQualityRef{ID: 0, Name: "Unknown"}, MinSize: f(1), MaxSize: f(199.9), PreferredSize: f(95)},
		{ID: 2, Quality: ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}},
	}
	if !QualityDefinitionsCarrySizes(withSizes) {
		t.Error("a list with sizes must report true")
	}
	without := []ArrQualityDefinition{
		{ID: 1, Quality: ArrQualityRef{ID: 0, Name: "Unknown"}, Title: "Unknown"},
		{ID: 2, Quality: ArrQualityRef{ID: 3, Name: "WEBDL-1080p"}, Title: "WEBDL-1080p"},
	}
	if QualityDefinitionsCarrySizes(without) {
		t.Error("a list without any size must report false")
	}
	if QualityDefinitionsCarrySizes(nil) {
		t.Error("an empty list must report false, so nothing is written to it")
	}
	// A zero is a value, not an absence.
	zero := []ArrQualityDefinition{{ID: 1, MinSize: f(0)}}
	if !QualityDefinitionsCarrySizes(zero) {
		t.Error("a zero size must count as present")
	}
}

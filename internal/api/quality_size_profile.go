package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"clonarr/internal/arr"
	"clonarr/internal/core"
)

// Per-profile quality sizes: the endpoints behind the profile editor's Quality
// size tab, plus CRUD for the size sets a user saves.
//
// Sonarr v5 moved quality sizes off the shared /qualitydefinition list and onto
// each quality profile. On those versions the instance-wide tab under Media
// Management has nothing to write, so the choice lives per profile instead.
// Everything here gates on the shape of the instance's definition list rather
// than on a version string or an app name.

// qsProfileRow is one quality as the tab shows it.
type qsProfileRow struct {
	Quality string `json:"quality"`
	// Group is the quality group this quality sits in, empty when it stands
	// on its own. Sizes belong to individual qualities, so a group is shown
	// for context and never as a source.
	Group   string `json:"group,omitempty"`
	Allowed bool   `json:"allowed"`
	// Current is what the instance has on this profile right now. Nil fields
	// mean the instance has no value, which its own UI shows as Unlimited.
	Current qsTriple `json:"current"`
	// Resolved is what clonarr would write on the next sync, and Source says
	// where that number came from so the user knows which lever to pull. The
	// attribution is computed here rather than inferred by the frontend.
	Resolved qsTriple `json:"resolved"`
	Source   string   `json:"source"` // "override", "set", or "" for nothing written
}

type qsTriple struct {
	Min       *float64 `json:"min"`
	Preferred *float64 `json:"preferred"`
	Max       *float64 `json:"max"`
}

// handleRuleQualitySizes returns everything the Quality size tab needs in one
// call: whether this instance keeps sizes per profile at all, the rule's
// current choice, the sets available to pick from, and a row per quality.
func (s *Server) handleRuleQualitySizes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	cfg := s.Core.Config.Get()
	var rule *core.AutoSyncRule
	for i := range cfg.AutoSync.Rules {
		if cfg.AutoSync.Rules[i].ID == id {
			rule = &cfg.AutoSync.Rules[i]
			break
		}
	}
	if rule == nil {
		writeError(w, 404, "Rule not found")
		return
	}
	inst, ok := s.Core.Config.GetInstance(rule.InstanceID)
	if !ok {
		writeError(w, 404, "Instance not found")
		return
	}

	client := core.NewArrClientFor(inst, s.Core.HTTPClient)
	defs, err := client.ListQualityDefinitions()
	if err != nil {
		writeError(w, 502, "Failed to read quality sizes: "+err.Error())
		return
	}
	// An empty list is not an answer. Reporting "per profile" on a failed read
	// would offer the user a tab that cannot write anything.
	perProfile := len(defs) > 0 && !arr.QualityDefinitionsCarrySizes(defs)

	resp := map[string]any{
		"perProfileSizes": perProfile,
		"source":          rule.QualitySizeSource,
		"overrides":       rule.QualitySizeOverrides,
		"sets":            s.qsAvailableSets(inst.Type),
		"defaultSetType":  core.DefaultQualitySizeSetType(inst.Type),
	}
	if !perProfile {
		// Nothing more to say: on these versions the sizes live in the shared
		// list and the instance-wide tab is the right place for them.
		resp["rows"] = []qsProfileRow{}
		writeJSON(w, resp)
		return
	}

	profile, err := client.GetProfile(rule.ArrProfileID)
	if err != nil {
		writeError(w, 502, "Failed to read the quality profile: "+err.Error())
		return
	}

	ad := s.Core.Trash.GetAppData(inst.Type)
	var guideSets []*core.TrashQualitySize
	if ad != nil {
		guideSets = ad.QualitySizes
	}
	sizes := core.ResolveQualitySizes(core.QualitySizeResolveInput{
		Source:    rule.QualitySizeSource,
		Overrides: rule.QualitySizeOverrides,
		AppType:   inst.Type,
		GuideSets: guideSets,
		UserSets:  cfg.QualitySizeSets,
	})

	resp["rows"] = buildQSProfileRows(profile.Items, sizes, rule.QualitySizeOverrides)
	writeJSON(w, resp)
}

// buildQSProfileRows flattens a profile's items into one row per quality.
//
// Group rows are not rows of their own: the app keeps sizes on individual
// qualities, so a group contributes its members and its name as context. A
// member inherits the group's allowed state, which is what the profile
// actually stores.
func buildQSProfileRows(items []arr.ArrQualityItem, sizes map[string]core.QualitySizeTriple, overrides map[string]core.QSOverride) []qsProfileRow {
	rows := []qsProfileRow{}
	var walk func(its []arr.ArrQualityItem, group string)
	walk = func(its []arr.ArrQualityItem, group string) {
		for _, it := range its {
			if len(it.Items) > 0 {
				name := it.Name
				if name == "" && it.Quality != nil {
					name = it.Quality.Name
				}
				walk(it.Items, name)
				continue
			}
			name := it.Name
			if name == "" && it.Quality != nil {
				name = it.Quality.Name
			}
			if name == "" {
				continue
			}
			row := qsProfileRow{
				Quality: name,
				Group:   group,
				Allowed: it.Allowed,
				Current: qsTriple{Min: it.MinSize, Preferred: it.PreferredSize, Max: it.MaxSize},
			}
			if t, ok := sizes[name]; ok {
				row.Resolved = qsTriple{Min: t.Min, Preferred: t.Preferred, Max: t.Max}
				if _, isOverride := overrides[name]; isOverride {
					row.Source = "override"
				} else {
					row.Source = "set"
				}
			}
			rows = append(rows, row)
		}
	}
	walk(items, "")
	// The profile stores items lowest-priority first; the tab reads better
	// highest first, which is the order both Arr UIs show.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// qsSetOption is one entry in the source picker: a guide set or a user set,
// described the same way so one list can hold both.
type qsSetOption struct {
	Value     string `json:"value"` // the stored source string
	Name      string `json:"name"`
	Origin    string `json:"origin"` // "guide" or "user"
	Qualities int    `json:"qualities"`
}

func (s *Server) qsAvailableSets(appType string) []qsSetOption {
	out := []qsSetOption{}
	if ad := s.Core.Trash.GetAppData(appType); ad != nil {
		for _, set := range ad.QualitySizes {
			if set == nil {
				continue
			}
			out = append(out, qsSetOption{
				Value:     core.QSSourceSetPfx + set.TrashID,
				Name:      set.Type,
				Origin:    "guide",
				Qualities: len(set.Qualities),
			})
		}
		// movie and series first, then the sqp variants, then anime: the same
		// order the instance-wide tab uses, so the two screens read alike.
		order := map[string]int{"movie": 0, "series": 0, "sqp-streaming": 1, "sqp-uhd": 2, "anime": 3}
		sort.SliceStable(out, func(i, j int) bool {
			oi, oj := 99, 99
			if v, ok := order[out[i].Name]; ok {
				oi = v
			}
			if v, ok := order[out[j].Name]; ok {
				oj = v
			}
			return oi < oj
		})
	}
	for _, set := range s.Core.Config.Get().QualitySizeSets {
		if set.AppType != appType {
			continue
		}
		out = append(out, qsSetOption{
			Value:     core.QSSourceCustomPfx + set.ID,
			Name:      set.Name,
			Origin:    "user",
			Qualities: len(set.Qualities),
		})
	}
	return out
}

// --- User sets ---

func (s *Server) handleListQualitySizeSets(w http.ResponseWriter, r *http.Request) {
	appType := r.URL.Query().Get("app")
	out := []core.QualitySizeSet{}
	for _, set := range s.Core.Config.Get().QualitySizeSets {
		if appType != "" && set.AppType != appType {
			continue
		}
		out = append(out, set)
	}
	writeJSON(w, out)
}

func (s *Server) handleSaveQualitySizeSet(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<18)
	var set core.QualitySizeSet
	if err := json.NewDecoder(r.Body).Decode(&set); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	set.Name = strings.TrimSpace(set.Name)
	if set.Name == "" {
		writeError(w, 400, "Give the set a name")
		return
	}
	if set.AppType != "radarr" && set.AppType != "sonarr" {
		writeError(w, 400, "A set belongs to either Radarr or Sonarr")
		return
	}
	// A set with no rows would resolve to nothing, which is indistinguishable
	// from off and would leave the user wondering why their choice does not
	// take. Refuse it instead.
	if len(set.Qualities) == 0 {
		writeError(w, 400, "A set needs at least one quality")
		return
	}

	var saveErr string
	if err := s.Core.Config.Update(func(cfg *core.Config) {
		// Names are what the picker shows, so a duplicate within one app
		// makes two entries that cannot be told apart.
		for _, existing := range cfg.QualitySizeSets {
			if existing.ID != set.ID && existing.AppType == set.AppType &&
				strings.EqualFold(existing.Name, set.Name) {
				saveErr = "A set with that name already exists for this app"
				return
			}
		}
		if set.ID == "" {
			set.ID = core.GenerateID()
			cfg.QualitySizeSets = append(cfg.QualitySizeSets, set)
			return
		}
		for i := range cfg.QualitySizeSets {
			if cfg.QualitySizeSets[i].ID == set.ID {
				cfg.QualitySizeSets[i] = set
				return
			}
		}
		cfg.QualitySizeSets = append(cfg.QualitySizeSets, set)
	}); err != nil {
		writeError(w, 500, "Failed to save the set")
		return
	}
	if saveErr != "" {
		writeError(w, 409, saveErr)
		return
	}
	writeJSON(w, set)
}

func (s *Server) handleDeleteQualitySizeSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// A set a rule points at cannot be deleted out from under it: the rule
	// would resolve to nothing and quietly stop writing sizes. Name the rules
	// so the user can go and change them.
	cfg := s.Core.Config.Get()
	inUse := []string{}
	for _, rule := range cfg.AutoSync.Rules {
		if rule.QualitySizeSource == core.QSSourceCustomPfx+id {
			name := rule.ID
			if inst, ok := s.Core.Config.GetInstance(rule.InstanceID); ok {
				name = inst.Name
			}
			inUse = append(inUse, name)
		}
	}
	if len(inUse) > 0 {
		writeError(w, 409, "This set is still used by a sync rule on "+strings.Join(inUse, ", "))
		return
	}

	if err := s.Core.Config.Update(func(cfg *core.Config) {
		out := cfg.QualitySizeSets[:0]
		for _, set := range cfg.QualitySizeSets {
			if set.ID != id {
				out = append(out, set)
			}
		}
		cfg.QualitySizeSets = out
	}); err != nil {
		writeError(w, 500, "Failed to delete the set")
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

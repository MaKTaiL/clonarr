package arr

import (
	"encoding/json"
	"fmt"
)

// Quality sizes live in one of two places depending on the app and version.
//
// Radarr, and Sonarr up to v4, keep them in one shared list at
// /qualitydefinition: three numbers per quality, applied to every profile.
// Sonarr v5 moved them onto each quality profile's items instead, dropped the
// three fields from the shared list, and now accepts and ignores writes to it.
//
// Nothing here asks the app its version. The shape of the data answers the
// question, which cannot go stale after an upgrade, cannot be wiped by an
// unrelated config edit, and fails on the safe side when the instance is
// unreachable: no list to read means no list to write either.

// QualityDefinitionsCarrySizes reports whether this instance keeps quality
// sizes in the shared definition list. False means they live on the quality
// profiles instead, so writing to the shared list has no effect.
//
// An empty list also answers false. That only happens when something is wrong
// with the instance, and refusing to write is the safer of the two mistakes.
func QualityDefinitionsCarrySizes(defs []ArrQualityDefinition) bool {
	for _, d := range defs {
		if d.MinSize != nil || d.MaxSize != nil || d.PreferredSize != nil {
			return true
		}
	}
	return false
}

// ProfileItemsCarrySizes is the same question asked of a quality profile's
// items, for the places that hold a profile but not the definition list. The
// versions that keep sizes on the profile put all three fields on every
// quality there; the versions that do not have no such fields at all.
func ProfileItemsCarrySizes(items []ArrQualityItem) bool {
	for _, it := range items {
		if it.MinSize != nil || it.MaxSize != nil || it.PreferredSize != nil {
			return true
		}
		if len(it.Items) > 0 && ProfileItemsCarrySizes(it.Items) {
			return true
		}
	}
	return false
}

// KeepsQualitySizesPerProfile asks the instance where its quality sizes live.
// Convenience for callers that do not already hold the definition list; the
// ones that do should call QualityDefinitionsCarrySizes directly rather than
// fetch it twice.
func (c *ArrClient) KeepsQualitySizesPerProfile() (bool, error) {
	defs, err := c.ListQualityDefinitions()
	if err != nil {
		return false, err
	}
	return !QualityDefinitionsCarrySizes(defs), nil
}

// GetProfileSchema fetches the blank quality profile the app offers as a
// starting point, which is what its own UI builds a new profile from. On the
// versions that keep sizes per profile it carries the app's default sizes, so
// it is the right source for a profile we are about to create.
func (c *ArrClient) GetProfileSchema() (*ArrQualityProfile, error) {
	data, status, err := c.DoRequest("GET", "/qualityprofile/schema", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncate(string(data), 200))
	}
	var schema ArrQualityProfile
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("parse profile schema: %w", err)
	}
	return &schema, nil
}

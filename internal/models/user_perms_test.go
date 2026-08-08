package models_test

import (
	"encoding/json"
	"testing"

	"gorm.io/datatypes"

	"jarvishomeassist-brain/internal/models"
)

// GetResourcePerms feeds `resource_perms` in every login and refresh response.
// A nil slice marshals to JSON `null`, which clients crash on when they index
// it or read .length. Assert on the marshalled JSON, not len() — len(nil) is 0
// in Go, which is exactly how this class of bug hides from Go-side tests.
func TestGetResourcePermsNeverMarshalsToNull(t *testing.T) {
	cases := map[string]datatypes.JSON{
		"nil column":        nil,
		"literal null":      datatypes.JSON(`null`),
		"empty array":       datatypes.JSON(`[]`),
		"malformed json":    datatypes.JSON(`{not json`),
		"populated array":   datatypes.JSON(`["ui:view","media:view"]`),
		"wrong shape (obj)": datatypes.JSON(`{"a":1}`),
	}

	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			u := models.User{ResourcePerms: stored}

			perms := u.GetResourcePerms()
			if perms == nil {
				t.Fatal("GetResourcePerms returned nil")
			}

			raw, err := json.Marshal(perms)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) == "null" {
				t.Errorf("marshalled to null; clients read .length off this")
			}
		})
	}

	// The populated case must still round-trip its values.
	u := models.User{ResourcePerms: datatypes.JSON(`["ui:view","media:view"]`)}
	got := u.GetResourcePerms()
	if len(got) != 2 || got[0] != "ui:view" || got[1] != "media:view" {
		t.Errorf("perms = %v, want [ui:view media:view]", got)
	}
}

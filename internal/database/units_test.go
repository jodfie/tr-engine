package database

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestEffectiveUnitPatchSource(t *testing.T) {
	text := "Engine 1"
	empty := ""

	tests := []struct {
		name     string
		current  string
		alphaTag *string
		want     string
	}{
		{"no alpha tag preserves empty source", "", nil, ""},
		{"no alpha tag preserves explicit source", "csv", nil, "csv"},
		{"alpha tag marks manual", "", &text, "manual"},
		{"alpha tag overrides explicit csv source", "csv", &text, "manual"},
		{"alpha tag overrides explicit mqtt source", "mqtt", &text, "manual"},
		{"empty alpha tag preserves empty source", "", &empty, ""},
		{"empty alpha tag preserves explicit source", "csv", &empty, "csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveUnitPatchSource(tt.current, tt.alphaTag); got != tt.want {
				t.Fatalf("effectiveUnitPatchSource(%q) = %q, want %q", tt.current, got, tt.want)
			}
		})
	}
}

func TestUnitAPITagObservationsJSON(t *testing.T) {
	t.Run("omitted_when_unknown", func(t *testing.T) {
		data, err := json.Marshal(UnitAPI{SystemID: 1, UnitID: 338, AlphaTag: "Engine 1"})
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"recorder_alpha_tag", "recorder_alpha_tag_seen", "ota_alpha_tag", "ota_alpha_tag_first_seen", "ota_alpha_tag_last_seen"} {
			if _, ok := raw[k]; ok {
				t.Errorf("unexpected key %q in %s", k, data)
			}
		}
	})

	t.Run("present_beside_alpha_tag", func(t *testing.T) {
		seen := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		data, err := json.Marshal(UnitAPI{
			SystemID: 1, UnitID: 338,
			AlphaTag: "FRNSW - P 338 - Jindabyne", AlphaTagSource: "manual",
			RecorderAlphaTag: "P338 FF1", RecorderAlphaTagSeen: &seen,
			OTAAlphaTag: "P338 FF1", OTAAlphaTagFirstSeen: &seen, OTAAlphaTagLastSeen: &seen,
		})
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"alpha_tag":                "FRNSW - P 338 - Jindabyne",
			"recorder_alpha_tag":       "P338 FF1",
			"recorder_alpha_tag_seen":  "2026-09-01T12:00:00Z",
			"ota_alpha_tag":            "P338 FF1",
			"ota_alpha_tag_first_seen": "2026-09-01T12:00:00Z",
			"ota_alpha_tag_last_seen":  "2026-09-01T12:00:00Z",
		}
		for k, v := range want {
			if raw[k] != v {
				t.Errorf("%s = %v, want %v", k, raw[k], v)
			}
		}
	})
}

// unitObservationMergeSQL is spliced into statements with different parameter
// numbering (ImportUpsertUnit, MergeSystems), so it must read the incoming
// values only through EXCLUDED and never through positional parameters.
func TestUnitObservationMergeSQLUsesOnlyExcluded(t *testing.T) {
	if regexp.MustCompile(`\$[0-9]`).MatchString(unitObservationMergeSQL) {
		t.Errorf("unitObservationMergeSQL must not reference $n parameters:\n%s", unitObservationMergeSQL)
	}
	for _, col := range []string{"recorder_alpha_tag", "recorder_alpha_tag_seen", "ota_alpha_tag", "ota_alpha_tag_first_seen", "ota_alpha_tag_last_seen"} {
		if !strings.Contains(unitObservationMergeSQL, "\n\t"+col+" = CASE") {
			t.Errorf("unitObservationMergeSQL does not assign %s", col)
		}
		if !strings.Contains(unitObservationMergeSQL, "EXCLUDED."+col) {
			t.Errorf("unitObservationMergeSQL does not read EXCLUDED.%s", col)
		}
	}
}

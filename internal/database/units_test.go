package database

import "testing"

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

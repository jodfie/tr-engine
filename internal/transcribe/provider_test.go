package transcribe

import (
	"reflect"
	"testing"
)

func TestInterpolateWords(t *testing.T) {
	got := interpolateWords("  ten   four  ", 1.0, 2.0)
	want := []Word{
		{Word: "ten", Start: 1.0, End: 1.5},
		{Word: "four", Start: 1.5, End: 2.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Rounded to the millisecond (no float noise in stored JSON).
	got = interpolateWords("a b c", 0.1, 1.0)
	if got[0].End != 0.4 || got[1].End != 0.7 || got[2].End != 1.0 {
		t.Errorf("unexpected rounding: %+v", got)
	}

	if w := interpolateWords("   ", 0, 1); w != nil {
		t.Errorf("blank text: got %+v, want nil", w)
	}
}

func TestWordsFromSegments_DeepInfra(t *testing.T) {
	got := wordsFromSegments([]deepInfraSegment{
		{Text: " Engine 7 ", Start: 0, End: 1},
		{Text: "", Start: 1, End: 2},
		{Text: "copy", Start: 2, End: 2.5},
	})
	want := []Word{
		{Word: "Engine", Start: 0, End: 0.5},
		{Word: "7", Start: 0.5, End: 1},
		{Word: "copy", Start: 2, End: 2.5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

package transcribe

import (
	"encoding/json"
	"testing"
)

func TestBuildSegments_PunctuationPreserved(t *testing.T) {
	// Word tokens lack punctuation, but fullText has it.
	words := []AttributedWord{
		{Word: "Air", Start: 0.0, End: 0.3, Src: 1},
		{Word: "2", Start: 0.3, End: 0.5, Src: 1},
		{Word: "pilot", Start: 0.5, End: 0.9, Src: 1},
		{Word: "weather", Start: 1.0, End: 1.4, Src: 1},
		{Word: "check", Start: 1.4, End: 1.8, Src: 1},
	}
	fullText := "Air 2 pilot, weather check."

	segments := buildSegments(words, fullText)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0].Text != "Air 2 pilot, weather check." {
		t.Errorf("expected %q, got %q", "Air 2 pilot, weather check.", segments[0].Text)
	}
}

func TestBuildSegments_MultiSegmentPunctuation(t *testing.T) {
	// Two units — punctuation should land in the correct segment.
	words := []AttributedWord{
		{Word: "Air", Start: 0.0, End: 0.3, Src: 1, SrcTag: "Unit1"},
		{Word: "2", Start: 0.3, End: 0.5, Src: 1, SrcTag: "Unit1"},
		{Word: "pilot", Start: 0.5, End: 0.9, Src: 1, SrcTag: "Unit1"},
		{Word: "weather", Start: 1.0, End: 1.4, Src: 2, SrcTag: "Unit2"},
		{Word: "check", Start: 1.4, End: 1.8, Src: 2, SrcTag: "Unit2"},
	}
	fullText := "Air 2 pilot, weather check."

	segments := buildSegments(words, fullText)

	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].Text != "Air 2 pilot," {
		t.Errorf("segment 0: expected %q, got %q", "Air 2 pilot,", segments[0].Text)
	}
	if segments[0].Src != 1 {
		t.Errorf("segment 0: expected src=1, got src=%d", segments[0].Src)
	}
	if segments[1].Text != "weather check." {
		t.Errorf("segment 1: expected %q, got %q", "weather check.", segments[1].Text)
	}
	if segments[1].Src != 2 {
		t.Errorf("segment 1: expected src=2, got src=%d", segments[1].Src)
	}
}

func TestBuildSegments_SingleSegment(t *testing.T) {
	words := []AttributedWord{
		{Word: "hello", Start: 0.0, End: 0.5, Src: 1},
		{Word: "world", Start: 0.5, End: 1.0, Src: 1},
	}
	fullText := "Hello, world!"

	segments := buildSegments(words, fullText)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0].Text != "Hello, world!" {
		t.Errorf("expected %q, got %q", "Hello, world!", segments[0].Text)
	}
}

func TestBuildSegments_EmptyFullTextFallback(t *testing.T) {
	// When fullText is empty, fall back to joining word tokens.
	words := []AttributedWord{
		{Word: "hello", Start: 0.0, End: 0.5, Src: 1},
		{Word: "world", Start: 0.5, End: 1.0, Src: 1},
	}

	segments := buildSegments(words, "")

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0].Text != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", segments[0].Text)
	}
}

func TestBuildSegments_RepeatedWordsMatchSequentially(t *testing.T) {
	// "go go go" — each "go" should match sequentially in fullText.
	words := []AttributedWord{
		{Word: "go", Start: 0.0, End: 0.3, Src: 1},
		{Word: "go", Start: 0.3, End: 0.6, Src: 2},
		{Word: "go", Start: 0.6, End: 0.9, Src: 1},
	}
	fullText := "Go, go, go!"

	segments := buildSegments(words, fullText)

	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	if segments[0].Text != "Go," {
		t.Errorf("segment 0: expected %q, got %q", "Go,", segments[0].Text)
	}
	if segments[1].Text != "go," {
		t.Errorf("segment 1: expected %q, got %q", "go,", segments[1].Text)
	}
	if segments[2].Text != "go!" {
		t.Errorf("segment 2: expected %q, got %q", "go!", segments[2].Text)
	}
}

func TestAttributeWords_WithFullText(t *testing.T) {
	// Integration test: AttributeWords with transmissions and fullText.
	srcList := json.RawMessage(`[{"src":100,"tag":"Engine 1","pos":0.0},{"src":200,"tag":"Dispatch","pos":1.0}]`)
	transmissions := ParseSrcList(srcList, 2.0)

	words := []Word{
		{Word: "responding", Start: 0.1, End: 0.5},
		{Word: "to", Start: 0.5, End: 0.7},
		{Word: "scene", Start: 0.7, End: 0.9},
		{Word: "copy", Start: 1.1, End: 1.4},
		{Word: "that", Start: 1.4, End: 1.7},
	}
	fullText := "Responding to scene. Copy that."

	tw := AttributeWords(words, transmissions, fullText)

	if len(tw.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(tw.Segments))
	}
	if tw.Segments[0].Text != "Responding to scene." {
		t.Errorf("segment 0: expected %q, got %q", "Responding to scene.", tw.Segments[0].Text)
	}
	if tw.Segments[0].Src != 100 {
		t.Errorf("segment 0: expected src=100, got src=%d", tw.Segments[0].Src)
	}
	if tw.Segments[1].Text != "Copy that." {
		t.Errorf("segment 1: expected %q, got %q", "Copy that.", tw.Segments[1].Text)
	}
	if tw.Segments[1].Src != 200 {
		t.Errorf("segment 1: expected src=200, got src=%d", tw.Segments[1].Src)
	}
}

func TestAttributeWords_EmptyWords(t *testing.T) {
	tw := AttributeWords(nil, nil, "some text")
	if len(tw.Words) != 0 {
		t.Errorf("expected 0 words, got %d", len(tw.Words))
	}
	if len(tw.Segments) != 0 {
		t.Errorf("expected 0 segments, got %d", len(tw.Segments))
	}
}

func TestAttributeWords_NoTransmissions(t *testing.T) {
	words := []Word{
		{Word: "hello", Start: 0.0, End: 0.5},
		{Word: "world", Start: 0.5, End: 1.0},
	}
	fullText := "Hello, world!"

	tw := AttributeWords(words, nil, fullText)

	if len(tw.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(tw.Segments))
	}
	if tw.Segments[0].Src != 0 {
		t.Errorf("expected src=0, got src=%d", tw.Segments[0].Src)
	}
	if tw.Segments[0].Text != "Hello, world!" {
		t.Errorf("expected %q, got %q", "Hello, world!", tw.Segments[0].Text)
	}
}

func TestMapWordPositions_CaseInsensitive(t *testing.T) {
	words := []AttributedWord{
		{Word: "HELLO"},
		{Word: "World"},
	}
	fullText := "Hello, World!"

	positions := mapWordPositions(words, fullText)

	if positions[0] != 0 {
		t.Errorf("expected position 0 for HELLO, got %d", positions[0])
	}
	if positions[1] != 7 {
		t.Errorf("expected position 7 for World, got %d", positions[1])
	}
}

// ── ParseSrcList ─────────────────────────────────────────────────────

func TestParseSrcList(t *testing.T) {
	t.Run("nil_input", func(t *testing.T) {
		result := ParseSrcList(nil, 10.0)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("empty_json", func(t *testing.T) {
		result := ParseSrcList(json.RawMessage(``), 10.0)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("null_json", func(t *testing.T) {
		result := ParseSrcList(json.RawMessage(`null`), 10.0)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("malformed_json", func(t *testing.T) {
		result := ParseSrcList(json.RawMessage(`{bad json`), 10.0)
		if result != nil {
			t.Errorf("expected nil for malformed JSON, got %v", result)
		}
	})

	t.Run("single_transmission", func(t *testing.T) {
		src := json.RawMessage(`[{"src":100,"tag":"Engine 1","pos":0.0}]`)
		result := ParseSrcList(src, 5.0)
		if len(result) != 1 {
			t.Fatalf("expected 1 transmission, got %d", len(result))
		}
		if result[0].Src != 100 {
			t.Errorf("Src = %d, want 100", result[0].Src)
		}
		if result[0].Tag != "Engine 1" {
			t.Errorf("Tag = %q, want %q", result[0].Tag, "Engine 1")
		}
		if result[0].Pos != 0.0 {
			t.Errorf("Pos = %f, want 0.0", result[0].Pos)
		}
		// Single entry: duration = totalDuration - pos = 5.0 - 0.0 = 5.0
		if result[0].Duration != 5.0 {
			t.Errorf("Duration = %f, want 5.0", result[0].Duration)
		}
	})

	t.Run("multiple_transmissions", func(t *testing.T) {
		src := json.RawMessage(`[
			{"src":100,"tag":"Engine 1","pos":0.0},
			{"src":200,"tag":"Dispatch","pos":2.5},
			{"src":100,"tag":"Engine 1","pos":4.0}
		]`)
		result := ParseSrcList(src, 6.0)
		if len(result) != 3 {
			t.Fatalf("expected 3 transmissions, got %d", len(result))
		}
		// First: duration = 2.5 - 0.0 = 2.5
		if result[0].Duration != 2.5 {
			t.Errorf("[0] Duration = %f, want 2.5", result[0].Duration)
		}
		// Second: duration = 4.0 - 2.5 = 1.5
		if result[1].Duration != 1.5 {
			t.Errorf("[1] Duration = %f, want 1.5", result[1].Duration)
		}
		// Last: duration = 6.0 - 4.0 = 2.0
		if result[2].Duration != 2.0 {
			t.Errorf("[2] Duration = %f, want 2.0", result[2].Duration)
		}
	})

	t.Run("negative_duration_clamped", func(t *testing.T) {
		// Edge case: totalDuration < last pos (shouldn't happen but be safe)
		src := json.RawMessage(`[{"src":100,"pos":5.0}]`)
		result := ParseSrcList(src, 3.0)
		if result[0].Duration != 0 {
			t.Errorf("Duration = %f, want 0 (clamped)", result[0].Duration)
		}
	})
}

// ── fixBoundaryWords ─────────────────────────────────────────────────

func TestAttributeWords_BoundaryWordReassignment(t *testing.T) {
	// Real-world pattern: dispatch calls "1 Paul 31", unit responds "1 Paul 31".
	// Whisper places the first word of the response ("1") slightly before the
	// transmission boundary, within the previous transmission's time range.
	// The boundary word should be reassigned to the next transmission.
	srcList := json.RawMessage(`[
		{"src":100,"tag":"Dispatch","pos":0.0},
		{"src":200,"tag":"Unit","pos":1.08},
		{"src":100,"tag":"Dispatch","pos":2.70}
	]`)
	transmissions := ParseSrcList(srcList, 6.0)

	words := []Word{
		// Tx1: "1 Paul 31" (dispatch calling)
		{Word: "1", Start: 0.32, End: 0.40},
		{Word: "Paul", Start: 0.40, End: 0.64},
		{Word: "31", Start: 0.64, End: 1.04},
		// Boundary artifact: first word of tx2, timestamped at 1.04 (before boundary 1.08)
		{Word: "1", Start: 1.04, End: 1.04},
		// Tx2: "Paul 31" (unit responding)
		{Word: "Paul", Start: 1.92, End: 2.08},
		{Word: "31", Start: 2.08, End: 2.32},
		// Boundary artifact: first word of tx3, timestamped at 2.32 (before boundary 2.70)
		{Word: "1", Start: 2.32, End: 2.32},
		// Tx3: "Paul 31 info..."
		{Word: "Paul", Start: 3.28, End: 3.60},
		{Word: "31", Start: 3.76, End: 4.32},
	}
	fullText := "1 Paul 31. 1 Paul 31. 1 Paul 31 info."

	tw := AttributeWords(words, transmissions, fullText)

	// Word 3 ("1" at 1.04) should be reassigned from Dispatch to Unit
	if tw.Words[3].Src != 200 {
		t.Errorf("boundary word 3: expected src=200 (Unit), got src=%d", tw.Words[3].Src)
	}
	// Word 6 ("1" at 2.32) should be reassigned from Unit to Dispatch
	if tw.Words[6].Src != 100 {
		t.Errorf("boundary word 6: expected src=100 (Dispatch), got src=%d", tw.Words[6].Src)
	}

	// Segments should now have correct text splits
	if len(tw.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(tw.Segments))
	}
	if tw.Segments[0].Src != 100 {
		t.Errorf("segment 0: expected src=100, got %d", tw.Segments[0].Src)
	}
	if tw.Segments[0].Text != "1 Paul 31." {
		t.Errorf("segment 0: expected %q, got %q", "1 Paul 31.", tw.Segments[0].Text)
	}
	if tw.Segments[1].Src != 200 {
		t.Errorf("segment 1: expected src=200, got %d", tw.Segments[1].Src)
	}
	if tw.Segments[1].Text != "1 Paul 31." {
		t.Errorf("segment 1: expected %q, got %q", "1 Paul 31.", tw.Segments[1].Text)
	}
	if tw.Segments[2].Src != 100 {
		t.Errorf("segment 2: expected src=100, got %d", tw.Segments[2].Src)
	}
}

func TestAttributeWords_StartTimeNotMidpoint(t *testing.T) {
	// Word that straddles a transmission boundary: start in tx1, midpoint in tx2.
	// Using start time should keep it in tx1.
	srcList := json.RawMessage(`[
		{"src":100,"tag":"Unit A","pos":0.0},
		{"src":200,"tag":"Unit B","pos":4.50}
	]`)
	transmissions := ParseSrcList(srcList, 10.0)

	words := []Word{
		{Word: "go", Start: 4.32, End: 4.48},      // well within tx1
		{Word: "ahead", Start: 4.48, End: 4.60},    // starts in tx1, midpoint at 4.54 would be in tx2
		{Word: "copy", Start: 5.10, End: 5.40},     // clearly in tx2
	}
	fullText := "Go ahead. Copy."

	tw := AttributeWords(words, transmissions, fullText)

	// "ahead" should stay in tx1 (src=100) since it starts at 4.48 < 4.50
	if tw.Words[1].Src != 100 {
		t.Errorf("word 'ahead': expected src=100 (start-time attribution), got src=%d", tw.Words[1].Src)
	}
	if tw.Words[2].Src != 200 {
		t.Errorf("word 'copy': expected src=200, got src=%d", tw.Words[2].Src)
	}
}

func TestAttributeWords_LegitimateEndWordNotReassigned(t *testing.T) {
	// A legitimate last word of a long transmission should NOT be reassigned
	// even though it's followed by a gap and a different-source word.
	srcList := json.RawMessage(`[
		{"src":100,"tag":"Dispatch","pos":0.0},
		{"src":200,"tag":"Unit","pos":15.0}
	]`)
	transmissions := ParseSrcList(srcList, 20.0)

	words := []Word{
		{Word: "weaving", Start: 13.80, End: 14.20}, // well within tx1, 0.8s from end
		// Gap of 1.5s (silence between transmissions)
		{Word: "copy", Start: 15.70, End: 16.00},    // in tx2
	}
	fullText := "Weaving. Copy."

	tw := AttributeWords(words, transmissions, fullText)

	// "weaving" should stay in tx1 — it's 0.8s from tx1's end, well beyond tolerance
	if tw.Words[0].Src != 100 {
		t.Errorf("word 'weaving': expected src=100 (legitimate end word), got src=%d", tw.Words[0].Src)
	}
}

func TestFixBoundaryWords_NoTransmissions(t *testing.T) {
	// Edge case: fewer than 2 transmissions — no boundary fixing needed
	words := []AttributedWord{
		{Word: "hello", Start: 0.0, End: 0.5, Src: 1},
	}
	txs := []Transmission{{Src: 1, Pos: 0, Duration: 1.0}}
	fixBoundaryWords(words, txs)
	if words[0].Src != 1 {
		t.Errorf("expected src=1, got src=%d", words[0].Src)
	}
}

func TestBuildSegments_SpeakerDoesNotSplit(t *testing.T) {
	// Same src (e.g. conventional channel with no unit IDs) but two diarized
	// speakers: segments still group by src only (irc-radio-live.html maps
	// segments to transmissions one-to-one), and a mixed segment has no speaker.
	// Checked with and without fullText.
	mixed := []AttributedWord{
		{Word: "Engine", Start: 0.0, End: 0.3, Speaker: "A"},
		{Word: "7", Start: 0.3, End: 0.5, Speaker: "A"},
		{Word: "copy", Start: 1.0, End: 1.4, Speaker: "B"},
		{Word: "that", Start: 1.4, End: 1.6, Speaker: "A"},
	}
	for _, fullText := range []string{"Engine 7. Copy that.", ""} {
		segments := buildSegments(mixed, fullText)
		if len(segments) != 1 {
			t.Fatalf("fullText=%q: expected 1 segment, got %d: %+v", fullText, len(segments), segments)
		}
		if segments[0].Speaker != "" {
			t.Errorf("fullText=%q: mixed segment speaker = %q, want empty", fullText, segments[0].Speaker)
		}
		if segments[0].Start != 0.0 || segments[0].End != 1.6 {
			t.Errorf("fullText=%q: segment times = %v-%v", fullText, segments[0].Start, segments[0].End)
		}
	}

	// A src change still splits, and a segment whose words share one label keeps it.
	uniform := []AttributedWord{
		{Word: "Engine", Start: 0.0, End: 0.3, Src: 1, Speaker: "A"},
		{Word: "7", Start: 0.3, End: 0.5, Src: 1, Speaker: "A"},
		{Word: "copy", Start: 1.0, End: 1.4, Src: 2, Speaker: "B"},
	}
	for _, fullText := range []string{"Engine 7. Copy.", ""} {
		segments := buildSegments(uniform, fullText)
		if len(segments) != 2 || segments[0].Speaker != "A" || segments[1].Speaker != "B" {
			t.Errorf("fullText=%q: got %+v, want 2 segments with speakers A, B", fullText, segments)
		}
	}
}

// diarizedWords builds interpolated words for one utterance, as
// diarizedToResponse does.
func diarizedWords(utterance int, speaker, text string, start, end float64) []Word {
	words := interpolateWords(text, start, end)
	for i := range words {
		words[i].Speaker = speaker
		words[i].Utterance = utterance
	}
	return words
}

func TestSnapUtterances(t *testing.T) {
	type seg struct {
		src  int
		text string
	}
	tests := []struct {
		name  string
		words []Word
		src   string
		want  []seg
	}{
		{
			// Speaker B starts 200ms before src_list's boundary (control-channel
			// lag). Per-word attribution would put "Copy" on 1001.
			name: "segment starts in lag window",
			words: append(diarizedWords(1, "A", "Engine 7 responding.", 0.1, 2.6),
				diarizedWords(2, "B", "Copy Engine 7.", 2.8, 5.0)...),
			src:  `[{"src":1001,"pos":0},{"src":2002,"pos":3.0}]`,
			want: []seg{{1001, "Engine 7 responding."}, {2002, "Copy Engine 7."}},
		},
		{
			// Speaker A runs 400ms past the boundary, so its last interpolated
			// word starts at 3.022s; it stays on 1001 with the rest of the segment.
			name: "segment ends in lag window",
			words: append(diarizedWords(1, "A", "Engine 7 responding to Main and Fifth right now.", 0.0, 3.4),
				diarizedWords(2, "B", "Copy.", 3.5, 4.0)...),
			src:  `[{"src":1001,"pos":0},{"src":2002,"pos":3.0}]`,
			want: []seg{{1001, "Engine 7 responding to Main and Fifth right now."}, {2002, "Copy."}},
		},
		{
			// One segment the diarizer didn't split, overlapping both units for
			// well over the lag: words stay divided by time.
			name:  "unsplit segment across two units",
			words: diarizedWords(1, "A", "one two three four five", 0.0, 5.0),
			src:   `[{"src":1001,"pos":0},{"src":2002,"pos":3.0}]`,
			want:  []seg{{1001, "one two three"}, {2002, "four five"}},
		},
		{
			// A segment shorter than the tolerance overlaps every unit by less
			// than 0.5s. It goes wholly to the unit it overlaps most (2002,
			// 0.3s vs 1001's 0.1s) rather than being left unattributed.
			name: "short segment straddling boundary",
			words: append(diarizedWords(1, "A", "Engine 7 responding.", 0.1, 2.6),
				diarizedWords(2, "B", "Copy that.", 2.9, 3.3)...),
			src:  `[{"src":1001,"pos":0},{"src":2002,"pos":3.0}]`,
			want: []seg{{1001, "Engine 7 responding."}, {2002, "Copy that."}},
		},
		{
			// 1001 transmits twice. Its overlaps with the utterance are summed
			// (0.25s + 0.35s = 0.6s), so its words are kept even though each
			// transmission alone is under the tolerance and 2002 (0.8s) wins.
			name:  "overlap summed per unit",
			words: diarizedWords(1, "A", "a b c d e f g h i j k l m n", 0.65, 2.05),
			src:   `[{"src":1001,"pos":0},{"src":2002,"pos":0.9},{"src":1001,"pos":1.7}]`,
			want:  []seg{{1001, "a b c"}, {2002, "d e f g h i j k"}, {1001, "l m n"}},
		},
		{
			// Real word timestamps (Utterance 0) are left to per-word attribution.
			name: "real word timestamps untouched",
			words: []Word{
				{Word: "Engine", Start: 0.1, End: 0.9},
				{Word: "7", Start: 0.9, End: 1.2},
				{Word: "copy", Start: 2.8, End: 3.2},
				{Word: "that", Start: 3.2, End: 3.6},
			},
			src:  `[{"src":1001,"pos":0},{"src":2002,"pos":3.0}]`,
			want: []seg{{1001, "Engine 7 copy"}, {2002, "that"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			txs := ParseSrcList(json.RawMessage(tt.src), 6.0)
			tw := AttributeWords(tt.words, txs, "")
			var got []seg
			for _, s := range tw.Segments {
				got = append(got, seg{s.Src, s.Text})
			}
			if len(got) != len(tt.want) {
				t.Fatalf("segments = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("segment %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSnapUtterances_NoOverlapKeepsAttribution(t *testing.T) {
	// An utterance entirely after the last transmission (e.g. src_list shorter
	// than the audio) overlaps nothing: per-word nearest-transmission
	// attribution stands.
	words := diarizedWords(1, "A", "ten four", 7.0, 8.0)
	txs := []Transmission{{Src: 1001, Pos: 0, Duration: 3}, {Src: 2002, Pos: 3, Duration: 3}}
	tw := AttributeWords(words, txs, "")
	for _, w := range tw.Words {
		if w.Src != 2002 {
			t.Errorf("word %q src = %d, want 2002 (nearest)", w.Word, w.Src)
		}
	}
}

func TestAttributeWords_NoSpeakerKeyForWhisper(t *testing.T) {
	// Providers that don't diarize must produce the same JSON as before
	// (no "speaker" key on words or segments).
	words := []Word{{Word: "hello", Start: 0.0, End: 0.5}}
	txs := ParseSrcList(json.RawMessage(`[{"src":100,"tag":"Unit","pos":0.0}]`), 1.0)
	b, err := json.Marshal(AttributeWords(words, txs, "Hello."))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"words":[{"word":"hello","start":0,"end":0.5,"src":100,"src_tag":"Unit"}],"segments":[{"src":100,"src_tag":"Unit","start":0,"end":0.5,"text":"Hello."}]}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

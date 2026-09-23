package unittags

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/snarg/tr-engine/internal/database"
)

var callStart = time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestUtterances(t *testing.T) {
	t.Run("segments with unit attribution", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Segments: raw(`[
				{"src":0,"start":0,"end":1,"text":"stray words"},
				{"src":1001,"src_tag":"BC M12","start":1,"end":3.5,"text":"County, Medic 12 on scene."},
				{"src":2000,"start":4,"end":6,"text":"Medic 12, copy, 14:32."}
			]`),
			SrcList: raw(`[{"src":1001,"pos":1},{"src":2000,"pos":4}]`),
		}
		got := Utterances(tr)
		if len(got) != 2 {
			t.Fatalf("got %d utterances, want 2: %+v", len(got), got)
		}
		if got[0].Src != 1001 || got[0].SrcTag != "BC M12" || got[0].Text != "County, Medic 12 on scene." || got[0].Start != 1 {
			t.Errorf("utterance 0 = %+v", got[0])
		}
		if got[1].Src != 2000 {
			t.Errorf("utterance 1 src = %d, want 2000", got[1].Src)
		}
	})

	t.Run("words when no segments were stored", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Words: raw(`[
				{"word":"County,","start":0.1,"end":0.4,"src":1001},
				{"word":"Engine","start":0.5,"end":0.8,"src":1001},
				{"word":"5","start":0.8,"end":0.9,"src":1001},
				{"word":"copy","start":2.0,"end":2.2,"src":2000}
			]`),
		}
		got := Utterances(tr)
		if len(got) != 2 || got[0].Text != "County, Engine 5" || got[0].End != 0.9 || got[1].Text != "copy" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("no word timings, single unit in src_list", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Text:     "County, Medic 12 on scene.",
			Segments: raw(`[]`),
			SrcList:  raw(`[{"src":1001,"tag":"M12","pos":0},{"src":1001,"pos":3}]`),
			Duration: 6,
		}
		got := Utterances(tr)
		if len(got) != 1 || got[0].Src != 1001 || got[0].SrcTag != "M12" || got[0].End != 6 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("no word timings, several units: skipped", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Text:    "County, Medic 12 on scene. Medic 12 copy.",
			SrcList: raw(`[{"src":1001,"pos":0},{"src":2000,"pos":3}]`),
		}
		if got := Utterances(tr); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("no word timings, unknown transmitter: skipped", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Text:    "County, Medic 12 on scene.",
			SrcList: raw(`[{"src":1001,"pos":0},{"src":-1,"pos":3}]`),
		}
		if got := Utterances(tr); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("all words unattributed and no src_list: skipped", func(t *testing.T) {
		tr := database.TagScanTranscription{
			Text:     "County, Medic 12 on scene.",
			Segments: raw(`[{"src":0,"start":0,"end":2,"text":"County, Medic 12 on scene."}]`),
		}
		if got := Utterances(tr); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
}

func TestBuildSightings(t *testing.T) {
	tr := database.TagScanTranscription{
		ID:            77,
		CallID:        9001,
		CallStartTime: callStart.In(time.FixedZone("EDT", -4*3600)),
		HasCall:       true,
		SystemID:      1,
		Tgid:          24513,
		CallGroupID:   555,
		TgAlphaTag:    "Engine 5 Ops",
	}
	utts := []Utterance{
		{Src: 1001, Text: "County, Medic 12 on scene.", Start: 1, End: 3},
		{Src: 1001, Text: "Medic 12 to County, we'll be transporting.", Start: 8, End: 11},
		{Src: 1002, SrcTag: "Engine 71", Text: "Dispatch, Engine 71 responding.", Start: 12, End: 14},
		{Src: 1003, Text: "County, Engine 5 on scene.", Start: 15, End: 16}, // talkgroup name
		{Src: 1004, Text: "County, Rescue 2 on scene.", Start: 17, End: 18},
	}
	current := map[database.UnitKey]string{
		{SystemID: 1, UnitID: 1001}: "",
		{SystemID: 1, UnitID: 1004}: "BCFD Rescue 2",
	}

	got := BuildSightings(tr, utts, current)
	if len(got) != 3 {
		t.Fatalf("got %d sightings, want 3: %+v", len(got), got)
	}

	m12 := got[0]
	if m12.UnitID != 1001 || m12.TagKey != "MEDIC 12" || m12.ProposedTag != "Medic 12" {
		t.Errorf("sighting 0 = %+v", m12)
	}
	if m12.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2 (same unit+tag twice in one call)", m12.Occurrences)
	}
	if m12.MatchesCurrentTag {
		t.Error("MEDIC 12 should not match an empty tag")
	}
	ev := m12.Evidence
	if ev.CallID != 9001 || ev.CallGroupID != 555 || ev.TranscriptionID != 77 || ev.Tgid != 24513 || ev.Pattern != PatternAddressee ||
		ev.Excerpt != "County, Medic 12 on scene." || ev.Start != 1 || ev.End != 3 {
		t.Errorf("evidence = %+v", ev)
	}
	if ev.CallStartTime.Location() != time.UTC || !ev.CallStartTime.Equal(callStart) {
		t.Errorf("evidence call_start_time = %v, want %v in UTC", ev.CallStartTime, callStart)
	}

	// Unit not in the current-tag map falls back to the src_list tag.
	if got[1].UnitID != 1002 || !got[1].MatchesCurrentTag || got[1].CurrentTag != "Engine 71" {
		t.Errorf("Engine 71 sighting = %+v, want matches_current_tag via src tag", got[1])
	}
	// Designator contained in the current tag.
	if got[2].UnitID != 1004 || got[2].TagKey != "RESCUE 2" || !got[2].MatchesCurrentTag {
		t.Errorf("Rescue 2 sighting = %+v, want matches_current_tag", got[2])
	}
	// The tag each flag was computed against is recorded with it.
	if m12.CurrentTag != "" || got[2].CurrentTag != "BCFD Rescue 2" {
		t.Errorf("current tags = %q, %q, want \"\", \"BCFD Rescue 2\"", m12.CurrentTag, got[2].CurrentTag)
	}

	t.Run("no call", func(t *testing.T) {
		if s := BuildSightings(database.TagScanTranscription{ID: 1}, utts, current); s != nil {
			t.Errorf("got %+v, want nil", s)
		}
	})

	t.Run("superseded transcription", func(t *testing.T) {
		old := tr
		old.Superseded = true
		if s := BuildSightings(old, utts, current); s != nil {
			t.Errorf("got %+v, want nil (a newer transcription replaced it)", s)
		}
	})
}

func TestSettledPrefix(t *testing.T) {
	b := []database.TagScanTranscription{{ID: 1, Settled: true}, {ID: 2, Settled: true}, {ID: 3}, {ID: 4, Settled: true}}
	got := settledPrefix(b)
	if len(got) != 2 || got[1].ID != 2 {
		t.Errorf("settledPrefix = %+v, want ids 1,2 (stop at first unsettled, never skip it)", got)
	}
	if got := settledPrefix([]database.TagScanTranscription{{ID: 5}}); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// fakeStore is an in-memory Store with compare-and-swap cursor semantics.
type fakeStore struct {
	mu        sync.Mutex
	cursor    int64
	rows      []database.TagScanTranscription
	tags      map[database.UnitKey]string
	applied   [][]database.UnitTagSighting
	stealNext bool // simulate another process advancing the cursor first
	fetchErr  error
}

func (f *fakeStore) EnsureScanCursor(_ context.Context, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != database.UnitTagSuggestionsCursor {
		return 0, errors.New("unexpected cursor " + name)
	}
	return f.cursor, nil
}

func (f *fakeStore) FetchTagScanBatch(_ context.Context, afterID int64, limit int, _ time.Duration) ([]database.TagScanTranscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	var out []database.TagScanTranscription
	for _, r := range f.rows {
		if r.ID > afterID && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetUnitAlphaTags(_ context.Context, keys []database.UnitKey) (map[database.UnitKey]string, error) {
	out := map[database.UnitKey]string{}
	for _, k := range keys {
		if tag, ok := f.tags[k]; ok {
			out[k] = tag
		}
	}
	return out, nil
}

func (f *fakeStore) ApplyUnitTagSightings(_ context.Context, _ string, fromID, toID int64, s []database.UnitTagSighting, _ int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stealNext {
		f.stealNext = false
		f.cursor = toID
		return false, nil
	}
	if f.cursor != fromID {
		return false, nil
	}
	f.cursor = toID
	f.applied = append(f.applied, s)
	return true, nil
}

func scanRow(id int64, callID int64, src int, text string, settled bool) database.TagScanTranscription {
	return database.TagScanTranscription{
		ID: id, CallID: callID, CallStartTime: callStart, HasCall: true, SystemID: 1, Tgid: 100,
		Segments: raw(`[{"src":` + itoa(src) + `,"start":0,"end":2,"text":` + quote(text) + `}]`),
		Settled:  settled,
	}
}

func itoa(n int) string       { b, _ := json.Marshal(n); return string(b) }
func quote(s string) string   { b, _ := json.Marshal(s); return string(b) }
func testLog() zerolog.Logger { return zerolog.Nop() }

func TestScanOnce(t *testing.T) {
	ctx := context.Background()

	t.Run("processes batches from the cursor and stops at unsettled rows", func(t *testing.T) {
		f := &fakeStore{rows: []database.TagScanTranscription{
			scanRow(1, 11, 1001, "County, Medic 12 on scene.", true),
			scanRow(2, 12, 1001, "Medic 12 en route.", true),
			scanRow(3, 13, 1002, "10-4.", true),
			scanRow(4, 14, 1001, "Medic 12 clear.", false), // too young
		}}
		s := NewScanner(f, Options{BatchSize: 2, Log: testLog()})

		n, err := s.ScanOnce(ctx)
		if err != nil || n != 2 || f.cursor != 2 {
			t.Fatalf("first batch: n=%d err=%v cursor=%d, want n=2 cursor=2", n, err, f.cursor)
		}
		if len(f.applied[0]) != 2 {
			t.Errorf("first batch sightings = %+v, want 2", f.applied[0])
		}

		n, err = s.ScanOnce(ctx)
		if err != nil || n != 1 || f.cursor != 3 {
			t.Fatalf("second batch: n=%d err=%v cursor=%d, want n=1 cursor=3", n, err, f.cursor)
		}
		if len(f.applied[1]) != 0 {
			t.Errorf("second batch sightings = %+v, want none", f.applied[1])
		}

		// Only the unsettled row remains: nothing consumed, cursor unchanged.
		n, err = s.ScanOnce(ctx)
		if err != nil || n != 0 || f.cursor != 3 {
			t.Fatalf("third pass: n=%d err=%v cursor=%d, want n=0 cursor=3", n, err, f.cursor)
		}

		f.rows[3].Settled = true
		if n, _ = s.ScanOnce(ctx); n != 1 || f.cursor != 4 {
			t.Fatalf("after settling: n=%d cursor=%d, want n=1 cursor=4", n, f.cursor)
		}
	})

	t.Run("superseded rows and rows without a live call are consumed without evidence", func(t *testing.T) {
		old := scanRow(1, 11, 1001, "County, Medic 21 on scene.", true)
		old.Superseded = true
		gone := scanRow(2, 12, 1001, "County, Medic 21 on scene.", true)
		gone.HasCall, gone.SystemID = false, 0 // call on a soft-deleted system
		f := &fakeStore{rows: []database.TagScanTranscription{
			old, gone,
			scanRow(3, 11, 1001, "County, Medic 12 on scene.", true), // the correction
		}}
		s := NewScanner(f, Options{Log: testLog()})
		n, err := s.ScanOnce(ctx)
		if err != nil || n != 3 || f.cursor != 3 {
			t.Fatalf("n=%d err=%v cursor=%d, want 3/nil/3", n, err, f.cursor)
		}
		if got := f.applied[0]; len(got) != 1 || got[0].TagKey != "MEDIC 12" || got[0].Evidence.TranscriptionID != 3 {
			t.Errorf("sightings = %+v, want only MEDIC 12 from the correction", got)
		}
	})

	t.Run("cursor moved by another process: batch discarded", func(t *testing.T) {
		f := &fakeStore{stealNext: true, rows: []database.TagScanTranscription{
			scanRow(1, 11, 1001, "County, Medic 12 on scene.", true),
		}}
		s := NewScanner(f, Options{Log: testLog()})
		n, err := s.ScanOnce(ctx)
		if err != nil || n != 0 || len(f.applied) != 0 {
			t.Errorf("n=%d err=%v applied=%d, want 0/nil/0", n, err, len(f.applied))
		}
	})

	t.Run("fetch error is returned", func(t *testing.T) {
		f := &fakeStore{fetchErr: errors.New("boom")}
		s := NewScanner(f, Options{Log: testLog()})
		if _, err := s.ScanOnce(ctx); err == nil {
			t.Error("expected error")
		}
	})
}

func TestScannerStartStop(t *testing.T) {
	f := &fakeStore{rows: []database.TagScanTranscription{
		scanRow(1, 11, 1001, "County, Medic 12 on scene.", true),
	}}
	s := NewScanner(f, Options{Interval: time.Hour, Log: testLog()})
	s.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		c := f.cursor
		f.mu.Unlock()
		if c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scanner did not process the first batch")
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return while the loop was waiting on its interval")
	}
}

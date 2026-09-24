package unittags

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/snarg/tr-engine/internal/database"
	"github.com/snarg/tr-engine/internal/transcribe"
)

// Store is the subset of database.DB used by the scanner.
type Store interface {
	EnsureScanCursor(ctx context.Context, name string) (int64, error)
	FetchTagScanBatch(ctx context.Context, afterID int64, limit int, settle time.Duration) ([]database.TagScanTranscription, error)
	GetUnitAlphaTags(ctx context.Context, keys []database.UnitKey) (map[database.UnitKey]string, error)
	ApplyUnitTagSightings(ctx context.Context, cursorName string, fromID, toID int64, sightings []database.UnitTagSighting, evidenceCap int) (bool, error)
}

// Options configures the scanner. Zero values take the defaults noted below.
type Options struct {
	Interval    time.Duration // pause once caught up (default 60s)
	BatchSize   int           // transcriptions per transaction (default 200)
	BatchPause  time.Duration // pause between full batches while backfilling (default 250ms)
	SettleDelay time.Duration // leave transcriptions younger than this for the next pass (default 30s)
	EvidenceCap int           // evidence entries kept per suggestion (default 10)
	Log         zerolog.Logger
}

// Scanner walks the transcriptions table in id order from a persisted cursor,
// extracts unit self-identifications, and records them as suggestions. Each
// batch and its cursor advance commit together, so every transcription is
// processed once across restarts, and enabling the scanner backfills history.
type Scanner struct {
	db     Store
	opts   Options
	log    zerolog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewScanner creates a scanner. Call Start to run it.
func NewScanner(db Store, opts Options) *Scanner {
	if opts.Interval <= 0 {
		opts.Interval = 60 * time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 200
	}
	if opts.BatchPause <= 0 {
		opts.BatchPause = 250 * time.Millisecond
	}
	if opts.SettleDelay <= 0 {
		opts.SettleDelay = 30 * time.Second
	}
	if opts.EvidenceCap <= 0 {
		opts.EvidenceCap = 10
	}
	return &Scanner{db: db, opts: opts, log: opts.Log}
}

// Start launches the scan loop. It stops when ctx is cancelled or Stop is called.
func (s *Scanner) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go s.loop(ctx)
}

// Stop cancels the scan loop and waits for the in-flight batch to finish.
func (s *Scanner) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

func (s *Scanner) loop(ctx context.Context) {
	defer close(s.done)
	s.log.Info().
		Dur("interval", s.opts.Interval).
		Int("batch_size", s.opts.BatchSize).
		Msg("unit tag suggestion scanner started")

	var backlog, batches int
	for {
		n, err := s.ScanOnce(ctx)
		if ctx.Err() != nil {
			s.log.Info().Msg("unit tag suggestion scanner stopped")
			return
		}

		wait := s.opts.Interval
		switch {
		case err != nil:
			s.log.Warn().Err(err).Msg("unit tag suggestion scan failed")
		case n >= s.opts.BatchSize:
			// More waiting — keep going, gently.
			wait = s.opts.BatchPause
			backlog += n
			batches++
			if batches%50 == 0 {
				s.log.Info().Int("transcriptions", backlog).Msg("unit tag suggestion backfill in progress")
			}
		default:
			if batches > 0 {
				s.log.Info().Int("transcriptions", backlog+n).Msg("unit tag suggestion scanner caught up")
				backlog, batches = 0, 0
			}
		}

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			s.log.Info().Msg("unit tag suggestion scanner stopped")
			return
		case <-t.C:
		}
	}
}

// ScanOnce processes the next batch of transcriptions and returns how many
// were consumed (0 when caught up or when another process owns the cursor).
func (s *Scanner) ScanOnce(ctx context.Context) (int, error) {
	from, err := s.db.EnsureScanCursor(ctx, database.UnitTagSuggestionsCursor)
	if err != nil {
		return 0, err
	}
	batch, err := s.db.FetchTagScanBatch(ctx, from, s.opts.BatchSize, s.opts.SettleDelay)
	if err != nil {
		return 0, err
	}
	batch = settledPrefix(batch)
	if len(batch) == 0 {
		return 0, nil
	}

	utts := make([][]Utterance, len(batch))
	seen := make(map[database.UnitKey]bool)
	var keys []database.UnitKey
	for i, t := range batch {
		if !usable(t) {
			continue
		}
		utts[i] = Utterances(t)
		for _, u := range utts[i] {
			k := database.UnitKey{SystemID: t.SystemID, UnitID: u.Src}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	tags, err := s.db.GetUnitAlphaTags(ctx, keys)
	if err != nil {
		return 0, err
	}

	var sightings []database.UnitTagSighting
	for i, t := range batch {
		sightings = append(sightings, BuildSightings(t, utts[i], tags)...)
	}

	to := batch[len(batch)-1].ID
	applied, err := s.db.ApplyUnitTagSightings(ctx, database.UnitTagSuggestionsCursor, from, to, sightings, s.opts.EvidenceCap)
	if err != nil {
		return 0, err
	}
	if !applied {
		s.log.Debug().Int64("from", from).Msg("unit tag cursor moved by another process, or a system was merged; batch will be re-read")
		return 0, nil
	}
	s.log.Debug().
		Int64("from", from).
		Int64("to", to).
		Int("transcriptions", len(batch)).
		Int("sightings", len(sightings)).
		Msg("unit tag suggestion batch applied")
	return len(batch), nil
}

// usable reports whether a transcription can contribute evidence: it is joined
// to a call on a live system and is still the call's primary transcription. A
// superseded transcription (re-transcribed, or corrected by a person) is
// skipped in favour of its replacement, which has a higher id.
func usable(t database.TagScanTranscription) bool {
	return t.HasCall && t.SystemID != 0 && !t.Superseded
}

// settledPrefix returns the leading run of settled transcriptions. Stopping at
// the first young row (rather than filtering young rows out) keeps the cursor
// from moving past an id whose neighbours are still being written.
func settledPrefix(batch []database.TagScanTranscription) []database.TagScanTranscription {
	for i, t := range batch {
		if !t.Settled {
			return batch[:i]
		}
	}
	return batch
}

// Utterance is one unit's speech within a call.
type Utterance struct {
	Src    int
	SrcTag string // unit tag from the call's src_list (fallback for the current tag)
	Text   string
	Start  float64 // seconds into the call audio
	End    float64
}

// Utterances splits a transcription into per-unit speech using the stored
// word-level attribution (transcriptions.words: segments, or words when no
// segments were stored). Words attributed to src 0 (no transmission data) are
// ignored. Without any attributed words it only proceeds when every src_list
// entry is the same known unit, so the speaker is certain; otherwise the
// transcription is skipped rather than guessed.
func Utterances(t database.TagScanTranscription) []Utterance {
	var segs []transcribe.Segment
	if len(t.Segments) > 0 {
		_ = json.Unmarshal(t.Segments, &segs)
	}
	if len(segs) == 0 && len(t.Words) > 0 {
		var words []transcribe.AttributedWord
		if json.Unmarshal(t.Words, &words) == nil {
			segs = segmentsFromWords(words)
		}
	}

	var out []Utterance
	for _, sg := range segs {
		if sg.Src <= 0 || strings.TrimSpace(sg.Text) == "" {
			continue
		}
		out = append(out, Utterance{Src: sg.Src, SrcTag: sg.SrcTag, Text: sg.Text, Start: sg.Start, End: sg.End})
	}
	if len(out) > 0 {
		return out
	}

	// No usable word attribution (e.g. IMBE ASR, human corrections without
	// words, or no transmissions at transcription time).
	if src, tag, ok := singleSource(t.SrcList); ok && strings.TrimSpace(t.Text) != "" {
		return []Utterance{{Src: src, SrcTag: tag, Text: t.Text, Start: 0, End: t.Duration}}
	}
	return nil
}

// segmentsFromWords groups consecutive words from the same unit.
func segmentsFromWords(words []transcribe.AttributedWord) []transcribe.Segment {
	var segs []transcribe.Segment
	for _, w := range words {
		text := strings.TrimSpace(w.Word)
		if n := len(segs); n > 0 && segs[n-1].Src == w.Src {
			segs[n-1].End = w.End
			segs[n-1].Text += " " + text
			continue
		}
		segs = append(segs, transcribe.Segment{Src: w.Src, SrcTag: w.SrcTag, Start: w.Start, End: w.End, Text: text})
	}
	return segs
}

// singleSource returns the unit when every src_list entry is the same known
// unit. An entry with an unknown source (src <= 0) means someone else may have
// spoken, so it disqualifies the call.
func singleSource(srcList json.RawMessage) (int, string, bool) {
	if len(srcList) == 0 {
		return 0, "", false
	}
	var entries []struct {
		Src int    `json:"src"`
		Tag string `json:"tag"`
	}
	if json.Unmarshal(srcList, &entries) != nil {
		return 0, "", false
	}
	src, tag := 0, ""
	for _, e := range entries {
		if e.Src <= 0 || (src != 0 && e.Src != src) {
			return 0, "", false
		}
		src = e.Src
		if tag == "" {
			tag = e.Tag
		}
	}
	return src, tag, src > 0
}

// BuildSightings extracts candidates from each unit's speech in one
// transcription. Candidates naming the call's talkgroup are dropped; candidates
// matching the unit's current alpha tag are kept but flagged so they count
// toward the unit's evidence without being offered for review.
func BuildSightings(t database.TagScanTranscription, utts []Utterance, currentTags map[database.UnitKey]string) []database.UnitTagSighting {
	if !usable(t) || len(utts) == 0 {
		return nil
	}
	tgKeys := make(map[string]bool)
	for _, k := range DesignatorKeys(t.TgAlphaTag) {
		tgKeys[k] = true
	}
	for _, k := range DesignatorKeys(t.TgDescription) {
		tgKeys[k] = true
	}

	type unitTag struct {
		unit int
		key  string
	}
	index := make(map[unitTag]int)
	var out []database.UnitTagSighting
	for _, u := range utts {
		for _, c := range Extract(u.Text) {
			if tgKeys[c.Key] {
				continue
			}
			ut := unitTag{u.Src, c.Key}
			if i, ok := index[ut]; ok {
				out[i].Occurrences++
				continue
			}
			current, ok := currentTags[database.UnitKey{SystemID: t.SystemID, UnitID: u.Src}]
			if !ok {
				current = u.SrcTag
			}
			index[ut] = len(out)
			out = append(out, database.UnitTagSighting{
				SystemID:          t.SystemID,
				UnitID:            u.Src,
				TagKey:            c.Key,
				ProposedTag:       c.Display,
				Occurrences:       1,
				MatchesCurrentTag: MatchesTag(c.Key, current),
				CurrentTag:        current,
				SeenAt:            t.CallStartTime,
				Evidence: database.UnitTagEvidence{
					CallID:          t.CallID,
					CallGroupID:     t.CallGroupID,
					CallStartTime:   t.CallStartTime.UTC(),
					TranscriptionID: t.ID,
					Tgid:            t.Tgid,
					TgAlphaTag:      t.TgAlphaTag,
					Excerpt:         excerpt(u.Text, 240),
					Pattern:         c.Pattern,
					Start:           u.Start,
					End:             u.End,
				},
			})
		}
	}
	return out
}

func excerpt(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max])) + "…"
}

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UnitTagSuggestionsCursor is the scan_cursors row used by the unit tag
// suggestion scanner (internal/unittags).
const UnitTagSuggestionsCursor = "unit_tag_suggestions"

// unitTagMergeLockKey is a transaction-level advisory lock taken first, before
// any row lock, by every transaction that writes unit_tag_suggestions:
// ApplyUnitTagSightings, MergeSystems, ApproveUnitTagSuggestion and
// DismissUnitTagSuggestion. A system merge therefore never commits between a
// scanner batch's fetch and its write (sightings built for the merge's source
// system would otherwise land on the soft-deleted system after its suggestions
// were folded into the target), and an approval, which locks a suggestion and
// then its unit, cannot deadlock against a merge, which locks units and then
// suggestions. ("tr_utags" in ASCII.)
const unitTagMergeLockKey int64 = 0x74725F7574616773

// unitTagScanInDoubtWindow bounds the scanner's in-flight transaction check
// (see FetchTagScanBatch): transcriptions older than this are treated as
// settled even while an older transaction is still open, so an idle or
// runaway transaction cannot stall the queue for longer than this.
const unitTagScanInDoubtWindow = time.Hour

// Suggestion statuses.
const (
	SuggestionPending   = "pending"
	SuggestionApproved  = "approved"
	SuggestionDismissed = "dismissed"
)

var (
	// ErrSuggestionNotFound is returned when a unit tag suggestion ID does not exist.
	ErrSuggestionNotFound = errors.New("unit tag suggestion not found")
	// ErrSuggestionUnitNotFound is returned when approving a suggestion whose unit row is missing.
	ErrSuggestionUnitNotFound = errors.New("unit not found")
)

// SuggestionStatusError is returned when approving or dismissing a suggestion
// that has already been decided.
type SuggestionStatusError struct {
	Status string
}

func (e *SuggestionStatusError) Error() string {
	return fmt.Sprintf("unit tag suggestion is %s, not pending", e.Status)
}

// UnitTagEvidence is one call in which a unit identified itself with a
// candidate tag. Stored as a capped JSONB list on the suggestion, most recent
// call first.
type UnitTagEvidence struct {
	CallID          int64     `json:"call_id"`
	CallGroupID     int64     `json:"call_group_id,omitempty"` // multi-site recordings of one transmission share it
	CallStartTime   time.Time `json:"call_start_time"`
	TranscriptionID int64     `json:"transcription_id"`
	Tgid            int       `json:"tgid"`
	TgAlphaTag      string    `json:"tg_alpha_tag,omitempty"`
	Excerpt         string    `json:"excerpt"`             // the unit's own transmission text
	Pattern         string    `json:"pattern"`             // extraction rule that matched
	Start           float64   `json:"start"`               // seconds into the call audio
	End             float64   `json:"end"`                 // seconds into the call audio
	AudioURL        string    `json:"audio_url,omitempty"` // derived on read, never stored
}

// UnitTagSuggestionAPI is a suggestion row as returned by the API.
type UnitTagSuggestionAPI struct {
	ID                 int64             `json:"id"`
	SystemID           int               `json:"system_id"`
	SystemName         string            `json:"system_name,omitempty"`
	UnitID             int               `json:"unit_id"`
	UnitAlphaTag       string            `json:"unit_alpha_tag"`
	UnitAlphaTagSource string            `json:"unit_alpha_tag_source,omitempty"`
	TagKey             string            `json:"tag_key"`
	ProposedTag        string            `json:"proposed_tag"`
	Status             string            `json:"status"`
	Occurrences        int               `json:"occurrences"`
	CallCount          int               `json:"call_count"`
	Share              float64           `json:"share"`
	MatchesCurrentTag  bool              `json:"matches_current_tag"`
	FirstSeen          time.Time         `json:"first_seen"`
	LastSeen           time.Time         `json:"last_seen"`
	AppliedTag         *string           `json:"applied_tag"`
	PreviousTag        *string           `json:"previous_tag"`
	PreviousTagSource  *string           `json:"previous_tag_source"`
	DecidedAt          *time.Time        `json:"decided_at"`
	DecidedBy          *string           `json:"decided_by"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	Evidence           []UnitTagEvidence `json:"evidence"`
}

// UnitTagSuggestionFilter selects suggestions for the review API.
//
// Pending rows are only listed once they pass the review gate: seen in at
// least MinCalls distinct calls, at least MinShare of the unit's
// self-identification evidence, and not already the unit's current tag.
// Approved/dismissed rows are listed regardless of the gate.
type UnitTagSuggestionFilter struct {
	Status    string // "pending" (default), "approved", "dismissed", or "all"
	SystemIDs []int
	UnitIDs   []int
	ID        *int64 // single-row lookup; bypasses the review gate
	MinCalls  int
	MinShare  float64
	Limit     int
	Offset    int
}

// UnitKey identifies a unit within a system.
type UnitKey struct {
	SystemID int
	UnitID   int
}

// UnitTagSighting is one transcription's worth of evidence that a unit used a
// candidate tag. The scanner produces at most one per (unit, tag key) per
// transcription.
type UnitTagSighting struct {
	SystemID          int
	UnitID            int
	TagKey            string
	ProposedTag       string
	Occurrences       int
	MatchesCurrentTag bool
	CurrentTag        string    // the unit alpha tag MatchesCurrentTag was computed against
	SeenAt            time.Time // call start time
	Evidence          UnitTagEvidence
}

// TagScanTranscription is the scanner's view of a transcription and its call.
type TagScanTranscription struct {
	ID            int64
	CallID        int64
	CallStartTime time.Time
	Text          string
	Segments      json.RawMessage // words->'segments'
	Words         json.RawMessage // words->'words', only fetched when there are no segments
	Superseded    bool            // no longer the call's primary transcription (re-transcribed or corrected)
	HasCall       bool            // joined to its call, on a system that is not soft-deleted
	SystemID      int
	Tgid          int
	CallGroupID   int64 // 0 when the call has no call group
	TgAlphaTag    string
	TgDescription string
	SrcList       json.RawMessage
	Duration      float64
	Settled       bool // past the settle delay, and no older transaction may still hold a lower id
}

// EnsureScanCursor creates the named cursor at 0 if missing and returns its position.
func (db *DB) EnsureScanCursor(ctx context.Context, name string) (int64, error) {
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO scan_cursors (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, name); err != nil {
		return 0, err
	}
	var last int64
	err := db.Pool.QueryRow(ctx, `SELECT last_id FROM scan_cursors WHERE name = $1`, name).Scan(&last)
	return last, err
}

// UnitTagScanStatus reports the unit tag scanner's progress through the
// transcriptions table.
type UnitTagScanStatus struct {
	LastTranscriptionID int64      `json:"last_transcription_id"` // cursor: highest id processed
	MaxTranscriptionID  int64      `json:"max_transcription_id"`  // highest id in transcriptions
	UpdatedAt           *time.Time `json:"updated_at"`            // last cursor advance (null if never run)
}

// GetUnitTagScanStatus returns the scanner cursor and the current highest
// transcription ID.
func (db *DB) GetUnitTagScanStatus(ctx context.Context) (UnitTagScanStatus, error) {
	var st UnitTagScanStatus
	err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT last_id FROM scan_cursors WHERE name = $1), 0),
			(SELECT updated_at FROM scan_cursors WHERE name = $1),
			COALESCE((SELECT max(id) FROM transcriptions), 0)`,
		UnitTagSuggestionsCursor).Scan(&st.LastTranscriptionID, &st.UpdatedAt, &st.MaxTranscriptionID)
	return st, err
}

// FetchTagScanBatch returns up to limit transcriptions with id > afterID, in
// id order, joined to their call. The scanner stops at the first row that is
// not Settled, so the cursor never passes a lower id that is still invisible
// because its inserting transaction has not committed. A row is settled when
// it is older than settle and either older than unitTagScanInDoubtWindow or
// written by a transaction older than every transaction still open when the
// batch was read (the snapshot's xmin). An open transaction holding a lower id
// got its id, and so its xid, before the later row's writer did (barring the
// instant between nextval and the row write), so the later row stays
// unsettled until that transaction ends, however long it takes. Any older
// open transaction in the cluster only delays the scanner, by at most the
// window.
// Superseded rows (a newer transcription of the call exists, which the cursor
// will reach) and calls on soft-deleted systems are returned so the cursor
// can pass them, but carry no usable evidence.
func (db *DB) FetchTagScanBatch(ctx context.Context, afterID int64, limit int, settle time.Duration) ([]TagScanTranscription, error) {
	// The LIMIT sits in a subquery so the batch is cut from the primary key
	// before joining calls, however large the backlog. Transaction ids are
	// compared modulo 2^32 (xmin is a 32-bit xid, the snapshot's an epoch-
	// extended xid8); inside the window a row is far less than 2^31 xids old,
	// so the wraparound-aware comparison is exact. xids below 3 are special
	// (frozen/bootstrap) and always settled.
	rows, err := db.Pool.Query(ctx, `
		SELECT t.id, t.call_id, t.call_start_time, COALESCE(t.text, ''),
			t.words -> 'segments',
			CASE WHEN jsonb_typeof(t.words -> 'segments') = 'array'
			          AND jsonb_array_length(t.words -> 'segments') > 0
			     THEN NULL ELSE t.words -> 'words' END,
			NOT t.is_primary,
			c.system_id, sy.deleted_at IS NOT NULL, c.tgid, c.call_group_id,
			COALESCE(c.tg_alpha_tag, ''), COALESCE(c.tg_description, ''),
			c.src_list, COALESCE(c.duration, 0)::float8,
			t.created_at < now() - ($3::float8 * interval '1 second')
			AND (t.created_at < now() - ($4::float8 * interval '1 second')
			     OR t.row_xid < 3
			     OR ((t.row_xid - snap.xmin32) % 4294967296 + 4294967296) % 4294967296 >= 2147483648)
		FROM (
			SELECT id, call_id, call_start_time, text, words, is_primary, created_at,
				xmin::text::bigint AS row_xid
			FROM transcriptions
			WHERE id > $1
			ORDER BY id
			LIMIT $2
		) t
		CROSS JOIN (SELECT pg_snapshot_xmin(pg_current_snapshot())::text::bigint % 4294967296 AS xmin32) snap
		LEFT JOIN calls c ON c.call_id = t.call_id AND c.start_time = t.call_start_time
		LEFT JOIN systems sy ON sy.system_id = c.system_id
		ORDER BY t.id`, afterID, limit, settle.Seconds(), unitTagScanInDoubtWindow.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TagScanTranscription
	for rows.Next() {
		var t TagScanTranscription
		var systemID, tgid *int
		var callGroupID *int64
		var systemDeleted bool
		if err := rows.Scan(&t.ID, &t.CallID, &t.CallStartTime, &t.Text,
			&t.Segments, &t.Words, &t.Superseded,
			&systemID, &systemDeleted, &tgid, &callGroupID, &t.TgAlphaTag, &t.TgDescription,
			&t.SrcList, &t.Duration, &t.Settled); err != nil {
			return nil, err
		}
		if systemID != nil && !systemDeleted {
			t.HasCall = true
			t.SystemID = *systemID
		}
		if tgid != nil {
			t.Tgid = *tgid
		}
		if callGroupID != nil {
			t.CallGroupID = *callGroupID
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetUnitAlphaTags returns the current alpha_tag for each requested unit that exists.
func (db *DB) GetUnitAlphaTags(ctx context.Context, keys []UnitKey) (map[UnitKey]string, error) {
	out := make(map[UnitKey]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	sys := make([]int, len(keys))
	units := make([]int, len(keys))
	for i, k := range keys {
		sys[i] = k.SystemID
		units[i] = k.UnitID
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT u.system_id, u.unit_id, COALESCE(u.alpha_tag, '')
		FROM units u
		JOIN unnest($1::int[], $2::int[]) AS k(system_id, unit_id)
		  ON u.system_id = k.system_id AND u.unit_id = k.unit_id`, sys, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k UnitKey
		var tag string
		if err := rows.Scan(&k.SystemID, &k.UnitID, &tag); err != nil {
			return nil, err
		}
		out[k] = tag
	}
	return out, rows.Err()
}

// upsertSightingSQL accumulates evidence on the (system, unit, tag_key) row,
// keeping the $9 most recent calls (by call start time) as evidence. A call
// already in the evidence list is not counted again (e.g. a re-transcription
// of the same call), and neither is another site's recording of the same
// transmission (same call group, as on a multi-site P25 system where every
// site's copy is transcribed). Once a call has aged out of the capped list, a
// later re-transcription or copy would count it twice, which only affects
// rows that are already well past the review gate. Decided rows keep their
// status: a dismissed candidate keeps counting but never returns to pending.
// matches_current_tag is stored with the unit tag it was computed against
// (tag_at_sighting) so a later tag change can be detected on read.
const upsertSightingSQL = `
INSERT INTO unit_tag_suggestions AS s
	(system_id, unit_id, tag_key, proposed_tag, occurrences, call_count,
	 matches_current_tag, tag_at_sighting, first_seen, last_seen, evidence)
VALUES ($1, $2, $3, $4, $5, 1, $6, $10, $7, $7, jsonb_build_array($8::jsonb))
ON CONFLICT (system_id, unit_id, tag_key) DO UPDATE SET
	occurrences         = s.occurrences + EXCLUDED.occurrences,
	call_count          = s.call_count + 1,
	matches_current_tag = EXCLUDED.matches_current_tag,
	tag_at_sighting     = EXCLUDED.tag_at_sighting,
	first_seen          = LEAST(s.first_seen, EXCLUDED.first_seen),
	last_seen           = GREATEST(s.last_seen, EXCLUDED.last_seen),
	evidence            = (
		SELECT COALESCE(jsonb_agg(e.item ORDER BY e.at DESC, e.ord), '[]'::jsonb)
		FROM (
			SELECT item, ord, (item ->> 'call_start_time')::timestamptz AS at
			FROM jsonb_array_elements(EXCLUDED.evidence || s.evidence) WITH ORDINALITY AS x(item, ord)
			ORDER BY at DESC, ord
			LIMIT $9
		) e
	)
WHERE NOT s.evidence @> jsonb_build_array(jsonb_build_object('call_id', EXCLUDED.evidence -> 0 -> 'call_id'))
  AND NOT ((EXCLUDED.evidence -> 0 -> 'call_group_id') IS NOT NULL
           AND s.evidence @> jsonb_build_array(jsonb_build_object('call_group_id', EXCLUDED.evidence -> 0 -> 'call_group_id')))`

// ApplyUnitTagSightings records sightings and advances the named cursor from
// fromID to toID in one transaction. The cursor update is a compare-and-swap:
// if another process already moved the cursor, nothing is written and
// applied=false is returned, so evidence is never counted twice. It also
// returns applied=false, writing nothing, when a system the sightings name
// was merged away (soft-deleted) after the batch was fetched; the next pass
// re-reads the batch with the calls on their new system.
func (db *DB) ApplyUnitTagSightings(ctx context.Context, cursorName string, fromID, toID int64, sightings []UnitTagSighting, evidenceCap int) (bool, error) {
	if evidenceCap < 1 {
		evidenceCap = 1
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Taken first, before any row lock, so this and MergeSystems serialize
	// without risk of deadlock.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, unitTagMergeLockKey); err != nil {
		return false, fmt.Errorf("lock against system merge: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE scan_cursors SET last_id = $3, updated_at = now() WHERE name = $1 AND last_id = $2`,
		cursorName, fromID, toID)
	if err != nil {
		return false, fmt.Errorf("advance cursor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if len(sightings) > 0 {
		seen := make(map[int]bool)
		var systemIDs []int
		for _, s := range sightings {
			if !seen[s.SystemID] {
				seen[s.SystemID] = true
				systemIDs = append(systemIDs, s.SystemID)
			}
		}
		var merged bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM systems WHERE system_id = ANY($1::int[]) AND deleted_at IS NOT NULL)`,
			systemIDs).Scan(&merged); err != nil {
			return false, fmt.Errorf("check systems: %w", err)
		}
		if merged {
			return false, nil
		}

		batch := &pgx.Batch{}
		for _, s := range sightings {
			ev, err := json.Marshal(s.Evidence)
			if err != nil {
				return false, fmt.Errorf("marshal evidence: %w", err)
			}
			batch.Queue(upsertSightingSQL,
				s.SystemID, s.UnitID, s.TagKey, s.ProposedTag, s.Occurrences,
				s.MatchesCurrentTag, s.SeenAt, string(ev), evidenceCap, s.CurrentTag)
		}
		br := tx.SendBatch(ctx, batch)
		for range sightings {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return false, fmt.Errorf("upsert suggestion: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return false, fmt.Errorf("upsert suggestion: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// unitTagSuggestionCTE computes each row's share of its unit's evidence (over
// all statuses, before filtering) and applies the review gate to pending rows.
// matches_tag is the flag stored at the last sighting (unittags.MatchesTag,
// which also matches phonetic/letter forms such as "1 Paul 31" vs "1P31"),
// trusted only while the unit's tag is still the one it was computed against
// (tag_at_sighting; or the unit row is missing). After a tag change it is a
// live comparison ignoring case and punctuation instead: the whole tag, or the
// designator as whole words inside it ("BCFD Medic 12"). Phonetic forms are
// picked up again at the next sighting. A single-row lookup ($4) narrows the
// share window to that row's unit.
//
// $1 system_ids int[], $2 unit_ids int[], $3 status, $4 id, $5 min_calls, $6 min_share
const unitTagSuggestionCTE = `
WITH scored AS (
	SELECT s.*,
		s.call_count::float8
			/ NULLIF(SUM(s.call_count) OVER (PARTITION BY s.system_id, s.unit_id), 0) AS share
	FROM unit_tag_suggestions s
	WHERE ($1::int[] IS NULL OR s.system_id = ANY($1))
	  AND ($2::int[] IS NULL OR s.unit_id = ANY($2))
	  AND ($4::bigint IS NULL OR (s.system_id, s.unit_id) =
	        (SELECT o.system_id, o.unit_id FROM unit_tag_suggestions o WHERE o.id = $4))
), joined AS (
	SELECT sc.*,
		COALESCE(sy.name, '') AS system_name,
		COALESCE(u.alpha_tag, '') AS unit_alpha_tag,
		COALESCE(u.alpha_tag_source, '') AS unit_alpha_tag_source,
		CASE WHEN u.unit_id IS NULL OR sc.tag_at_sighting = COALESCE(u.alpha_tag, '')
		     THEN sc.matches_current_tag
		     ELSE regexp_replace(upper(COALESCE(u.alpha_tag, '')), '[^[:alnum:]]+', '', 'g')
		            = replace(sc.tag_key, ' ', '')
		       OR (' ' || regexp_replace(upper(COALESCE(u.alpha_tag, '')), '[^[:alnum:]]+', ' ', 'g') || ' ')
		            LIKE ('% ' || sc.tag_key || ' %')
		END AS matches_tag
	FROM scored sc
	JOIN systems sy ON sy.system_id = sc.system_id AND sy.deleted_at IS NULL
	LEFT JOIN units u ON u.system_id = sc.system_id AND u.unit_id = sc.unit_id
), visible AS (
	SELECT j.*,
		max(j.call_count) OVER (PARTITION BY j.system_id, j.unit_id) AS unit_max_calls
	FROM joined j
	WHERE ($3::text = 'all' OR j.status = $3)
	  AND ($4::bigint IS NULL OR j.id = $4)
	  AND ($4::bigint IS NOT NULL OR j.status <> 'pending' OR (
	        j.call_count >= $5
	    AND COALESCE(j.share, 0) >= $6
	    AND NOT j.matches_tag
	  ))
)`

// ListUnitTagSuggestions returns suggestions matching the filter and the total count.
func (db *DB) ListUnitTagSuggestions(ctx context.Context, filter UnitTagSuggestionFilter) ([]UnitTagSuggestionAPI, int, error) {
	status := filter.Status
	if status == "" {
		status = SuggestionPending
	}
	args := []any{pqIntArray(filter.SystemIDs), pqIntArray(filter.UnitIDs), status,
		filter.ID, filter.MinCalls, filter.MinShare}

	var total int
	if err := db.Pool.QueryRow(ctx, unitTagSuggestionCTE+` SELECT count(*) FROM visible`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Pending review: keep each unit's candidates together, busiest units first.
	orderBy := `(status = 'pending') DESC, unit_max_calls DESC, system_id, unit_id, call_count DESC, last_seen DESC, id`
	if status == SuggestionApproved || status == SuggestionDismissed {
		orderBy = `COALESCE(decided_at, updated_at) DESC, id DESC`
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	rows, err := db.Pool.Query(ctx, unitTagSuggestionCTE+`
		SELECT id, system_id, system_name, unit_id, unit_alpha_tag, unit_alpha_tag_source,
			tag_key, proposed_tag, status, occurrences, call_count, COALESCE(share, 0),
			matches_tag, first_seen, last_seen, applied_tag, previous_tag, previous_tag_source,
			decided_at, decided_by, created_at, updated_at, evidence
		FROM visible
		ORDER BY `+orderBy+`
		LIMIT $7 OFFSET $8`, append(args, limit, filter.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []UnitTagSuggestionAPI{}
	for rows.Next() {
		var s UnitTagSuggestionAPI
		var evidence []byte
		if err := rows.Scan(&s.ID, &s.SystemID, &s.SystemName, &s.UnitID, &s.UnitAlphaTag, &s.UnitAlphaTagSource,
			&s.TagKey, &s.ProposedTag, &s.Status, &s.Occurrences, &s.CallCount, &s.Share,
			&s.MatchesCurrentTag, &s.FirstSeen, &s.LastSeen, &s.AppliedTag, &s.PreviousTag, &s.PreviousTagSource,
			&s.DecidedAt, &s.DecidedBy, &s.CreatedAt, &s.UpdatedAt, &evidence); err != nil {
			return nil, 0, err
		}
		s.Evidence = []UnitTagEvidence{}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &s.Evidence); err != nil {
				return nil, 0, fmt.Errorf("decode evidence for suggestion %d: %w", s.ID, err)
			}
		}
		for i := range s.Evidence {
			s.Evidence[i].AudioURL = fmt.Sprintf("/api/v1/calls/%d/audio", s.Evidence[i].CallID)
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// GetUnitTagSuggestion returns one suggestion by ID regardless of status or review gate.
func (db *DB) GetUnitTagSuggestion(ctx context.Context, id int64) (*UnitTagSuggestionAPI, error) {
	rows, _, err := db.ListUnitTagSuggestions(ctx, UnitTagSuggestionFilter{Status: "all", ID: &id, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrSuggestionNotFound
	}
	return &rows[0], nil
}

// UnitTagApproval describes the unit update performed by an approval.
type UnitTagApproval struct {
	SuggestionID int64
	SystemID     int
	UnitID       int
	AppliedTag   string
}

// ApproveUnitTagSuggestion applies a pending suggestion to its unit and marks
// it approved, in one transaction. The unit is written through the same query
// as PATCH /units/{id} with alpha_tag_source='manual' (a person explicitly
// chose the tag), so MQTT and CSV re-imports will not overwrite it.
// alphaTag overrides the proposed tag when non-empty ("edit then approve").
// It waits for any system merge in progress; a suggestion the merge folded
// into the target system's row no longer exists (ErrSuggestionNotFound).
func (db *DB) ApproveUnitTagSuggestion(ctx context.Context, id int64, alphaTag, decidedBy string) (*UnitTagApproval, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Taken first, before the suggestion and unit row locks (see unitTagMergeLockKey).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, unitTagMergeLockKey); err != nil {
		return nil, fmt.Errorf("lock against system merge: %w", err)
	}

	a := UnitTagApproval{SuggestionID: id}
	var status, proposed string
	err = tx.QueryRow(ctx, `
		SELECT system_id, unit_id, status, proposed_tag
		FROM unit_tag_suggestions WHERE id = $1 FOR UPDATE`, id).
		Scan(&a.SystemID, &a.UnitID, &status, &proposed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSuggestionNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != SuggestionPending {
		return nil, &SuggestionStatusError{Status: status}
	}

	a.AppliedTag = alphaTag
	if a.AppliedTag == "" {
		a.AppliedTag = proposed
	}

	// Lock the unit row and remember its tag for the suggestion's provenance.
	var prevTag, prevSource *string
	err = tx.QueryRow(ctx,
		`SELECT alpha_tag, alpha_tag_source FROM units WHERE system_id = $1 AND unit_id = $2 FOR UPDATE`,
		a.SystemID, a.UnitID).Scan(&prevTag, &prevSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSuggestionUnitNotFound
	}
	if err != nil {
		return nil, err
	}

	manual := "manual"
	if err := updateUnitFields(ctx, db.Q.WithTx(tx), a.SystemID, a.UnitID, &a.AppliedTag, &manual); err != nil {
		return nil, fmt.Errorf("update unit: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE unit_tag_suggestions
		SET status = 'approved', applied_tag = $2, previous_tag = $3, previous_tag_source = $4,
			decided_at = now(), decided_by = NULLIF($5, '')
		WHERE id = $1`, id, a.AppliedTag, prevTag, prevSource, decidedBy); err != nil {
		return nil, fmt.Errorf("mark approved: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &a, nil
}

// DismissUnitTagSuggestion marks a pending suggestion dismissed. The unit is
// not touched. Later sightings keep accumulating on the dismissed row. Like
// approval it serializes with system merges, so a merge folding this row into
// the target system's row sees the decision (or the dismissal sees the fold).
func (db *DB) DismissUnitTagSuggestion(ctx context.Context, id int64, decidedBy string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, unitTagMergeLockKey); err != nil {
		return fmt.Errorf("lock against system merge: %w", err)
	}

	var status string
	err = tx.QueryRow(ctx, `
		UPDATE unit_tag_suggestions
		SET status = 'dismissed', decided_at = now(), decided_by = NULLIF($2, '')
		WHERE id = $1 AND status = 'pending'
		RETURNING status`, id, decidedBy).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT status FROM unit_tag_suggestions WHERE id = $1`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSuggestionNotFound
		}
		if err != nil {
			return err
		}
		return &SuggestionStatusError{Status: status}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

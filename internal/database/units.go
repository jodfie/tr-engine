package database

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/snarg/tr-engine/internal/database/sqlcdb"
)

// UnitFilter specifies filters for listing units.
type UnitFilter struct {
	Sysid        *string
	Search       *string
	ActiveWithin *int // minutes
	Talkgroups   []int
	Limit        int
	Offset       int
	Sort         string
}

// UnitAPI represents a unit for API responses.
type UnitAPI struct {
	SystemID       int        `json:"system_id"`
	SystemName     string     `json:"system_name,omitempty"`
	Sysid          string     `json:"sysid,omitempty"`
	UnitID         int        `json:"unit_id"`
	AlphaTag       string     `json:"alpha_tag,omitempty"`
	AlphaTagSource string     `json:"alpha_tag_source,omitempty"`
	FirstSeen      *time.Time `json:"first_seen,omitempty"`
	LastSeen       *time.Time `json:"last_seen,omitempty"`
	LastEventType  *string    `json:"last_event_type,omitempty"`
	LastEventTime  *time.Time `json:"last_event_time,omitempty"`
	LastEventTgid  *int       `json:"last_event_tgid,omitempty"`
	LastEventTgTag string     `json:"last_event_tg_tag,omitempty"`
	// Tag observations stored beside alpha_tag (read-only; never change it).
	RecorderAlphaTag     string     `json:"recorder_alpha_tag,omitempty"`
	RecorderAlphaTagSeen *time.Time `json:"recorder_alpha_tag_seen,omitempty"`
	OTAAlphaTag          string     `json:"ota_alpha_tag,omitempty"`
	OTAAlphaTagFirstSeen *time.Time `json:"ota_alpha_tag_first_seen,omitempty"`
	OTAAlphaTagLastSeen  *time.Time `json:"ota_alpha_tag_last_seen,omitempty"`
	CallCount            *int       `json:"call_count,omitempty"`
	RelevanceScore       *int       `json:"relevance_score,omitempty"`
}

func unitRowToAPI(r sqlcdb.GetUnitByCompositeRow) UnitAPI {
	u := UnitAPI{
		SystemID:         r.SystemID,
		SystemName:       r.SystemName,
		Sysid:            r.Sysid,
		UnitID:           r.UnitID,
		AlphaTag:         r.AlphaTag,
		AlphaTagSource:   r.AlphaTagSource,
		LastEventType:    r.LastEventType,
		LastEventTgTag:   r.LastEventTgTag,
		RecorderAlphaTag: r.RecorderAlphaTag,
		OTAAlphaTag:      r.OtaAlphaTag,
	}
	if r.FirstSeen.Valid {
		u.FirstSeen = &r.FirstSeen.Time
	}
	if r.LastSeen.Valid {
		u.LastSeen = &r.LastSeen.Time
	}
	if r.LastEventTime.Valid {
		u.LastEventTime = &r.LastEventTime.Time
	}
	if r.LastEventTgid != nil {
		v := int(*r.LastEventTgid)
		u.LastEventTgid = &v
	}
	if r.RecorderAlphaTagSeen.Valid {
		u.RecorderAlphaTagSeen = &r.RecorderAlphaTagSeen.Time
	}
	if r.OtaAlphaTagFirstSeen.Valid {
		u.OTAAlphaTagFirstSeen = &r.OtaAlphaTagFirstSeen.Time
	}
	if r.OtaAlphaTagLastSeen.Valid {
		u.OTAAlphaTagLastSeen = &r.OtaAlphaTagLastSeen.Time
	}
	return u
}

// ListUnits returns units matching the filter.
func (db *DB) ListUnits(ctx context.Context, filter UnitFilter) ([]UnitAPI, int, error) {
	var activeWithin any
	if filter.ActiveWithin != nil {
		activeWithin = strconv.Itoa(*filter.ActiveWithin) + " minutes"
	}

	const fromClause = `FROM units u
		JOIN systems s ON s.system_id = u.system_id AND s.deleted_at IS NULL
		LEFT JOIN talkgroups tg ON tg.system_id = u.system_id AND tg.tgid = u.last_event_tgid`
	const whereClause = `
		WHERE ($1::text IS NULL OR s.sysid = $1)
		  AND ($2::text IS NULL OR u.alpha_tag ILIKE '%' || $2 || '%' OR u.unit_id::text = $2)
		  AND ($3::text IS NULL OR u.last_seen > now() - $3::interval)
		  AND ($4::int[] IS NULL OR u.last_event_tgid = ANY($4))`
	args := []any{filter.Sysid, filter.Search, activeWithin, pqIntArray(filter.Talkgroups)}

	var total int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) "+fromClause+whereClause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := "u.unit_id ASC"
	if filter.Sort != "" {
		orderBy = filter.Sort
	}

	dataQuery := fmt.Sprintf(`
		SELECT u.system_id, COALESCE(s.name, ''), s.sysid,
			u.unit_id, COALESCE(u.alpha_tag, ''), COALESCE(u.alpha_tag_source, ''),
			u.first_seen, u.last_seen,
			u.last_event_type, u.last_event_time, u.last_event_tgid,
			COALESCE(tg.alpha_tag, ''),
			COALESCE(u.recorder_alpha_tag, ''), u.recorder_alpha_tag_seen,
			COALESCE(u.ota_alpha_tag, ''), u.ota_alpha_tag_first_seen, u.ota_alpha_tag_last_seen
		%s %s
		ORDER BY %s
		LIMIT $5 OFFSET $6
	`, fromClause, whereClause, orderBy)

	rows, err := db.Pool.Query(ctx, dataQuery, append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var units []UnitAPI
	for rows.Next() {
		var u UnitAPI
		if err := rows.Scan(
			&u.SystemID, &u.SystemName, &u.Sysid,
			&u.UnitID, &u.AlphaTag, &u.AlphaTagSource,
			&u.FirstSeen, &u.LastSeen,
			&u.LastEventType, &u.LastEventTime, &u.LastEventTgid,
			&u.LastEventTgTag,
			&u.RecorderAlphaTag, &u.RecorderAlphaTagSeen,
			&u.OTAAlphaTag, &u.OTAAlphaTagFirstSeen, &u.OTAAlphaTagLastSeen,
		); err != nil {
			return nil, 0, err
		}

		if filter.Search != nil {
			search := *filter.Search
			score := 10
			if strconv.Itoa(u.UnitID) == search || u.AlphaTag == search {
				score = 100
			} else if len(search) > 0 && len(u.AlphaTag) >= len(search) && u.AlphaTag[:len(search)] == search {
				score = 50
			}
			u.RelevanceScore = &score
		}

		units = append(units, u)
	}
	if units == nil {
		units = []UnitAPI{}
	}
	return units, total, rows.Err()
}

// GetUnitByComposite returns a single unit by system_id and unit_id.
func (db *DB) GetUnitByComposite(ctx context.Context, systemID, unitID int) (*UnitAPI, error) {
	row, err := db.Q.GetUnitByComposite(ctx, sqlcdb.GetUnitByCompositeParams{
		SystemID: systemID,
		UnitID:   unitID,
	})
	if err != nil {
		return nil, err
	}
	u := unitRowToAPI(row)
	return &u, nil
}

// FindUnitSystems returns systems where a unit ID exists (for ambiguity resolution).
func (db *DB) FindUnitSystems(ctx context.Context, unitID int) ([]AmbiguousMatch, error) {
	rows, err := db.Q.FindUnitSystems(ctx, unitID)
	if err != nil {
		return nil, err
	}
	matches := make([]AmbiguousMatch, len(rows))
	for i, r := range rows {
		matches[i] = AmbiguousMatch{
			SystemID:   r.SystemID,
			SystemName: r.SystemName,
			Sysid:      r.Sysid,
		}
	}
	return matches, nil
}

// UpdateUnitFields updates mutable unit fields. Setting a non-empty alpha_tag
// marks the unit as manually tagged, so neither MQTT ingest nor a unit tags CSV
// re-import will overwrite the edit (same rule as UpdateTalkgroupFields). An
// empty alpha_tag leaves the tag and its source unchanged.
func (db *DB) UpdateUnitFields(ctx context.Context, systemID, unitID int, alphaTag, alphaTagSource *string) error {
	return updateUnitFields(ctx, db.Q, systemID, unitID, alphaTag, alphaTagSource)
}

// updateUnitFields is the shared write path for unit edits (PATCH /units/{id}
// and unit tag suggestion approval). q may be bound to a transaction.
func updateUnitFields(ctx context.Context, q *sqlcdb.Queries, systemID, unitID int, alphaTag, alphaTagSource *string) error {
	atVal := ""
	if alphaTag != nil {
		atVal = *alphaTag
	}
	srcVal := ""
	if alphaTagSource != nil {
		srcVal = *alphaTagSource
	}
	srcVal = effectiveUnitPatchSource(srcVal, alphaTag)
	return q.UpdateUnitFields(ctx, sqlcdb.UpdateUnitFieldsParams{
		AlphaTag:       atVal,
		AlphaTagSource: srcVal,
		SystemID:       systemID,
		UnitID:         unitID,
	})
}

// effectiveUnitPatchSource returns the alpha_tag_source a PATCH stores: a
// non-empty alpha_tag marks the unit manual. An empty alpha_tag is ignored by
// UpdateUnitFields, so it must not pin the current tag as manual either.
func effectiveUnitPatchSource(current string, alphaTag *string) string {
	if alphaTag != nil && *alphaTag != "" {
		return "manual"
	}
	return current
}

// UnitTag is one unit_id,alpha_tag entry of a unit tags CSV.
type UnitTag struct {
	UnitID   int
	AlphaTag string
}

// ImportUnitTags imports unit alpha_tags from a unit tags CSV (TR's unitTagsFile)
// in a single statement, so a large file is applied atomically with one commit.
// If a unit ID repeats, the last entry wins.
// Priority is manual > csv > mqtt: the CSV tag replaces MQTT-discovered tags and
// previously imported CSV tags (so an edited CSV takes effect on re-import), and
// marks the unit alpha_tag_source = 'csv'. Manual tags (user edits) are never
// overwritten; a manual unit with an empty tag is filled. Rows that would not
// change are left untouched so re-importing an unchanged CSV is a no-op.
// Returns the number of units inserted or changed.
func (db *DB) ImportUnitTags(ctx context.Context, systemID int, tags []UnitTag) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	ids := make([]int32, len(tags))
	alphaTags := make([]string, len(tags))
	for i, t := range tags {
		if t.UnitID <= 0 || t.UnitID > math.MaxInt32 {
			return 0, fmt.Errorf("unit ID %d out of range", t.UnitID)
		}
		ids[i] = int32(t.UnitID)
		alphaTags[i] = t.AlphaTag
	}
	// DISTINCT ON keeps the last entry per unit ID: ON CONFLICT DO UPDATE can't
	// touch the same row twice in one statement.
	tag, err := db.Pool.Exec(ctx, `
		INSERT INTO units (system_id, unit_id, alpha_tag, alpha_tag_source)
		SELECT $1, e.unit_id, e.alpha_tag, 'csv'
		FROM (
			SELECT DISTINCT ON (f.unit_id) f.unit_id, f.alpha_tag
			FROM unnest($2::int[], $3::text[]) WITH ORDINALITY AS f(unit_id, alpha_tag, ord)
			ORDER BY f.unit_id, f.ord DESC
		) e
		ON CONFLICT (system_id, unit_id) DO UPDATE SET
			alpha_tag = CASE WHEN COALESCE(units.alpha_tag_source, '') = 'manual'
			                 THEN COALESCE(NULLIF(units.alpha_tag, ''), EXCLUDED.alpha_tag)
			                 ELSE EXCLUDED.alpha_tag END,
			alpha_tag_source = CASE WHEN COALESCE(units.alpha_tag_source, '') = 'manual' THEN units.alpha_tag_source
			                        ELSE 'csv' END
		WHERE (units.alpha_tag, units.alpha_tag_source) IS DISTINCT FROM (
			CASE WHEN COALESCE(units.alpha_tag_source, '') = 'manual'
			     THEN COALESCE(NULLIF(units.alpha_tag, ''), EXCLUDED.alpha_tag)
			     ELSE EXCLUDED.alpha_tag END,
			CASE WHEN COALESCE(units.alpha_tag_source, '') = 'manual' THEN units.alpha_tag_source
			     ELSE 'csv' END)
	`, systemID, ids, alphaTags)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// UnitUpsertResult is the unit's stored tag state after UpsertUnit.
type UnitUpsertResult struct {
	AlphaTag    string // effective alpha_tag (manual > csv > mqtt priority)
	OTAAlphaTag string // latest known over-the-air alias ("" if never reported)
}

// UpsertUnit inserts or updates a unit, never overwriting good data with empty strings.
// alphaTag is the tag the recorder reported: it feeds alpha_tag (subject to the
// manual > csv > mqtt priority) and is always recorded as recorder_alpha_tag.
// otaAlphaTag is the raw over-the-air alias, if the plugin sent one; it only
// updates ota_alpha_tag. Empty strings leave the stored values untouched.
func (db *DB) UpsertUnit(ctx context.Context, systemID, unitID int, alphaTag, otaAlphaTag, eventType string, eventTime time.Time, tgid int) (UnitUpsertResult, error) {
	tgid32 := int32(tgid)
	row, err := db.Q.UpsertUnit(ctx, sqlcdb.UpsertUnitParams{
		SystemID:    systemID,
		UnitID:      unitID,
		AlphaTag:    &alphaTag,
		OtaAlphaTag: otaAlphaTag,
		EventType:   &eventType,
		EventTime:   pgtype.Timestamptz{Time: eventTime, Valid: true},
		Tgid:        &tgid32,
	})
	return UnitUpsertResult{AlphaTag: row.AlphaTag, OTAAlphaTag: row.OtaAlphaTag}, err
}

// UnitExport contains fields needed for export (no stats, no event details).
type UnitExport struct {
	SystemID       int
	UnitID         int
	AlphaTag       string
	AlphaTagSource string
	FirstSeen      *time.Time
	LastSeen       *time.Time

	RecorderAlphaTag     string
	RecorderAlphaTagSeen *time.Time
	OTAAlphaTag          string
	OTAAlphaTagFirstSeen *time.Time
	OTAAlphaTagLastSeen  *time.Time
}

// ExportUnits returns all units for the given systems, suitable for export.
func (db *DB) ExportUnits(ctx context.Context, systemIDs []int) ([]UnitExport, error) {
	// alpha_tag_source and the recorder/OTA observation columns are added by
	// separate migrations, so check each; export also runs read-only against
	// databases that haven't been migrated.
	sourceCol := `''`
	if db.columnExists(ctx, "units", "alpha_tag_source") {
		sourceCol = `COALESCE(alpha_tag_source, '')`
	}
	observationCols := `'', NULL::timestamptz, '', NULL::timestamptz, NULL::timestamptz`
	if db.columnExists(ctx, "units", "ota_alpha_tag_last_seen") {
		observationCols = `COALESCE(recorder_alpha_tag, ''), recorder_alpha_tag_seen,
			COALESCE(ota_alpha_tag, ''), ota_alpha_tag_first_seen, ota_alpha_tag_last_seen`
	}

	query := `SELECT system_id, unit_id,
			COALESCE(alpha_tag, ''), ` + sourceCol + `,
			first_seen, last_seen,
			` + observationCols + `
			FROM units WHERE ($1::int[] IS NULL OR system_id = ANY($1))
			ORDER BY system_id, unit_id`

	rows, err := db.Pool.Query(ctx, query, pqIntArray(systemIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UnitExport
	for rows.Next() {
		var u UnitExport
		if err := rows.Scan(
			&u.SystemID, &u.UnitID,
			&u.AlphaTag, &u.AlphaTagSource,
			&u.FirstSeen, &u.LastSeen,
			&u.RecorderAlphaTag, &u.RecorderAlphaTagSeen,
			&u.OTAAlphaTag, &u.OTAAlphaTagFirstSeen, &u.OTAAlphaTagLastSeen,
		); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// unitObservationMergeSQL is the ON CONFLICT DO UPDATE assignment list that
// merges an incoming row's recorder/OTA tag observations (EXCLUDED, with empty
// tags passed as NULL) into the stored unit. Archive import and system merge
// use it; their incoming rows carry whole observation windows, not one event.
//   - recorder_alpha_tag: the most recently seen tag wins.
//   - Same OTA alias: last_seen moves later. first_seen moves earlier only when
//     the incoming window overlaps the stored one, so an older, separate run
//     of the alias cannot stretch back across a change to another alias.
//   - Different OTA alias: the incoming alias and its window replace the
//     stored ones, unless the incoming alias was last seen before the stored one.
const unitObservationMergeSQL = `
	recorder_alpha_tag = CASE
		WHEN EXCLUDED.recorder_alpha_tag IS NOT NULL AND (units.recorder_alpha_tag IS NULL
			OR units.recorder_alpha_tag_seen IS NULL OR EXCLUDED.recorder_alpha_tag_seen >= units.recorder_alpha_tag_seen)
		THEN EXCLUDED.recorder_alpha_tag ELSE units.recorder_alpha_tag END,
	recorder_alpha_tag_seen = CASE WHEN EXCLUDED.recorder_alpha_tag IS NOT NULL
		THEN GREATEST(units.recorder_alpha_tag_seen, EXCLUDED.recorder_alpha_tag_seen) ELSE units.recorder_alpha_tag_seen END,
	ota_alpha_tag = CASE
		WHEN EXCLUDED.ota_alpha_tag IS NULL THEN units.ota_alpha_tag
		WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag = EXCLUDED.ota_alpha_tag
			OR units.ota_alpha_tag_last_seen IS NULL OR EXCLUDED.ota_alpha_tag_last_seen >= units.ota_alpha_tag_last_seen
		THEN EXCLUDED.ota_alpha_tag ELSE units.ota_alpha_tag END,
	ota_alpha_tag_first_seen = CASE
		WHEN EXCLUDED.ota_alpha_tag IS NULL THEN units.ota_alpha_tag_first_seen
		WHEN units.ota_alpha_tag = EXCLUDED.ota_alpha_tag THEN CASE
			WHEN EXCLUDED.ota_alpha_tag_last_seen >= units.ota_alpha_tag_first_seen
			THEN LEAST(units.ota_alpha_tag_first_seen, EXCLUDED.ota_alpha_tag_first_seen)
			ELSE COALESCE(units.ota_alpha_tag_first_seen, EXCLUDED.ota_alpha_tag_first_seen) END
		WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag_last_seen IS NULL
			OR EXCLUDED.ota_alpha_tag_last_seen >= units.ota_alpha_tag_last_seen THEN EXCLUDED.ota_alpha_tag_first_seen
		ELSE units.ota_alpha_tag_first_seen END,
	ota_alpha_tag_last_seen = CASE
		WHEN EXCLUDED.ota_alpha_tag IS NULL THEN units.ota_alpha_tag_last_seen
		WHEN units.ota_alpha_tag = EXCLUDED.ota_alpha_tag
		THEN GREATEST(units.ota_alpha_tag_last_seen, EXCLUDED.ota_alpha_tag_last_seen)
		WHEN units.ota_alpha_tag IS NULL OR units.ota_alpha_tag_last_seen IS NULL
			OR EXCLUDED.ota_alpha_tag_last_seen >= units.ota_alpha_tag_last_seen THEN EXCLUDED.ota_alpha_tag_last_seen
		ELSE units.ota_alpha_tag_last_seen END`

// ImportUpsertUnit upserts a unit from an export archive.
// Respects alpha_tag_source priority: manual > csv > mqtt. An empty archive tag
// never blanks an existing one, and an archive tag of lower priority (or with no
// source, i.e. MQTT-discovered) still fills an empty existing tag.
// Recorder/OTA tag observations merge as described on unitObservationMergeSQL.
func (db *DB) ImportUpsertUnit(ctx context.Context, u UnitExport) error {
	hasSource := db.columnExists(ctx, "units", "alpha_tag_source")

	if hasSource {
		// NULLIF($4, ''): rows exported without a source must insert NULL, not ''
		// (chk_units_alpha_tag_source rejects '').
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO units (system_id, unit_id, alpha_tag, alpha_tag_source, first_seen, last_seen,
				recorder_alpha_tag, recorder_alpha_tag_seen,
				ota_alpha_tag, ota_alpha_tag_first_seen, ota_alpha_tag_last_seen)
			VALUES ($1, $2, $3, NULLIF($4::text, ''), $5, $6, NULLIF($7::text, ''), $8, NULLIF($9::text, ''), $10, $11)
			ON CONFLICT (system_id, unit_id) DO UPDATE SET
				alpha_tag = CASE
					WHEN NULLIF($3, '') IS NULL THEN units.alpha_tag
					WHEN $4 = 'manual' THEN $3
					WHEN $4 = 'csv' AND COALESCE(units.alpha_tag_source, '') NOT IN ('manual') THEN $3
					WHEN $4 = 'mqtt' AND COALESCE(units.alpha_tag_source, '') NOT IN ('manual', 'csv') THEN $3
					ELSE COALESCE(NULLIF(units.alpha_tag, ''), $3)
				END,
				alpha_tag_source = CASE
					WHEN NULLIF($3, '') IS NULL THEN units.alpha_tag_source
					WHEN $4 = 'manual' THEN $4
					WHEN $4 = 'csv' AND COALESCE(units.alpha_tag_source, '') NOT IN ('manual') THEN $4
					WHEN $4 = 'mqtt' AND COALESCE(units.alpha_tag_source, '') NOT IN ('manual', 'csv') THEN $4
					ELSE units.alpha_tag_source
				END,
				first_seen = LEAST(units.first_seen, $5),
				last_seen  = GREATEST(units.last_seen, $6),`+unitObservationMergeSQL+`
		`, u.SystemID, u.UnitID, u.AlphaTag, u.AlphaTagSource, u.FirstSeen, u.LastSeen,
			u.RecorderAlphaTag, u.RecorderAlphaTagSeen,
			u.OTAAlphaTag, u.OTAAlphaTagFirstSeen, u.OTAAlphaTagLastSeen)
		return err
	}

	// Fallback: alpha_tag_source column doesn't exist yet
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO units (system_id, unit_id, alpha_tag, first_seen, last_seen)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (system_id, unit_id) DO UPDATE SET
			alpha_tag  = COALESCE(NULLIF($3, ''), units.alpha_tag),
			first_seen = LEAST(units.first_seen, $4),
			last_seen  = GREATEST(units.last_seen, $5)
	`, u.SystemID, u.UnitID, u.AlphaTag, u.FirstSeen, u.LastSeen)
	return err
}

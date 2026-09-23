package database

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// PreUpgradeTagEditsFixup is the data_fixups name of KeepPreUpgradeTagEdits. Its
// detail column holds the PinnedTagEdits it marked manual.
const PreUpgradeTagEditsFixup = "keep-pre-csv-priority-tag-edits"

// TalkgroupTag is one tgid → alpha_tag entry of a talkgroup CSV.
type TalkgroupTag struct {
	Tgid     int
	AlphaTag string
}

// CSVTags holds the entries of the talkgroup CSV and unit tags CSV that the
// startup TR_DIR import is about to import into one system.
type CSVTags struct {
	Talkgroups []TalkgroupTag
	Units      []UnitTag
}

// PinnedTagEdits lists the talkgroups and units KeepPreUpgradeTagEdits marked
// manual, as the API's composite IDs ("system_id:tgid", "system_id:unit_id"),
// sorted.
type PinnedTagEdits struct {
	Talkgroups []string `json:"talkgroups"`
	Units      []string `json:"units"`
}

// KeepPreUpgradeTagEdits runs once per database, at the first startup after
// upgrading to the version where CSV tags take priority over MQTT tags. It must
// run before the startup CSV import and before ingest starts.
//
// Older versions never marked tag edits: a dashboard edit of a unit or
// talkgroup whose tag came from a CSV left alpha_tag_source = 'csv', and a
// talkgroup edit was also copied into talkgroup_directory. That was safe
// because CSV imports never changed 'csv' rows. They now do (so edits to the
// CSV take effect), which would silently replace such edits with the CSV value.
//
// An edit can't be told apart from a CSV value in the database (the directory
// holds the edit too), so a 'csv' row stays 'csv' only when its tag is
// confirmed by the TR_DIR CSV about to be imported: talkgroups must match an
// entry for them in the system's TR_DIR talkgroup CSV, units an entry in its
// TR_DIR unit tags CSV. Every other 'csv' row with a tag is marked 'manual',
// which keeps exactly the behavior older versions had for it. That includes all
// 'csv' rows of systems without TR_DIR CSVs (uploads only), and CSV changes that
// older versions ignored. PATCH alpha_tag_source=csv hands a row back to the CSV.
//
// files maps system_id to its TR_DIR CSV entries. The rows marked manual are
// returned and stored in data_fixups.detail; ran is false when the fixup had
// already run.
func (db *DB) KeepPreUpgradeTagEdits(ctx context.Context, files map[int]CSVTags) (pinned PinnedTagEdits, ran bool, err error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Claim the fixup first. A concurrent start blocks on the primary key until
	// this transaction ends, then finds the row and skips.
	claim, err := tx.Exec(ctx, `INSERT INTO data_fixups (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`,
		PreUpgradeTagEditsFixup)
	if err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("claim fixup: %w", err)
	}
	if claim.RowsAffected() == 0 {
		return PinnedTagEdits{}, false, nil
	}

	// The TR_DIR entries of all systems, flattened into parallel arrays. The
	// statements group them per (system, ID), since a CSV may repeat an ID.
	var tgSys, tgIDs, unitSys, unitIDs []int32
	var tgTags, unitTags []string
	for systemID, f := range files {
		if systemID <= 0 || systemID > math.MaxInt32 {
			continue
		}
		for _, tg := range f.Talkgroups {
			if tg.Tgid > 0 && tg.Tgid <= math.MaxInt32 {
				tgSys, tgIDs, tgTags = append(tgSys, int32(systemID)), append(tgIDs, int32(tg.Tgid)), append(tgTags, tg.AlphaTag)
			}
		}
		for _, u := range f.Units {
			if u.UnitID > 0 && u.UnitID <= math.MaxInt32 {
				unitSys, unitIDs, unitTags = append(unitSys, int32(systemID)), append(unitIDs, int32(u.UnitID)), append(unitTags, u.AlphaTag)
			}
		}
	}

	pinned.Talkgroups, err = pinUnconfirmedTags(ctx, tx, `
		WITH f AS (
			SELECT e.system_id, e.id, array_agg(btrim(e.alpha_tag)) AS alpha_tags
			FROM unnest($1::int[], $2::int[], $3::text[]) AS e(system_id, id, alpha_tag)
			WHERE btrim(e.alpha_tag) <> ''
			GROUP BY e.system_id, e.id
		)
		UPDATE talkgroups t SET alpha_tag_source = 'manual'
		WHERE t.alpha_tag_source = 'csv'
		  AND btrim(COALESCE(t.alpha_tag, '')) <> ''
		  AND NOT EXISTS (
		      SELECT 1 FROM f
		      WHERE f.system_id = t.system_id AND f.id = t.tgid
		        AND btrim(t.alpha_tag) = ANY (f.alpha_tags))
		RETURNING t.system_id, t.tgid
	`, tgSys, tgIDs, tgTags)
	if err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("keep talkgroup tag edits: %w", err)
	}

	pinned.Units, err = pinUnconfirmedTags(ctx, tx, `
		WITH f AS (
			SELECT e.system_id, e.id, array_agg(btrim(e.alpha_tag)) AS alpha_tags
			FROM unnest($1::int[], $2::int[], $3::text[]) AS e(system_id, id, alpha_tag)
			WHERE btrim(e.alpha_tag) <> ''
			GROUP BY e.system_id, e.id
		)
		UPDATE units u SET alpha_tag_source = 'manual'
		WHERE u.alpha_tag_source = 'csv'
		  AND btrim(COALESCE(u.alpha_tag, '')) <> ''
		  AND NOT EXISTS (
		      SELECT 1 FROM f
		      WHERE f.system_id = u.system_id AND f.id = u.unit_id
		        AND btrim(u.alpha_tag) = ANY (f.alpha_tags))
		RETURNING u.system_id, u.unit_id
	`, unitSys, unitIDs, unitTags)
	if err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("keep unit tag edits: %w", err)
	}

	detail, err := json.Marshal(pinned)
	if err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("encode fixup detail: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE data_fixups SET detail = $2::jsonb WHERE name = $1`,
		PreUpgradeTagEditsFixup, string(detail)); err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("record fixup detail: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PinnedTagEdits{}, false, fmt.Errorf("commit: %w", err)
	}
	return pinned, true, nil
}

// pinUnconfirmedTags runs one of KeepPreUpgradeTagEdits' UPDATE ... RETURNING
// system_id, id statements and returns the changed rows as sorted
// "system_id:id" strings (never nil, so the stored detail lists [] not null).
func pinUnconfirmedTags(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ systemID, id int }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.systemID, &k.id); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].systemID != keys[j].systemID {
			return keys[i].systemID < keys[j].systemID
		}
		return keys[i].id < keys[j].id
	})
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = strconv.Itoa(k.systemID) + ":" + strconv.Itoa(k.id)
	}
	return ids, nil
}

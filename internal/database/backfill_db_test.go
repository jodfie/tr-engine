package database

// Runs against a real PostgreSQL; skipped unless TEST_DATABASE_URL is set (see
// export_units_db_test.go).

import (
	"context"
	"os"
	"testing"
	"time"
)

// ListUntranscribedCalls pages with a keyset cursor, so walking the whole set
// visits every eligible call exactly once, in order, even though none of them
// become transcribed while paging (issue #59).
func TestListUntranscribedCalls_KeysetPaging(t *testing.T) {
	db := emptyDB(t)
	ctx := context.Background()
	schema, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InitSchema(ctx, schema); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO systems (system_id, system_type, name) VALUES (1, 'p25', 'Butler'), (2, 'p25', 'Warren')`)

	now := time.Now().UTC()
	// Microsecond precision so the cursor has to round-trip exactly.
	base := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 123456000, time.UTC)
	insert := func(systemID int, start time.Time, extra string) int64 {
		t.Helper()
		var id int64
		sql := `INSERT INTO calls (system_id, tgid, start_time, duration, audio_file_path` + extra
		if err := db.Pool.QueryRow(ctx, sql, systemID, start).Scan(&id); err != nil {
			t.Fatalf("insert call: %v", err)
		}
		return id
	}
	const eligible = `) VALUES ($1, 100, $2, 5, 'a.m4a') RETURNING call_id`

	want := map[int64]bool{}
	for i := 0; i < 7; i++ {
		// Groups of three share a start time, so ties are broken on call_id.
		want[insert(1, base.Add(time.Duration(i/3)*time.Second), eligible)] = true
	}
	// Not eligible: transcribed, excluded, encrypted, no audio, other system.
	insert(1, base, `, has_transcription) VALUES ($1, 100, $2, 5, 'a.m4a', true) RETURNING call_id`)
	insert(1, base, `, transcription_status) VALUES ($1, 100, $2, 5, 'a.m4a', 'excluded') RETURNING call_id`)
	insert(1, base, `, encrypted) VALUES ($1, 100, $2, 5, 'a.m4a', true) RETURNING call_id`)
	exec(`INSERT INTO calls (system_id, tgid, start_time, duration) VALUES (1, 100, $1, 5)`, base)
	insert(2, base, eligible)

	sys := 1
	filter := BackfillFilter{SystemID: &sys}
	count, err := db.CountUntranscribedCalls(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(want) {
		t.Fatalf("CountUntranscribedCalls = %d, want %d", count, len(want))
	}

	var cursor *UntranscribedCallKey
	var got []UntranscribedCallKey
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatalf("paging did not terminate; got %d keys", len(got))
		}
		page, err := db.ListUntranscribedCalls(ctx, filter, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, k := range page {
			if cursor != nil && !cursor.Before(k) {
				t.Fatalf("key %+v does not come after cursor %+v", k, *cursor)
			}
			k := k
			cursor = &k
			got = append(got, k)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("visited %d calls, want %d: %+v", len(got), len(want), got)
	}
	for _, k := range got {
		if !want[k.CallID] {
			t.Errorf("visited ineligible call %d", k.CallID)
		}
		delete(want, k.CallID)
	}
	if len(want) != 0 {
		t.Errorf("never visited calls %v", want)
	}
}

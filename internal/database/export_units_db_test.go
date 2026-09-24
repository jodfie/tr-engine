package database

// Runs against a real PostgreSQL. Skipped unless TEST_DATABASE_URL points at a
// server where the user may CREATE DATABASE; each subtest creates a throwaway
// database (tr_engine_test_*) and drops it afterwards. Example:
//
//	TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/postgres go test ./internal/database/

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// emptyDB connects to a new, empty throwaway database.
func emptyDB(t *testing.T) *DB {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := fmt.Sprintf("tr_engine_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		admin.Close()
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := Connect(ctx, u.String(), zerolog.Nop())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// ExportUnits runs read-only against databases that may predate the
// alpha_tag_source and recorder/OTA observation migrations.
func TestExportUnits_PreMigrationSchemas(t *testing.T) {
	const base = `system_id int NOT NULL, unit_id int NOT NULL, alpha_tag text,
		first_seen timestamptz, last_seen timestamptz`
	const observations = `, recorder_alpha_tag text, recorder_alpha_tag_seen timestamptz,
		ota_alpha_tag text, ota_alpha_tag_first_seen timestamptz, ota_alpha_tag_last_seen timestamptz`

	cases := []struct {
		name       string
		columns    string
		insert     string
		wantSource string
		wantOTA    string
	}{
		{
			name:       "current schema",
			columns:    base + `, alpha_tag_source text` + observations,
			insert:     `INSERT INTO units (system_id, unit_id, alpha_tag, alpha_tag_source, ota_alpha_tag) VALUES (1, 338, 'FRNSW P338', 'manual', 'P338 FF1')`,
			wantSource: "manual",
			wantOTA:    "P338 FF1",
		},
		{
			name:       "before recorder/OTA columns",
			columns:    base + `, alpha_tag_source text`,
			insert:     `INSERT INTO units (system_id, unit_id, alpha_tag, alpha_tag_source) VALUES (1, 338, 'FRNSW P338', 'manual')`,
			wantSource: "manual",
		},
		{
			name:    "before alpha_tag_source",
			columns: base,
			insert:  `INSERT INTO units (system_id, unit_id, alpha_tag) VALUES (1, 338, 'FRNSW P338')`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := emptyDB(t)
			ctx := context.Background()
			if _, err := db.Pool.Exec(ctx, `CREATE TABLE units (`+tc.columns+`)`); err != nil {
				t.Fatalf("create units: %v", err)
			}
			if _, err := db.Pool.Exec(ctx, tc.insert); err != nil {
				t.Fatalf("insert unit: %v", err)
			}

			units, err := db.ExportUnits(ctx, nil)
			if err != nil {
				t.Fatalf("ExportUnits: %v", err)
			}
			if len(units) != 1 {
				t.Fatalf("got %d units, want 1", len(units))
			}
			u := units[0]
			if u.UnitID != 338 || u.AlphaTag != "FRNSW P338" {
				t.Errorf("unit = %d %q, want 338 %q", u.UnitID, u.AlphaTag, "FRNSW P338")
			}
			if u.AlphaTagSource != tc.wantSource {
				t.Errorf("AlphaTagSource = %q, want %q", u.AlphaTagSource, tc.wantSource)
			}
			if u.OTAAlphaTag != tc.wantOTA {
				t.Errorf("OTAAlphaTag = %q, want %q", u.OTAAlphaTag, tc.wantOTA)
			}
		})
	}
}

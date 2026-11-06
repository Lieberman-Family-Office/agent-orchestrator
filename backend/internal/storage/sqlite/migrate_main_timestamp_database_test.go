package sqlite

import (
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestPrepareBurnedContinuationRepairUsesForkVersion(t *testing.T) {
	for _, burned := range []bool{false, true} {
		t.Run(fmt.Sprintf("burned203=%t", burned), func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 202)
			if burned {
				mustExec(t, db, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (203, 1)`)
			}
			if err := prepareBurnedSchemaRepairs(db); err != nil {
				t.Fatalf("prepare burned schema repairs: %v", err)
			}
			var present int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('conversation_messages') WHERE name = 'continuation'`).Scan(&present); err != nil {
				t.Fatal(err)
			}
			want := 0
			if burned {
				want = 1
			}
			if present != want {
				t.Fatalf("pre-goose continuation columns = %d, want %d; fork 199 must not trigger repair of pending 203", present, want)
			}
			for range 2 {
				if err := migrate(db); err != nil {
					t.Fatalf("migrate after repair preparation: %v", err)
				}
			}
		})
	}
}

// shadowed203FS models upstream's shadowed-199 regression at this fork's
// continuation version (203). Keep 199's interrupt schema intact; recording
// 203 as a no-op leaves continuation absent despite an applied receipt.
func shadowed203FS(t *testing.T) fstest.MapFS {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	legacyFS := fstest.MapFS{}
	for _, entry := range entries {
		name := entry.Name()
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		var version int64
		if _, err := fmt.Sscanf(name, "%d_", &version); err == nil && version == 203 {
			content = []byte("-- +goose Up\nSELECT 1;\n\n-- +goose Down\nSELECT 1;\n")
		}
		legacyFS["migrations/"+name] = &fstest.MapFile{Data: content}
	}
	return legacyFS
}

// openShadowed203Database applies numeric and timestamp migrations with a
// no-op at 203. This models missing continuation schema without suppressing
// the fork's unrelated interrupt migration at 199.
func openShadowed203Database(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", databaseURI(t.TempDir())+noForeignKeysDSN)
	if err != nil {
		t.Fatalf("open shadowed database: %v", err)
	}
	db.SetMaxOpenConns(1)

	gooseMu.Lock()
	goose.SetBaseFS(shadowed203FS(t))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		gooseMu.Unlock()
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		gooseMu.Unlock()
		t.Fatalf("apply legacy (shadowed-203) migrations: %v", err)
	}
	gooseMu.Unlock()

	var applied203 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = 203 AND is_applied = 1`).Scan(&applied203); err != nil {
		t.Fatalf("read version 203 history: %v", err)
	}
	if applied203 != 1 {
		t.Fatalf("setup: version 203 not recorded as applied (%d)", applied203)
	}
	var present int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('conversation_messages') WHERE name = 'continuation'`,
	).Scan(&present); err != nil {
		t.Fatalf("setup: inspect conversation_messages: %v", err)
	}
	if present != 0 {
		t.Fatalf("setup: continuation column already present; the shadowing scenario is not reproduced")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrateUpgradesShadowed203Database preserves upstream's repair coverage
// using this fork's continuation identity. Startup must restore missing schema
// even when goose skips 203 because its version is already recorded.
func TestMigrateUpgradesShadowed203Database(t *testing.T) {
	db := openShadowed203Database(t)

	// The upgrade under test: open the legacy database with this build.
	if err := migrate(db); err != nil {
		t.Fatalf("migrate shadowed-203 database: %v", err)
	}

	// The contract the generated chat-history selects depend on (every
	// conversations.sql select names this column).
	var present int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('conversation_messages') WHERE name = 'continuation'`,
	).Scan(&present); err != nil {
		t.Fatalf("inspect conversation_messages: %v", err)
	}
	if present != 1 {
		t.Fatal("conversation_messages.continuation is missing after repairing shadowed version 203")
	}

	// The generated history select must run without a schema error.
	rows, err := db.Query(`
SELECT id, conversation_id, turn_id, sequence, revision, role, origin, text, streaming,
       provider_item_id, client_message_id, created_at, updated_at, delivery_content_json,
       continuation, sender_session_id, sender_project_id, sender_display_name
FROM conversation_messages`)
	if err != nil {
		t.Fatalf("chat-history select failed after upgrade: %v", err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate chat-history select: %v", err)
	}
}

// TestMigrateUpgradesShadowed203DatabaseIdempotent pins the repair contract:
// repeated migrate passes must keep both fork receipts (199 and 203)
// recorded exactly once with the schema stable.
func TestMigrateUpgradesShadowed203DatabaseIdempotent(t *testing.T) {
	db := openShadowed203Database(t)

	for i := 0; i < 2; i++ {
		if err := migrate(db); err != nil {
			t.Fatalf("migrate pass %d: %v", i+1, err)
		}
	}
	for _, version := range []int64{199, 203} {
		var applied int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1`, version,
		).Scan(&applied); err != nil {
			t.Fatalf("read version history: %v", err)
		}
		if applied != 1 {
			t.Fatalf("version %d recorded %d times after two migrate passes, want exactly 1", version, applied)
		}
	}
	var present int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('conversation_messages') WHERE name = 'continuation'`,
	).Scan(&present); err != nil {
		t.Fatalf("inspect conversation_messages: %v", err)
	}
	if present != 1 {
		t.Fatalf("continuation column missing after two migrate passes")
	}
}

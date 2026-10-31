package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestMigrationIndexesOnlyTurnCompletedProviderEvents covers the partial
// index that keeps the completed-turn authentication lookup off the full
// provider-event archive: it must pick up the rows that predate it, must not
// cover any other method, and must restore the fork index on the way down.
func TestMigrationIndexesOnlyTurnCompletedProviderEvents(t *testing.T) {
	const index = "idx_conversation_provider_events_turn_completed"
	// The archive rows are seeded without their conversation and session parents:
	// the migration only reads the table, so the parent chain adds nothing.
	db := openMigratedDatabaseCopyNoForeignKeys(t, 20261021230230412)
	for i, method := range []string{"reasoning.delta", "turn.completed", "agent_message.delta", "turn.completed", "turn.completed"} {
		mustExec(t, db, `INSERT INTO conversation_provider_events
			(conversation_id, session_id, provider_event_id, method, payload_json, received_at, branch_id)
			VALUES ('c', 's', ?, ?, '{}', CURRENT_TIMESTAMP, 'c:root')`, fmt.Sprintf("event-%d", i), method)
	}
	assertTurnCompletedIndexColumns(t, db, "conversation_id,branch_id")

	upTo(t, db, 20261022144835405)
	assertTurnCompletedIndexColumns(t, db, "branch_id,id")
	for range 2 {
		if err := repairCustom0134MigrationHistory(db); err != nil {
			t.Fatalf("validate upgraded index: %v", err)
		}
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM conversation_provider_events INDEXED BY ` + index + ` WHERE method = 'turn.completed'`,
	).Scan(&count); err != nil || count != 3 {
		t.Fatalf("turn.completed rows reachable through the index = %d, err = %v; want the 3 that predate it", count, err)
	}
	// A partial index cannot answer a query that does not imply its predicate, so
	// SQLite refuses to force it: that refusal is how this proves the other
	// methods were left out of the index.
	err := db.QueryRow(
		`SELECT COUNT(*) FROM conversation_provider_events INDEXED BY ` + index + ` WHERE method = 'reasoning.delta'`,
	).Scan(&count)
	if err == nil || !strings.Contains(err.Error(), "no query solution") {
		t.Fatalf("forcing the index for another method = %v; want a no-query-solution error", err)
	}

	downTo(t, db, 20261021230230412)

	assertTurnCompletedIndexColumns(t, db, "conversation_id,branch_id")
	if err := repairCustom0134MigrationHistory(db); err != nil {
		t.Fatalf("validate restored fork index: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_provider_events`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("archive rows after down migration = %d, err = %v; want all 5", count, err)
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity after downgrade = %q, %v", integrity, err)
	}
}

func assertTurnCompletedIndexColumns(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var columns string
	if err := db.QueryRow(`SELECT COALESCE(group_concat(name, ','), '') FROM (SELECT name FROM pragma_index_info('idx_conversation_provider_events_turn_completed') ORDER BY seqno)`).Scan(&columns); err != nil || columns != want {
		t.Fatalf("turn.completed index columns = %q, err = %v; want %q", columns, err, want)
	}
}

func TestTurnCompletedIndexHistoryValidation(t *testing.T) {
	const bridge = 20261022144835404
	const upstream = 20261022144835405
	const dropIndex = `DROP INDEX idx_conversation_provider_events_turn_completed`
	const upstreamDDL = `CREATE INDEX idx_conversation_provider_events_turn_completed
    ON conversation_provider_events(branch_id, id)
    WHERE method = 'turn.completed'`
	for _, tc := range []struct {
		name    string
		version int64
		damage  string
	}{
		{"fork", 200, ""},
		{"bridge", bridge, ""},
		{"upstream", upstream, ""},
		{"missing fork index", 200, dropIndex},
		{"upstream DDL without receipt", 200, dropIndex + ";" + upstreamDDL},
		{"bridge undone", bridge, `INSERT INTO goose_db_version(version_id,is_applied) VALUES(20261022144835404,0)`},
		{"bridge without fork identity", bridge, `DELETE FROM goose_db_version WHERE version_id=200`},
		{"bridge without project identity", bridge, `DELETE FROM goose_db_version WHERE version_id=198`},
		{"bridge without project schema", bridge, `DROP TRIGGER projects_revision_guard`},
		{"bridge with unreceipted upstream DDL", bridge, upstreamDDL},
		{"upstream undone", upstream, `INSERT INTO goose_db_version(version_id,is_applied) VALUES(20261022144835405,0)`},
		{"upstream without bridge receipt", upstream, `DELETE FROM goose_db_version WHERE version_id=20261022144835404`},
		{"upstream missing index", upstream, dropIndex},
		{"upstream wrong columns", upstream, dropIndex + ";" + strings.Replace(upstreamDDL, "branch_id, id", "conversation_id, branch_id", 1)},
		{"upstream reversed columns", upstream, dropIndex + ";" + strings.Replace(upstreamDDL, "branch_id, id", "id, branch_id", 1)},
		{"upstream wrong predicate", upstream, dropIndex + ";" + strings.Replace(upstreamDDL, "turn.completed", "turn.started", 1)},
		{"upstream missing predicate", upstream, dropIndex + ";" + strings.Split(upstreamDDL, "WHERE")[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, tc.version)
			if tc.damage != "" {
				mustExec(t, db, tc.damage)
			}
			const history = `SELECT group_concat(id || ':' || version_id || ':' || is_applied, ',') FROM (SELECT * FROM goose_db_version ORDER BY id)`
			var before, after string
			if err := db.QueryRow(history).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				err := repairCustom0134MigrationHistory(db)
				if (err != nil) != (tc.damage != "") {
					t.Fatalf("repair error = %v; damaged schema/history = %t", err, tc.damage != "")
				}
			}
			if err := db.QueryRow(history).Scan(&after); err != nil || before != after {
				t.Fatalf("validation changed migration receipts: before=%q after=%q error=%v", before, after, err)
			}
		})
	}
}

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestCustom0134HistoryRepairRollsBackIncompleteSchema(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 189)
	body, err := migrationsFS.ReadFile("migrations/0198_project_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.Split(string(body), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO goose_db_version (version_id,is_applied) VALUES (190,1),(191,1)`,
		`ALTER TABLE conversation_turns ADD COLUMN interrupt_target_provider_turn_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := repairCustom0134MigrationHistory(db); err == nil || !strings.Contains(err.Error(), "custom migration 191 has incomplete or incompatible columns") {
		t.Fatalf("partial interrupt schema was not refused for incomplete columns: %v", err)
	}
	var original, remapped int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM goose_db_version WHERE version_id=190),(SELECT COUNT(*) FROM goose_db_version WHERE version_id=198)`).Scan(&original, &remapped); err != nil || original != 1 || remapped != 0 {
		t.Fatalf("partial repair changed history: original=%d remapped=%d error=%v", original, remapped, err)
	}
}

func TestOfficialAndFreshDatabasesOpenTwice(t *testing.T) {
	for _, version := range []int64{0, 194, 195, 196, 197, 198, 199, 200} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir := t.TempDir()
			if version != 0 {
				db := openMigratedDatabaseCopyAt(t, dir, version, pragmas)
				if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at,config) VALUES('official','fixture',CURRENT_TIMESTAMP,'{"env":{"MCP_PROFILE":"retained"}}')`); err != nil {
					t.Fatal(err)
				}
				if version >= 195 {
					if _, err := db.Exec(`INSERT INTO sessions(id,project_id,num,harness,requested_output,activity_last_at,created_at,updated_at) VALUES('official-1','official',1,'claude-code','retained output',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
						t.Fatal(err)
					}
				}
				if version >= 196 {
					if _, err := db.Exec(`INSERT INTO session_artifact_versions(session_id,path,version,sha256,size,content_type,mtime,created_at) VALUES('official-1','retained.txt',1,'original-sha',7,'text/plain',0,CURRENT_TIMESTAMP)`); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				store, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", databaseURI(dir)+readOnlyPragmas)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var defaults string
			if err := db.QueryRow(`SELECT harness_defaults FROM app_settings`).Scan(&defaults); err != nil || defaults != "{}" {
				t.Fatalf("harness defaults=%q error=%v", defaults, err)
			}
			var columns int
			if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='revision')+(SELECT COUNT(*) FROM pragma_table_info('conversation_turns') WHERE name IN ('interrupt_target_provider_turn_id','interrupt_delivered'))+(SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name IN ('hibernated_at','branch_state','requested_output'))+(SELECT COUNT(*) FROM pragma_table_info('session_artifact_versions'))+(SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_conversation_provider_events_turn_completed')`).Scan(&columns); err != nil || columns != 15 {
				t.Fatalf("columns=%d error=%v", columns, err)
			}
			if version >= 195 {
				var output string
				if err := db.QueryRow(`SELECT requested_output FROM sessions WHERE id='official-1'`).Scan(&output); err != nil || output != "retained output" {
					t.Fatalf("requested output=%q error=%v", output, err)
				}
			}
			if version >= 196 {
				var hash string
				if err := db.QueryRow(`SELECT sha256 FROM session_artifact_versions WHERE session_id='official-1' AND path='retained.txt' AND version=1`).Scan(&hash); err != nil || hash != "original-sha" {
					t.Fatalf("artifact hash=%q error=%v", hash, err)
				}
			}
			if version != 0 {
				var config string
				if err := db.QueryRow(`SELECT config FROM projects WHERE id='official'`).Scan(&config); err != nil || config != `{"env":{"MCP_PROFILE":"retained"}}` {
					t.Fatalf("config=%q error=%v", config, err)
				}
			}
		})
	}
}

func TestOfficial0142UpgradePreservesHarnessDefaults(t *testing.T) {
	for name, damage := range map[string]string{
		"valid":                     "",
		"missing column":            `ALTER TABLE app_settings DROP COLUMN harness_defaults`,
		"wrong type":                `ALTER TABLE app_settings DROP COLUMN harness_defaults; ALTER TABLE app_settings ADD COLUMN harness_defaults INTEGER NOT NULL DEFAULT 0`,
		"missing constraint":        `ALTER TABLE app_settings DROP COLUMN harness_defaults; ALTER TABLE app_settings ADD COLUMN harness_defaults TEXT NOT NULL DEFAULT '{}'`,
		"occupied destination":      `INSERT INTO goose_db_version(version_id,is_applied) VALUES(201,1)`,
		"historical destination":    `INSERT INTO goose_db_version(version_id,is_applied) VALUES(201,0)`,
		"incomplete upstream":       `DROP INDEX idx_session_artifact_versions_sha`,
		"orphaned revision trigger": `CREATE TRIGGER projects_revision_guard BEFORE UPDATE ON projects BEGIN SELECT 1; END`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db := openMigratedDatabaseCopyAt(t, dir, 197, pragmas)
			// Upstream v0.14.2 shipped this unchanged SQL as migration 198.
			applyForkMigration(t, db, "0201_harness_defaults.sql", 198)
			const defaults = `{"claude-code":{"model":"retained-model"}}`
			if _, err := db.Exec(`UPDATE app_settings SET harness_defaults=?`, defaults); err != nil {
				t.Fatal(err)
			}
			if damage != "" {
				if _, err := db.Exec(damage); err != nil {
					t.Fatal(err)
				}
				const history = `SELECT group_concat(id || ':' || version_id || ':' || is_applied, ',') FROM (SELECT * FROM goose_db_version ORDER BY id)`
				var before, after string
				if err := db.QueryRow(history).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if err := repairCustom0134MigrationHistory(db); err == nil {
					t.Fatal("ambiguous upstream migration was accepted")
				}
				if err := db.QueryRow(history).Scan(&after); err != nil || before != after {
					t.Fatalf("refused repair changed migration history: %v", err)
				}
				return
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				upgraded, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", databaseURI(dir)+readOnlyPragmas)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var got string
			if err := db.QueryRow(`SELECT harness_defaults FROM app_settings`).Scan(&got); err != nil || got != defaults {
				t.Fatalf("harness defaults=%q error=%v", got, err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(DISTINCT version_id) FROM goose_db_version WHERE version_id BETWEEN 198 AND 202 AND is_applied=1`).Scan(&count); err != nil || count != 5 {
				t.Fatalf("applied migrations=%d error=%v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='revision'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("fork revision column=%d error=%v", count, err)
			}
		})
	}
}

func TestCustomizedUpgradePreservesSchemaAndQueue(t *testing.T) {
	for _, customVersion := range []int64{190, 191, 195, 196, 197} {
		t.Run(fmt.Sprint(customVersion), func(t *testing.T) {
			dir := t.TempDir()
			base, first := int64(189), int64(190)
			if customVersion >= 195 {
				base, first = 194, 195
			}
			db := openMigratedDatabaseCopyAt(t, dir, base, pragmas)
			for index, name := range []string{"0198_project_revision.sql", "0199_interrupt_and_send.sql", "0200_provider_events_turn_completed_index.sql"} {
				version := first + int64(index)
				if version > customVersion {
					break
				}
				applyForkMigration(t, db, name, version)
			}
			for _, query := range []string{
				`INSERT INTO projects (id,path,registered_at) VALUES ('upgrade','fixture-workspace',CURRENT_TIMESTAMP)`,
				`UPDATE projects SET display_name='retained project' WHERE id='upgrade'`,
				`INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at) VALUES ('upgrade-1','upgrade',1,'claude-code',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
				`INSERT INTO conversations (id,scope,project_id,session_id,current_session_id,created_at,updated_at) VALUES ('conversation','session','upgrade','upgrade-1','upgrade-1',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
				`INSERT INTO conversation_turns (id,conversation_id,handled_by_session_id,state,requested_at) VALUES ('queued','conversation','upgrade-1','queued',CURRENT_TIMESTAMP)`,
				`INSERT INTO conversation_messages (id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at) VALUES ('message','conversation','queued',1,'user','human','  retained prompt  ',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if customVersion == 191 || customVersion >= 196 {
				if _, err := db.Exec(`UPDATE conversation_turns SET interrupt_target_provider_turn_id='retained-target',interrupt_delivered=TRUE WHERE id='queued'`); err != nil {
					t.Fatal(err)
				}
			}
			if customVersion == 197 {
				// A crash after identity repair but before goose must be restartable.
				for range 2 {
					if err := repairCustom0134MigrationHistory(db); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				upgraded, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", databaseURI(dir)+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var state, text, target, integrity string
			var revision int
			var delivered bool
			if err := db.QueryRow(`SELECT t.state,m.text,t.interrupt_target_provider_turn_id,t.interrupt_delivered,p.revision FROM conversation_turns t JOIN conversation_messages m ON m.turn_id=t.id JOIN projects p ON p.id='upgrade' WHERE t.id='queued'`).Scan(&state, &text, &target, &delivered, &revision); err != nil {
				t.Fatal(err)
			}
			if state != "queued" || text != "  retained prompt  " || revision != 1 || ((customVersion == 191 || customVersion >= 196) && (target != "retained-target" || !delivered)) {
				t.Fatalf("data changed: state=%q text=%q revision=%d target=%q delivered=%v", state, text, revision, target, delivered)
			}
			var versions int
			if err := db.QueryRow(`SELECT COUNT(DISTINCT version_id) FROM goose_db_version WHERE version_id IN (190,191,195,196,197,198,199,200) AND is_applied=1`).Scan(&versions); err != nil || versions != 8 {
				t.Fatalf("repaired versions=%d error=%v", versions, err)
			}
			if _, err := db.Exec(`INSERT INTO session_artifact_versions(session_id,path,version,sha256,size,content_type,mtime,created_at) VALUES('upgrade-1','retained.txt',1,'fixture-sha',7,'text/plain',0,CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			for _, column := range []string{"hibernated_at", "branch_state", "requested_output"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name=?`, column).Scan(&count); err != nil || count != 1 {
					t.Fatalf("sessions.%s count=%d error=%v", column, count, err)
				}
			}
			if _, err := db.Exec(`UPDATE projects SET display_name='after upgrade' WHERE id='upgrade'`); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT revision FROM projects WHERE id='upgrade'`).Scan(&revision); err != nil || revision != 2 {
				t.Fatalf("revision=%d error=%v", revision, err)
			}
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity=%q error=%v", integrity, err)
			}
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() || rows.Err() != nil {
				t.Fatal("foreign key validation failed")
			}
		})
	}
}

func applyForkMigration(t *testing.T, db *sql.DB, name string, version int64) {
	t.Helper()
	body, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.Split(string(body), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version); err != nil {
		t.Fatal(err)
	}
}

func TestForkMigrationIdentityRepairRefusesAmbiguousSchema(t *testing.T) {
	for name, query := range map[string]string{
		"missing revision trigger":         `DROP TRIGGER projects_revision_guard`,
		"wrong revision trigger":           `DROP TRIGGER projects_revision_update; CREATE TRIGGER projects_revision_update AFTER UPDATE ON projects BEGIN SELECT 1; END`,
		"partial interrupt columns":        `ALTER TABLE conversation_turns DROP COLUMN interrupt_delivered`,
		"missing index":                    `DROP INDEX idx_conversation_provider_events_turn_completed`,
		"wrong index predicate":            `DROP INDEX idx_conversation_provider_events_turn_completed; CREATE INDEX idx_conversation_provider_events_turn_completed ON conversation_provider_events(conversation_id,branch_id) WHERE method='turn.started'`,
		"competing upstream identity":      `ALTER TABLE sessions ADD COLUMN requested_output TEXT NOT NULL DEFAULT ''`,
		"partial upstream artifact schema": `CREATE TABLE session_artifact_versions(session_id TEXT)`,
		"occupied destination":             `INSERT INTO goose_db_version(version_id,is_applied) VALUES(198,1)`,
		"historical destination":           `INSERT INTO goose_db_version(version_id,is_applied) VALUES(198,0)`,
		"missing project identity":         `DELETE FROM goose_db_version WHERE version_id=195`,
		"missing index identity":           `DELETE FROM goose_db_version WHERE version_id=197`,
	} {
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 194)
			for i, file := range []string{"0198_project_revision.sql", "0199_interrupt_and_send.sql", "0200_provider_events_turn_completed_index.sql"} {
				applyForkMigration(t, db, file, int64(195+i))
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			const history = `SELECT group_concat(id || ':' || version_id || ':' || is_applied, ',') FROM (SELECT * FROM goose_db_version ORDER BY id)`
			var before, after string
			if err := db.QueryRow(history).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := repairCustom0134MigrationHistory(db); err == nil {
				t.Fatal("ambiguous schema was accepted")
			}
			if err := db.QueryRow(history).Scan(&after); err != nil || before != after {
				t.Fatalf("refused repair changed migration history: error=%v", err)
			}
		})
	}
}

func TestUpstreamMigrationIdentityRepairRefusesPartialSchema(t *testing.T) {
	for name, query := range map[string]string{
		"orphaned fork trigger":       `CREATE TRIGGER projects_revision_guard BEFORE UPDATE ON projects BEGIN SELECT 1; END`,
		"missing requested output":    `ALTER TABLE sessions DROP COLUMN requested_output`,
		"wrong requested output type": `ALTER TABLE sessions DROP COLUMN requested_output; ALTER TABLE sessions ADD COLUMN requested_output INTEGER NOT NULL DEFAULT 0`,
		"missing artifact index":      `DROP INDEX idx_session_artifact_versions_sha`,
		"wrong artifact index":        `DROP INDEX idx_session_artifact_versions_sha; CREATE INDEX idx_session_artifact_versions_sha ON session_artifact_versions(session_id,path)`,
		"missing artifact columns":    `ALTER TABLE session_artifact_versions DROP COLUMN size`,
	} {
		t.Run(name, func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, 197)
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if err := repairCustom0134MigrationHistory(db); err == nil {
				t.Fatal("partial upstream schema was accepted")
			}
			var remapped int
			if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id>=198`).Scan(&remapped); err != nil || remapped != 0 {
				t.Fatalf("partial upstream repair changed history: remapped=%d error=%v", remapped, err)
			}
		})
	}
}

func TestInterruptSendUpgradePreservesExistingQueue(t *testing.T) {
	dir := t.TempDir()
	db := openMigratedDatabaseCopyAt(t, dir, 198, pragmas)
	for _, query := range []string{
		`INSERT INTO projects (id,path,registered_at) VALUES ('upgrade','fixture-workspace',CURRENT_TIMESTAMP)`,
		`INSERT INTO sessions (id,project_id,num,harness,activity_last_at,created_at,updated_at) VALUES ('upgrade-1','upgrade',1,'claude-code',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO conversations (id,scope,project_id,session_id,current_session_id,created_at,updated_at) VALUES ('conversation','session','upgrade','upgrade-1','upgrade-1',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO conversation_turns (id,conversation_id,handled_by_session_id,state,requested_at) VALUES ('queued','conversation','upgrade-1','queued',CURRENT_TIMESTAMP)`,
		`INSERT INTO conversation_messages (id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at) VALUES ('message','conversation','queued',1,'user','human','  retained prompt  ',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	verify, err := sql.Open("sqlite", databaseURI(dir)+readOnlyPragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	var state, text, target, integrity string
	var delivered bool
	if err := verify.QueryRow(`SELECT t.state,m.text,t.interrupt_target_provider_turn_id,t.interrupt_delivered FROM conversation_turns t JOIN conversation_messages m ON m.turn_id=t.id WHERE t.id='queued'`).Scan(&state, &text, &target, &delivered); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || text != "  retained prompt  " || target != "" || delivered {
		t.Fatalf("existing queue changed: state=%q text=%q target=%q delivered=%v", state, text, target, delivered)
	}
	if err := verify.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	rows, err := verify.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("foreign key validation failed")
	}
}

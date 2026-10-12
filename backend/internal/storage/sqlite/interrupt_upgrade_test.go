package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestCustom0134HistoryRepairRollsBackIncompleteSchema(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 189)
	body, err := migrationsFS.ReadFile("migrations/0195_project_revision.sql")
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
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM goose_db_version WHERE version_id=190),(SELECT COUNT(*) FROM goose_db_version WHERE version_id=195)`).Scan(&original, &remapped); err != nil || original != 1 || remapped != 0 {
		t.Fatalf("partial repair changed history: original=%d remapped=%d error=%v", original, remapped, err)
	}
}

func TestOfficial0137AndFreshDatabasesOpenTwice(t *testing.T) {
	for _, version := range []int64{0, 194} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir := t.TempDir()
			if version != 0 {
				db := openMigratedDatabaseCopyAt(t, dir, version, pragmas)
				if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at,config) VALUES('official','fixture',CURRENT_TIMESTAMP,'{"env":{"MCP_PROFILE":"retained"}}')`); err != nil {
					t.Fatal(err)
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
			var columns int
			if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='revision')+(SELECT COUNT(*) FROM pragma_table_info('conversation_turns') WHERE name IN ('interrupt_target_provider_turn_id','interrupt_delivered'))+(SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name IN ('hibernated_at','branch_state'))`).Scan(&columns); err != nil || columns != 5 {
				t.Fatalf("columns=%d error=%v", columns, err)
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

func TestCustomized0134UpgradePreservesSchemaAndQueue(t *testing.T) {
	for _, customVersion := range []int64{190, 191} {
		t.Run(fmt.Sprint(customVersion), func(t *testing.T) {
			dir := t.TempDir()
			db := openMigratedDatabaseCopyAt(t, dir, 189, pragmas)
			for index, name := range []string{"0195_project_revision.sql", "0196_interrupt_and_send.sql"} {
				version := int64(190 + index)
				if version > customVersion {
					break
				}
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
			if customVersion == 191 {
				if _, err := db.Exec(`UPDATE conversation_turns SET interrupt_target_provider_turn_id='retained-target',interrupt_delivered=TRUE WHERE id='queued'`); err != nil {
					t.Fatal(err)
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
			if state != "queued" || text != "  retained prompt  " || revision != 1 || (customVersion == 191 && (target != "retained-target" || !delivered)) {
				t.Fatalf("data changed: state=%q text=%q revision=%d target=%q delivered=%v", state, text, revision, target, delivered)
			}
			for _, column := range []string{"hibernated_at", "branch_state"} {
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

func TestInterruptSendUpgradePreservesExistingQueue(t *testing.T) {
	dir := t.TempDir()
	db := openMigratedDatabaseCopyAt(t, dir, 195, pragmas)
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

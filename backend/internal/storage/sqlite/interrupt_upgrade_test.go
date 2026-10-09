package sqlite

import (
	"database/sql"
	"testing"
)

func TestInterruptSendUpgradePreservesExistingQueue(t *testing.T) {
	dir := t.TempDir()
	db := openMigratedDatabaseCopyAt(t, dir, 190, pragmas)
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

package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestRepairProjectRevisionWritesRemainGuarded(t *testing.T) {
	for _, recursive := range []string{"OFF", "ON"} {
		for _, unexpected := range []bool{false, true} {
			name := recursive + "/normal"
			if unexpected {
				name = recursive + "/unexpected"
			}
			t.Run(name, func(t *testing.T) {
				db := openMigratedTestDB(t)
				const legacy = "2026-10-21 09:00:00 +0530 IST"
				if _, err := db.Exec("PRAGMA recursive_triggers = " + recursive); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at,archived_at,revision)
VALUES('revision-repair','fixture-project',?,?,7);
CREATE TABLE timestamp_unrelated(value INTEGER);
INSERT INTO timestamp_unrelated VALUES(0);`, legacy, legacy); err != nil {
					t.Fatal(err)
				}
				if unexpected {
					if _, err := db.Exec(`CREATE TRIGGER unexpected_project_write AFTER UPDATE OF registered_at ON projects
BEGIN UPDATE timestamp_unrelated SET value=value+1; END`); err != nil {
						t.Fatal(err)
					}
				}
				var beforeLog string
				const logState = `SELECT json_array((SELECT count(*) FROM change_log),(SELECT seq FROM sqlite_sequence WHERE name='change_log'))`
				if err := db.QueryRow(logState).Scan(&beforeLog); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					err := repairTimestamps(context.Background(), db)
					wantTime, wantRevision := "2026-10-21 03:30:00 +0000 UTC", int64(8)
					if unexpected {
						if err == nil || !strings.Contains(err.Error(), "unexpected timestamp repair writes") {
							t.Fatalf("unexpected project write accepted: %v", err)
						}
						wantTime, wantRevision = legacy, 7
					} else if err != nil {
						t.Fatal(err)
					}
					var registered, archived, path, afterLog string
					var revision, unrelated, paused int64
					if err := db.QueryRow(`SELECT CAST(registered_at AS TEXT),CAST(archived_at AS TEXT),path,revision,
(SELECT value FROM timestamp_unrelated),(SELECT paused FROM timestamp_repair_control WHERE id=1)
FROM projects WHERE id='revision-repair'`).Scan(&registered, &archived, &path, &revision, &unrelated, &paused); err != nil {
						t.Fatal(err)
					}
					if registered != wantTime || archived != wantTime || path != "fixture-project" || revision != wantRevision || unrelated != 0 || paused != 0 {
						t.Fatalf("repair state: registered=%q archived=%q path=%q revision=%d unrelated=%d paused=%d", registered, archived, path, revision, unrelated, paused)
					}
					if err := db.QueryRow(logState).Scan(&afterLog); err != nil || afterLog != beforeLog {
						t.Fatalf("repair changed change-log state: %s -> %s: %v", beforeLog, afterLog, err)
					}
				}
				if _, err := db.Exec(`UPDATE projects SET revision=0 WHERE id='revision-repair'`); err == nil {
					t.Fatal("repair disabled the project revision guard")
				}
			})
		}
	}
}

func TestRepairProjectAndSessionNumericOffsetColumns(t *testing.T) {
	db := openMigratedTestDB(t)
	loc, err := time.LoadLocation("Asia/Colombo")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 18, 15, 40, 123456789, loc)
	stored := at.String() + " m=+1.234"
	if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at,archived_at)
VALUES('legacy','/tmp/legacy',?,?);
INSERT INTO sessions(id,num,harness,activity_state,activity_last_at,is_terminated,created_at,updated_at,native_identity_observed_at,first_signal_at)
VALUES('legacy-session',1,'aider','idle',CURRENT_TIMESTAMP,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,?,?);`, stored, stored, stored, stored); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT registered_at FROM projects WHERE id='legacy'`,
		`SELECT archived_at FROM projects WHERE id='legacy'`,
		`SELECT native_identity_observed_at FROM sessions WHERE id='legacy-session'`,
		`SELECT first_signal_at FROM sessions WHERE id='legacy-session'`,
	}
	for _, query := range queries {
		if err := canScanTime(db, query); err == nil {
			t.Fatalf("fixture is readable before repair: %s", query)
		}
	}
	for range 2 {
		if err := repairTimestamps(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		for _, query := range queries {
			var got time.Time
			if err := db.QueryRow(query).Scan(&got); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			if !got.Equal(at) || got.Location() != time.UTC {
				t.Fatalf("%s: got %v, want %v", query, got, at.UTC())
			}
		}
	}
	for _, query := range []string{
		`SELECT CAST(registered_at AS TEXT),CAST(archived_at AS TEXT) FROM projects WHERE id='legacy'`,
		`SELECT CAST(native_identity_observed_at AS TEXT),CAST(first_signal_at AS TEXT) FROM sessions WHERE id='legacy-session'`,
	} {
		var first, second string
		if err := db.QueryRow(query).Scan(&first, &second); err != nil {
			t.Fatal(err)
		}
		if first != at.UTC().String() || second != at.UTC().String() {
			t.Fatalf("repair stored %q, %q; want %q", first, second, at.UTC().String())
		}
	}
}

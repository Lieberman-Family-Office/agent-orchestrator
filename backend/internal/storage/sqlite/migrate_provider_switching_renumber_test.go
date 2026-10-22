package sqlite

import (
	"fmt"
	"testing"
)

func TestMigrateMovesProviderSwitchingFromItsBranchNumbers(t *testing.T) {
	for _, old := range []int64{196, 199, 200, 201, 216} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			db := openMigratedDatabaseCopy(t, old-1)
			applyForkMigration(t, db, "20261020131539535_provider_switching.sql", old)
			var receipt int64
			if err := db.QueryRow(`SELECT id FROM goose_db_version WHERE version_id = ?`, old).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := migrate(db); err != nil {
					t.Fatalf("migrate a database that ran provider switching as %d: %v", old, err)
				}
				var atOld, atNew int
				if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ?`, old).Scan(&atOld); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ? AND is_applied = 1 AND id = ?`, providerSwitchingVersion, receipt).Scan(&atNew); err != nil {
					t.Fatal(err)
				}
				wantOld := 1 // The fork's shipped migration must now own this number.
				if old == 216 {
					wantOld = 0
				}
				if atOld != wantOld || atNew != 1 {
					t.Fatalf("versions %d=%d new=%d, want %d and 1 with the original receipt", old, atOld, atNew, wantOld)
				}
				var defaults string
				if err := db.QueryRow(`SELECT harness_defaults FROM app_settings WHERE id = 1`).Scan(&defaults); err != nil || defaults != "{}" {
					t.Fatalf("fork harness defaults were skipped: %q, %v", defaults, err)
				}
				if err := repairCustom0134MigrationHistory(db); err != nil {
					t.Fatalf("fork schema or receipts changed: %v", err)
				}
			}
		})
	}
}

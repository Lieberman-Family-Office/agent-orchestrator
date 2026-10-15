package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Break caught: ReconcileConversationAuthentication runs this query under the
// writer lock after every completed chat turn. Without the partial
// turn.completed index its EXISTS probe walks every provider event on the
// active branch.
func TestSelectVerifiedAuthenticationTurnUsesTurnCompletedIndex(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s, err := sqlite.Open(dataDir) // production open + migrate
	mustNoError(t, err)
	mustNoError(t, s.Close())
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	// Run the generated method once so the plan covers the exact SQL and args
	// production sends, not a hand copy of the query.
	capture := &queryCapture{DBTX: raw}
	_, err = gen.New(capture).SelectVerifiedAuthenticationTurn(ctx, gen.SelectVerifiedAuthenticationTurnParams{
		ConversationID: "conv", Generation: "gen-1", ProviderTurnID: "turn-1",
		AfterAt: sql.NullTime{Time: time.Unix(1700000000, 0).UTC(), Valid: true},
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("run generated query on empty database: %v", err)
	}

	rows, err := raw.QueryContext(ctx, "EXPLAIN QUERY PLAN "+capture.query, capture.args...)
	mustNoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		mustNoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	mustNoError(t, rows.Err())
	const want = "SEARCH e USING INDEX idx_conversation_provider_events_turn_completed (conversation_id=? AND branch_id=?)"
	if !slices.Contains(plan, want) {
		t.Fatalf("alias e does not search the turn.completed index; want %q in plan:\n%s", want, strings.Join(plan, "\n"))
	}
}

func TestSelectConversationTurnsUsesLineageIndexes(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s, err := sqlite.Open(dataDir)
	mustNoError(t, err)
	mustNoError(t, s.Close())
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	mustNoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	capture := &queryCapture{DBTX: raw}
	_, err = gen.New(capture).SelectConversationTurns(ctx, "conv")
	mustNoError(t, err)
	rows, err := raw.QueryContext(ctx, "EXPLAIN QUERY PLAN "+capture.query, capture.args...)
	mustNoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		mustNoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	mustNoError(t, rows.Err())
	for _, table := range []string{"messages", "activities"} {
		want := "USING COVERING INDEX idx_conversation_" + table + "_turn_sequence (turn_id=? AND sequence<?)"
		if !strings.Contains(strings.Join(plan, "\n"), want) {
			t.Errorf("lineage query does not use %s:\n%s", want, strings.Join(plan, "\n"))
		}
	}
}

// queryCapture records the SQL text and args a generated query sends.
type queryCapture struct {
	gen.DBTX
	query string
	args  []any
}

func (c *queryCapture) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.query, c.args = query, args
	return c.DBTX.QueryRowContext(ctx, query, args...)
}

func (c *queryCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.query, c.args = query, args
	return c.DBTX.QueryContext(ctx, query, args...)
}

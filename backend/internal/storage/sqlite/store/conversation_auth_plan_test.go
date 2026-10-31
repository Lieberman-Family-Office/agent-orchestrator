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

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
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
	const want = "SEARCH e USING INDEX idx_conversation_provider_events_turn_completed (branch_id=?)"
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
		want := "USING INDEX idx_conversation_" + table + "_turn_sequence (turn_id=? AND sequence<?)"
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

// planRecorder explains every single-row query before running it. A test can
// then assert on the plan of the statement sqlc compiled in, instead of on a
// hand-copied duplicate of its text that keeps passing after the real query
// drifts.
type planRecorder struct {
	gen.DBTX
	t     *testing.T
	plans []string
}

func (r *planRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.t.Helper()
	r.plans = append(r.plans, r.explain(ctx, query, args)...)
	return r.DBTX.QueryRowContext(ctx, query, args...)
}

func (r *planRecorder) explain(ctx context.Context, query string, args []any) []string {
	r.t.Helper()
	rows, err := r.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		r.t.Fatalf("explain query plan: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			r.t.Fatalf("scan query plan row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatalf("read query plan: %v", err)
	}
	return details
}

// Break caught: SelectVerifiedAuthenticationTurn runs inside the projection
// transaction of every completed turn, and its EXISTS found the turn.completed
// event by walking every archive row of the branch through
// idx_conversation_provider_events_branch and reading each row to test method.
// On a long conversation that scan held the single writer for seconds.
func TestVerifiedAuthenticationTurnLookupSearchesOnlyTurnCompletedArchiveRows(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	seedProject(t, s, "plan")
	record := sampleRecord("plan")
	record.Mode = domain.SessionModeChat
	session, err := s.CreateSession(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(ctx, "plan-conv", domain.ConversationScopeSession, "plan", session.ID, histClock)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimChatControllerGeneration(ctx, session.ID, "gen-1"); err != nil {
		t.Fatal(err)
	}
	startAt := histClock.Add(time.Second)
	if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "gen-1", "turn", "provider-turn", startAt); err != nil {
		t.Fatal(err)
	}
	// A streamed delta sits in the archive next to the completion, as it does in
	// every real turn; the lookup has to find the completion among them.
	if _, err := s.ProjectProviderEvent(ctx, conversation.ID, session.ID, "gen-1", "delta", "reasoning.delta", `{"providerTurnId":"provider-turn"}`, startAt, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	completedAt := startAt.Add(time.Second)
	if _, err := s.ProjectProviderEvent(ctx, conversation.ID, session.ID, "gen-1", "completion", "turn.completed", `{"providerTurnId":"provider-turn","turnState":"completed"}`, completedAt, func(txCtx context.Context) error {
		return s.SettleTurn(txCtx, conversation.ID, "provider-turn", domain.TurnStateCompleted, "", completedAt)
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db"))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	recorder := &planRecorder{DBTX: raw, t: t}
	turn, err := gen.New(recorder).SelectVerifiedAuthenticationTurn(ctx, gen.SelectVerifiedAuthenticationTurnParams{
		ConversationID: conversation.ID,
		Generation:     "gen-1",
		ProviderTurnID: "provider-turn",
		AfterAt:        sql.NullTime{Time: histClock, Valid: true},
	})
	if err != nil {
		t.Fatalf("select verified authentication turn: %v", err)
	}
	if turn.ProviderTurnID != "provider-turn" {
		t.Fatalf("verified turn = %+v, want provider-turn", turn)
	}

	plan := strings.Join(recorder.plans, "\n")
	if !strings.Contains(plan, "USING INDEX idx_conversation_provider_events_turn_completed (branch_id=?)") {
		t.Errorf("archive lookup does not search the turn.completed index:\n%s", plan)
	}
	if strings.Contains(plan, "idx_conversation_provider_events_branch") || strings.Contains(plan, "SCAN e") {
		t.Errorf("archive lookup walks the branch's whole provider-event archive:\n%s", plan)
	}
}

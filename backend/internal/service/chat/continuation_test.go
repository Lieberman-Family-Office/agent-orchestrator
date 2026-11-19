package chat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPrepareContinuationReadFailureDoesNotSendWithoutContext(t *testing.T) {
	readError := errors.New("conversation unavailable")
	controller := Controller{continuationReader: SnapshotReaderFunc(func(context.Context, string) (ConversationRows, error) {
		return ConversationRows{}, readError
	})}
	message := ports.ChatUserMessage{Text: "Continue from where you stopped.", Continuation: true}
	got, err := controller.prepareContinuation(context.Background(), message)
	if !errors.Is(err, readError) || got.Text != message.Text {
		t.Fatalf("unavailable context should return the read error without changing the request: %#v, %v", got, err)
	}
}

func continuationRows() ConversationRows {
	return ConversationRows{
		Turns: []domain.ConversationTurn{
			{ID: "old", State: domain.TurnStateCompleted, ProviderTurnID: "p-old"},
			{ID: "task", State: domain.TurnStateInterrupted, ProviderTurnID: "p-task"},
		},
		Messages: []domain.ConversationMessage{
			{ID: "old-user", TurnID: "old", Sequence: 1, Role: domain.MessageRoleUser, Text: "An unrelated old task"},
			{ID: "old-answer", TurnID: "old", Sequence: 2, Role: domain.MessageRoleAssistant, Text: "Unrelated old answer"},
			{ID: "task-user", TurnID: "task", Sequence: 3, Role: domain.MessageRoleUser, Text: "Write 30 AO tips without tools; end with TASK_DONE"},
			{ID: "task-answer", TurnID: "task", Sequence: 4, Role: domain.MessageRoleAssistant, Text: "1. Use separate worktrees.\n2. Keep the"},
		},
	}
}

func TestStoppedContinuationContextUsesCurrentTask(t *testing.T) {
	rows := continuationRows()
	reminder := stoppedContinuationContext(rows)
	for _, want := range []string{rows.Messages[2].Text, "Keep the", "avoid repeating completed work", "check current state"} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("context omits %q: %s", want, reminder)
		}
	}
	if strings.Contains(reminder, "unrelated") || strings.Contains(reminder, "Unrelated") {
		t.Fatalf("context includes an older task: %s", reminder)
	}
}

func TestStoppedContinuationContextRepeatedStopsKeepOriginalRequest(t *testing.T) {
	rows := continuationRows()
	rows.Turns = append(rows.Turns, domain.ConversationTurn{ID: "continued", State: domain.TurnStateInterrupted, ProviderTurnID: "p-continued"})
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "continued-user", TurnID: "continued", Sequence: 5, Role: domain.MessageRoleUser, Text: "PREVIOUS_ENRICHED_CONTINUE_CONTEXT", Continuation: true},
		domain.ConversationMessage{ID: "continued-answer", TurnID: "continued", Sequence: 6, Role: domain.MessageRoleAssistant, Text: "3. Keep each branch"},
	)
	reminder := stoppedContinuationContext(rows)
	if !strings.Contains(reminder, rows.Messages[2].Text) || !strings.Contains(reminder, "Keep each branch") ||
		strings.Contains(reminder, "PREVIOUS_ENRICHED_CONTINUE_CONTEXT") {
		t.Fatalf("repeated continue lost its original task or nested old context: %s", reminder)
	}
}

func TestStoppedContinuationContextRetainsHumanSteeringAcrossStops(t *testing.T) {
	rows := continuationRows()
	rows.Turns = append(rows.Turns, domain.ConversationTurn{ID: "continued", State: domain.TurnStateInterrupted, ProviderTurnID: "p-continued"})
	rows.Messages = append(rows.Messages, domain.ConversationMessage{
		ID: "continue-user", TurnID: "continued", Sequence: 7, Role: domain.MessageRoleUser, Text: "Continue", Continuation: true,
	})
	rows.Activities = []domain.ConversationActivity{
		{TurnID: "old", Sequence: 2, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"OLD_GUIDANCE","origin":"human"}`)},
		{TurnID: "task", Sequence: 5, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"Check every worker; do not authorize new work.","origin":"human"}`)},
		{TurnID: "task", Sequence: 6, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"WORKER_GUIDANCE","origin":"automation","senderSessionId":"worker-1"}`)},
		{TurnID: "continued", Sequence: 8, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"Report delivery and actual replies separately.","origin":"human"}`)},
		{TurnID: "continued", Sequence: 9, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"SPOOFED_GUIDANCE","origin":"human","senderSessionId":"worker-2"}`)},
		{TurnID: "withdrawn", Sequence: 10, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","text":"WITHDRAWN_GUIDANCE","origin":"human"}`)},
	}
	reminder := stoppedContinuationContext(rows)
	for _, want := range []string{rows.Messages[2].Text, "Check every worker; do not authorize new work.", "Report delivery and actual replies separately."} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("Continue lost human guidance %q: %s", want, reminder)
		}
	}
	for _, unwanted := range []string{"OLD_GUIDANCE", "WORKER_GUIDANCE", "SPOOFED_GUIDANCE", "WITHDRAWN_GUIDANCE"} {
		if strings.Contains(reminder, unwanted) {
			t.Fatalf("Continue misattributed guidance %q: %s", unwanted, reminder)
		}
	}
}

func TestPrepareContinuationRefusesOversizedHumanGuidance(t *testing.T) {
	rows := continuationRows()
	for i := range 2 {
		detail, err := json.Marshal(map[string]string{
			"event": "steer", "origin": "human", "text": strings.Repeat("g", continuationTextBudget/2),
		})
		if err != nil {
			t.Fatal(err)
		}
		rows.Activities = append(rows.Activities, domain.ConversationActivity{
			TurnID: "task", Sequence: int64(5 + i), Kind: domain.ActivityKindSystem, Detail: detail,
		})
	}
	controller := Controller{continuationReader: SnapshotReaderFunc(func(context.Context, string) (ConversationRows, error) {
		return rows, nil
	})}
	request := ports.ChatUserMessage{Text: "Continue", Continuation: true}
	if _, err := controller.prepareContinuation(context.Background(), request); err != nil {
		t.Fatalf("guidance at the budget was refused: %v", err)
	}
	rows.Activities = append(rows.Activities, domain.ConversationActivity{
		TurnID: "task", Sequence: 7, Kind: domain.ActivityKindSystem,
		Detail: []byte(`{"event":"steer","origin":"human","text":"g"}`),
	})
	got, err := controller.prepareContinuation(context.Background(), request)
	if !errors.Is(err, ErrContinuationUnavailable) || len(got.Content) != 0 {
		t.Fatalf("oversized guidance started a turn with dropped constraints: %#v, %v", got, err)
	}
}

func TestStoppedContinuationContextIgnoresWithdrawnWork(t *testing.T) {
	rows := continuationRows()
	rolledBack := time.Now()
	rows.Turns = append(rows.Turns,
		domain.ConversationTurn{ID: "queued", State: domain.TurnStateInterrupted},
		domain.ConversationTurn{ID: "cancelled", State: domain.TurnStateCancelled},
		domain.ConversationTurn{ID: "undone", State: domain.TurnStateCompleted, ProviderTurnID: "p-undone", RolledBackAt: &rolledBack},
	)
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "undone-message", TurnID: "undone", Sequence: 7, Role: domain.MessageRoleAssistant, Text: "UNDONE_RESPONSE"},
		domain.ConversationMessage{ID: "queued-message", TurnID: "queued", Sequence: 8, Role: domain.MessageRoleUser, Text: "UNDISPATCHED_TASK"},
		domain.ConversationMessage{ID: "late-old-answer", TurnID: "old", Sequence: 9, Role: domain.MessageRoleAssistant, Text: "LATE_OLD_RESPONSE"},
	)
	reminder := stoppedContinuationContext(rows)
	if reminder == "" || strings.Contains(reminder, "UNDONE_RESPONSE") || strings.Contains(reminder, "LATE_OLD_RESPONSE") || strings.Contains(reminder, "UNDISPATCHED_TASK") {
		t.Fatalf("withdrawn work changed continuation context: %s", reminder)
	}
}

func TestStoppedContinuationContextDoesNotResurrectOlderStop(t *testing.T) {
	for _, state := range []domain.TurnState{domain.TurnStateCompleted, domain.TurnStateFailed, domain.TurnStateRunning, domain.TurnStateQueued} {
		t.Run(string(state), func(t *testing.T) {
			rows := continuationRows()
			rows.Turns = append(rows.Turns, domain.ConversationTurn{ID: "new", State: state, ProviderTurnID: "p-new"})
			if got := stoppedContinuationContext(rows); got != "" {
				t.Fatalf("new %s task resurrected older stop: %s", state, got)
			}
		})
	}
}

func TestStoppedContinuationContextProviderStartedTurn(t *testing.T) {
	rows := continuationRows()
	rows.Turns[1].State = domain.TurnStateCompleted
	rows.Turns = append(rows.Turns, domain.ConversationTurn{ID: "goal", State: domain.TurnStateInterrupted, ProviderTurnID: "p-goal"})
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "goal-answer", TurnID: "goal", Sequence: 5, Role: domain.MessageRoleAssistant, Text: "GOAL_PROGRESS"},
	)
	reminder := stoppedContinuationContext(rows)
	if !strings.Contains(reminder, "TASK_DONE") || !strings.Contains(reminder, "GOAL_PROGRESS") || strings.Contains(reminder, "unrelated") {
		t.Fatalf("provider-started turn should continue the latest real request: %s", reminder)
	}
}

func TestStoppedContinuationContextBeforeOutput(t *testing.T) {
	rows := continuationRows()
	rows.Messages = rows.Messages[:3]
	reminder := stoppedContinuationContext(rows)
	if !strings.Contains(reminder, "TASK_DONE") || strings.Contains(reminder, "partialResponseTail") {
		t.Fatalf("stop before output should retain the request without inventing a response: %s", reminder)
	}
}

func TestStoppedContinuationContextBoundsUnicodeAndOmitsPrivateActivity(t *testing.T) {
	rows := continuationRows()
	rows.Messages[2].Text = "REQUEST_START" + strings.Repeat("界", 20000) + "REQUEST_END"
	rows.Messages[3].Text = "RESPONSE_START" + strings.Repeat("界", 20000) + "RESPONSE_END"
	rows.Activities = []domain.ConversationActivity{
		{TurnID: "task", Kind: domain.ActivityKindReasoning, Summary: "PRIVATE_REASONING"},
		{TurnID: "task", Kind: domain.ActivityKindCommand, Summary: "COMMAND_TO_REPLAY"},
	}
	reminder := stoppedContinuationContext(rows)
	for _, want := range []string{"REQUEST_START", "REQUEST_END", "RESPONSE_END", "requestTruncated", "responseTailTruncated"} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("bounded context omits %q", want)
		}
	}
	if !utf8.ValidString(reminder) || len(reminder) > 20*1024 {
		t.Fatalf("context is invalid UTF-8 or oversized: %d bytes", len(reminder))
	}
	for _, unwanted := range []string{"RESPONSE_START", "PRIVATE_REASONING", "COMMAND_TO_REPLAY"} {
		if strings.Contains(reminder, unwanted) {
			t.Fatalf("context retained %q", unwanted)
		}
	}
}

func TestContinuableTurnIDSurvivesProviderScopeChange(t *testing.T) {
	rows := continuationRows()
	started := time.Now()
	rows.Turns[1].StartedAt, rows.Turns[1].ProviderTurnID = &started, ""
	if ContinuableTurnID(rows.Turns) != "task" || !strings.Contains(stoppedContinuationContext(rows), "TASK_DONE") {
		t.Fatal("provider scope change lost the dispatched stopped task")
	}
	rows.Turns[1].State = domain.TurnStateCompleted
	rows.Turns = append(rows.Turns, domain.ConversationTurn{ID: "swept", State: domain.TurnStateInterrupted})
	if ContinuableTurnID(rows.Turns) != "" || stoppedContinuationContext(rows) != "" {
		t.Fatal("a swept queue resurrected completed work")
	}
}

func TestContinuationDispatchOrder(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	at := func(seconds int) *time.Time {
		value := base.Add(time.Duration(seconds) * time.Second)
		return &value
	}
	for _, tc := range []struct {
		name        string
		legacy      bool
		scopeChange bool
		continued   bool
		provider    bool
	}{
		{name: "priority overtakes queued task"},
		{name: "legacy task without start", legacy: true},
		{name: "provider scope cleared", scopeChange: true},
		{name: "continuation chain", continued: true},
		{name: "provider continuation", provider: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := ConversationRows{
				Turns: []domain.ConversationTurn{
					{ID: "a", State: domain.TurnStateInterrupted, RequestedAt: *at(0), StartedAt: at(0), ProviderTurnID: "p-a"},
					{ID: "b", State: domain.TurnStateInterrupted, RequestedAt: *at(1), StartedAt: at(4), ProviderTurnID: "p-b"},
					{ID: "c", State: domain.TurnStateCompleted, RequestedAt: *at(2), StartedAt: at(3), ProviderTurnID: "p-c"},
					{ID: "swept", State: domain.TurnStateInterrupted, RequestedAt: *at(5), CompletedAt: at(6)},
					{ID: "undone", State: domain.TurnStateCompleted, StartedAt: at(7), RolledBackAt: at(8)},
				},
				Messages: []domain.ConversationMessage{
					{ID: "a-user", TurnID: "a", Sequence: 1, Role: domain.MessageRoleUser, Text: "UNRELATED_A"},
					{ID: "b-user", TurnID: "b", Sequence: 2, Role: domain.MessageRoleUser, Text: "ORIGINAL_B"},
					{ID: "c-user", TurnID: "c", Sequence: 3, Role: domain.MessageRoleUser, Text: "UNRELATED_C"},
					{ID: "c-answer", TurnID: "c", Sequence: 4, Role: domain.MessageRoleAssistant, Text: "UNRELATED_C_RESPONSE"},
					{ID: "b-answer", TurnID: "b", Sequence: 5, Role: domain.MessageRoleAssistant, Text: "PARTIAL_B"},
					{ID: "swept-user", TurnID: "swept", Sequence: 6, Role: domain.MessageRoleUser, Text: "UNRELATED_SWEPT"},
				},
				Activities: []domain.ConversationActivity{
					{TurnID: "c", Sequence: 7, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"UNRELATED_C_GUIDANCE"}`)},
					{TurnID: "b", Sequence: 8, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"B_GUIDANCE"}`)},
				},
			}
			if tc.legacy {
				// Legacy rows use RequestedAt, not a missing timestamp treated as newest.
				rows.Turns[1].StartedAt = nil
				rows.Turns[1].RequestedAt = *at(4)
			}
			if tc.scopeChange {
				for i := range rows.Turns {
					rows.Turns[i].ProviderTurnID = ""
				}
			}
			wantID := "b"
			if tc.continued || tc.provider {
				wantID = "continued"
				rows.Turns = append(rows.Turns, domain.ConversationTurn{
					ID: wantID, State: domain.TurnStateInterrupted, RequestedAt: *at(9), StartedAt: at(9),
				})
				if tc.continued {
					rows.Messages = append(rows.Messages, domain.ConversationMessage{
						ID: "continue-user", TurnID: wantID, Sequence: 9, Role: domain.MessageRoleUser, Text: "Continue", Continuation: true,
					})
				}
				rows.Messages = append(rows.Messages, domain.ConversationMessage{
					ID: "continue-answer", TurnID: wantID, Sequence: 10, Role: domain.MessageRoleAssistant, Text: "MORE_B",
				})
				rows.Activities = append(rows.Activities, domain.ConversationActivity{
					TurnID: wantID, Sequence: 11, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"NEW_B_GUIDANCE"}`),
				})
			}
			if got := ContinuableTurnID(rows.Turns); got != wantID {
				t.Fatalf("continuable turn = %q, want %q", got, wantID)
			}
			reminder := stoppedContinuationContext(rows)
			for _, want := range []string{"ORIGINAL_B", "PARTIAL_B", "B_GUIDANCE"} {
				if !strings.Contains(reminder, want) {
					t.Fatalf("missing %q in context: %s", want, reminder)
				}
			}
			if strings.Contains(reminder, "UNRELATED") {
				t.Fatalf("dispatch order mixed tasks: %s", reminder)
			}
			if (tc.continued || tc.provider) && (!strings.Contains(reminder, "MORE_B") || !strings.Contains(reminder, "NEW_B_GUIDANCE")) {
				t.Fatalf("continuation lost new response or guidance: %s", reminder)
			}
			if rows.Turns[1].ID != "b" || rows.Turns[2].ID != "c" {
				t.Fatal("continuation reordered the caller's snapshot")
			}
		})
	}
}

func TestContinuableTurnIDChronologyBarriersAndTies(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	later := base.Add(time.Second)
	for _, tc := range []struct {
		name  string
		turns []domain.ConversationTurn
		want  string
	}{
		{
			name: "later dispatched completion blocks earlier stopped priority",
			turns: []domain.ConversationTurn{
				{ID: "completed", State: domain.TurnStateCompleted, StartedAt: &later},
				{ID: "stopped", State: domain.TurnStateInterrupted, StartedAt: &base},
			},
		},
		{
			name: "legacy completion still blocks older stop",
			turns: []domain.ConversationTurn{
				{ID: "stopped", State: domain.TurnStateInterrupted, StartedAt: &base},
				{ID: "completed", State: domain.TurnStateCompleted, RequestedAt: later},
			},
		},
		{
			name: "equal starts preserve snapshot order",
			turns: []domain.ConversationTurn{
				{ID: "completed", State: domain.TurnStateCompleted, StartedAt: &base, CompletedAt: &later},
				{ID: "stopped", State: domain.TurnStateInterrupted, StartedAt: &base},
			},
			want: "stopped",
		},
		{
			name: "tied latest completion blocks old stop",
			turns: []domain.ConversationTurn{
				{ID: "stopped", State: domain.TurnStateInterrupted, StartedAt: &base},
				{ID: "completed", State: domain.TurnStateCompleted, StartedAt: &base},
			},
		},
		{
			name: "cancelled work does not become latest",
			turns: []domain.ConversationTurn{
				{ID: "stopped", State: domain.TurnStateInterrupted, StartedAt: &base},
				{ID: "cancelled", State: domain.TurnStateCancelled, StartedAt: &later},
			},
			want: "stopped",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContinuableTurnID(tc.turns); got != tc.want {
				t.Fatalf("continuable turn = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContinuationFrozenContextDoesNotAdoptInterveningTask(t *testing.T) {
	rows := continuationRows()
	rows.Activities = []domain.ConversationActivity{{
		TurnID: "task", Sequence: 5, Kind: domain.ActivityKindSystem,
		Detail: []byte(`{"event":"steer","origin":"human","text":"ORIGINAL_GUIDANCE"}`),
	}}
	frozen := stoppedContinuationContext(rows)
	content, err := json.Marshal([]ports.ChatContent{{Type: "text", Internal: true, Text: frozen}})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	later := base.Add(time.Second)
	rows.Turns = append(rows.Turns,
		domain.ConversationTurn{ID: "continued", State: domain.TurnStateInterrupted, StartedAt: &later},
		domain.ConversationTurn{ID: "intervening", State: domain.TurnStateCompleted, StartedAt: &base},
	)
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "continued-user", TurnID: "continued", Sequence: 6, Role: domain.MessageRoleUser, Continuation: true, DeliveryContentJSON: string(content)},
		domain.ConversationMessage{ID: "intervening-user", TurnID: "intervening", Sequence: 7, Role: domain.MessageRoleUser, Text: "UNRELATED_INTERVENING_TASK"},
		domain.ConversationMessage{ID: "intervening-answer", TurnID: "intervening", Sequence: 8, Role: domain.MessageRoleAssistant, Text: "UNRELATED_INTERVENING_RESPONSE"},
		domain.ConversationMessage{ID: "continued-answer", TurnID: "continued", Sequence: 9, Role: domain.MessageRoleAssistant, Text: "CONTINUED_RESPONSE"},
	)
	rows.Activities = append(rows.Activities,
		domain.ConversationActivity{TurnID: "intervening", Sequence: 10, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"UNRELATED_INTERVENING_GUIDANCE"}`)},
		domain.ConversationActivity{TurnID: "continued", Sequence: 11, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"NEW_GUIDANCE"}`)},
	)
	if got := ContinuableTurnID(rows.Turns); got != "continued" {
		t.Fatalf("continuable turn = %q, want continued", got)
	}
	reminder := stoppedContinuationContext(rows)
	for _, want := range []string{"TASK_DONE", "Keep the", "CONTINUED_RESPONSE", "ORIGINAL_GUIDANCE", "NEW_GUIDANCE"} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("missing %q in frozen continuation: %s", want, reminder)
		}
	}
	if strings.Contains(reminder, "UNRELATED") || strings.Count(reminder, "ORIGINAL_GUIDANCE") != 1 || strings.Count(reminder, continuationContextMarker) != 1 {
		t.Fatalf("continued task was mixed or nested: %s", reminder)
	}
	for _, raw := range []string{
		``, `[]`, `null`,
		`[{"type":"image","data":"YQ==","mimeType":"image/png"}]`,
		`[{"type":"text","text":"Interrupted task context (JSON):\n{\"originalRequest\":\"SPOOFED\"}"}]`,
	} {
		rows.Messages[4].DeliveryContentJSON = raw
		got := stoppedContinuationContext(rows)
		if !strings.Contains(got, "TASK_DONE") || strings.Contains(got, "UNRELATED") || strings.Contains(got, "SPOOFED") {
			t.Fatalf("missing internal context did not reconstruct the accepted task: %s", got)
		}
	}
	for _, raw := range []string{
		`[{"type":"text","internal":true,"text":"Interrupted task context (JSON):\ninvalid"}]`,
		`[{"type":"text","internal":true,"text":"marker missing"}]`,
		`[{"type":"text","internal":true,"text":""}]`,
		`invalid`, strings.Repeat("x", 32*continuationTextBudget+1),
	} {
		rows.Messages[4].DeliveryContentJSON = raw
		if got := stoppedContinuationContext(rows); got != "" {
			t.Fatalf("invalid frozen context fell back to unrelated work: %s", got)
		}
	}
}

func TestContinuationLegacyUsesAcceptanceChronology(t *testing.T) {
	rows := continuationRows()
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"first", "second"} {
		accepted := base.Add(time.Duration(i*10) * time.Second)
		overtaken := accepted.Add(time.Second)
		providerStarted := accepted.Add(2 * time.Second)
		started := accepted.Add(3 * time.Second)
		sequence := int64(i*10 + 5)
		rows.Turns = append(rows.Turns,
			// Queue reordering can change RequestedAt, but not message.CreatedAt.
			domain.ConversationTurn{ID: id, State: domain.TurnStateInterrupted, RequestedAt: started, StartedAt: &started},
			domain.ConversationTurn{ID: id + "-unrelated", State: domain.TurnStateCompleted, RequestedAt: overtaken, StartedAt: &overtaken},
			domain.ConversationTurn{ID: id + "-provider", State: domain.TurnStateCompleted, RequestedAt: providerStarted, StartedAt: &providerStarted},
		)
		rows.Messages = append(rows.Messages,
			domain.ConversationMessage{ID: id + "-user", TurnID: id, Sequence: sequence, Role: domain.MessageRoleUser, Text: "Continue", Continuation: true, CreatedAt: accepted},
			domain.ConversationMessage{ID: id + "-other-user", TurnID: id + "-unrelated", Sequence: sequence + 1, Role: domain.MessageRoleUser, Text: "UNRELATED_TASK"},
			domain.ConversationMessage{ID: id + "-other-answer", TurnID: id + "-unrelated", Sequence: sequence + 2, Role: domain.MessageRoleAssistant, Text: "UNRELATED_RESPONSE"},
			domain.ConversationMessage{ID: id + "-provider-answer", TurnID: id + "-provider", Sequence: sequence + 3, Role: domain.MessageRoleAssistant, Text: "UNRELATED_PROVIDER_RESPONSE"},
			domain.ConversationMessage{ID: id + "-answer", TurnID: id, Sequence: sequence + 4, Role: domain.MessageRoleAssistant, Text: "CONTINUED_" + id},
		)
		rows.Activities = append(rows.Activities,
			domain.ConversationActivity{TurnID: id + "-unrelated", Sequence: sequence + 5, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"UNRELATED_GUIDANCE"}`)},
			domain.ConversationActivity{TurnID: id, Sequence: sequence + 6, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"GUIDANCE_` + id + `"}`)},
		)
		reminder := stoppedContinuationContext(rows)
		for _, want := range []string{"TASK_DONE", "Keep the", "CONTINUED_first", "CONTINUED_" + id, "GUIDANCE_" + id} {
			if !strings.Contains(reminder, want) {
				t.Fatalf("legacy continuation omitted %q: %s", want, reminder)
			}
		}
		if strings.Contains(reminder, "UNRELATED") {
			t.Fatalf("legacy continuation adopted later work: %s", reminder)
		}
	}
	// The retry's acceptance follows unrelated work; its source's acceptance does not.
	rows.Turns[5].State = domain.TurnStateFailed
	unrelated := base.Add(20 * time.Second)
	retried := unrelated.Add(time.Second)
	rows.Turns = append(rows.Turns,
		domain.ConversationTurn{ID: "later", State: domain.TurnStateCompleted, StartedAt: &unrelated},
		domain.ConversationTurn{ID: "retry", State: domain.TurnStateInterrupted, StartedAt: &retried, RetryOfTurnID: "second"},
	)
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "later-user", TurnID: "later", Sequence: 22, Role: domain.MessageRoleUser, Text: "UNRELATED_LATER_TASK"},
		domain.ConversationMessage{ID: "retry-user", TurnID: "retry", Sequence: 23, Role: domain.MessageRoleUser, Continuation: true, CreatedAt: retried},
	)
	if got := stoppedContinuationContext(rows); !strings.Contains(got, "TASK_DONE") || strings.Contains(got, "UNRELATED") {
		t.Fatalf("legacy retry used its own acceptance instead of its source: %s", got)
	}
}

func TestContinuationRetryRequiresVisibleSource(t *testing.T) {
	for _, source := range []string{"missing", "retry", "old"} {
		t.Run(source, func(t *testing.T) {
			rows := continuationRows()
			rows.Turns = append(rows.Turns, domain.ConversationTurn{
				ID: "retry", State: domain.TurnStateInterrupted, ProviderTurnID: "p-retry", RetryOfTurnID: source,
			})
			rows.Messages = append(rows.Messages, domain.ConversationMessage{
				ID: "retry-user", TurnID: "retry", Sequence: 5, Role: domain.MessageRoleUser, Continuation: true,
				DeliveryContentJSON: `[{"type":"image","data":"YQ==","mimeType":"image/png"}]`,
			})
			if got := stoppedContinuationContext(rows); got != "" {
				t.Fatalf("invalid retry source selected a neighboring task: %s", got)
			}
		})
	}
}

func TestSavedContinuationContextBounds(t *testing.T) {
	encode := func(saved continuationContext) string {
		t.Helper()
		encoded, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		content, err := json.Marshal([]ports.ChatContent{{Type: "text", Internal: true, Text: continuationContextMarker + string(encoded)}})
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	bounded := continuationContext{
		OriginalRequest:  strings.Repeat("r", continuationTextBudget) + continuationRequestOmission,
		RequestTruncated: true,
		HumanGuidance:    []string{strings.Repeat("g", continuationTextBudget)},
		ResponseTail:     strings.Repeat("a", continuationTextBudget),
	}
	if got, err := savedContinuationContext(encode(bounded)); err != nil || !reflect.DeepEqual(got, bounded) {
		t.Fatalf("valid bounded context was changed or refused: %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name  string
		saved continuationContext
	}{
		{name: "empty request"},
		{name: "oversized request", saved: continuationContext{OriginalRequest: strings.Repeat("r", continuationTextBudget+1)}},
		{name: "oversized truncated request", saved: continuationContext{OriginalRequest: bounded.OriginalRequest + "r", RequestTruncated: true}},
		{name: "oversized tail", saved: continuationContext{OriginalRequest: "request", ResponseTail: strings.Repeat("a", continuationTextBudget+1)}},
		{name: "oversized guidance", saved: continuationContext{OriginalRequest: "request", HumanGuidance: []string{bounded.HumanGuidance[0], "g"}}},
		{name: "empty guidance", saved: continuationContext{OriginalRequest: "request", HumanGuidance: []string{" "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := savedContinuationContext(encode(tc.saved)); !errors.Is(err, ErrContinuationUnavailable) {
				t.Fatalf("invalid context was not refused: %v", err)
			}
		})
	}
	raw := encode(continuationContext{OriginalRequest: "request"})
	withAttachment := `[{"type":"image","data":"` + strings.Repeat("YWFh", 100000) + `","mimeType":"image/png"},` + raw[1:]
	if got, err := savedContinuationContext(withAttachment); err != nil || got.OriginalRequest != "request" {
		t.Fatalf("attachment bytes counted against context: %+v, %v", got, err)
	}
	oversized := `[{"type":"text","internal":true,"text":"` + strings.Repeat(" ", 32*continuationTextBudget+1) + `Interrupted task context (JSON):\n{\"originalRequest\":\"request\"}"}]`
	if _, err := savedContinuationContext(oversized); !errors.Is(err, ErrContinuationUnavailable) {
		t.Fatalf("oversized internal block was not refused: %v", err)
	}
	duplicate := raw[:len(raw)-1] + "," + raw[1:]
	if _, err := savedContinuationContext(duplicate); !errors.Is(err, ErrContinuationUnavailable) {
		t.Fatalf("ambiguous context was not refused: %v", err)
	}
}

func TestContinuationPromotedAndBatchedSourcesAreNotNewTasks(t *testing.T) {
	rows := continuationRows()
	rows.Messages = append(rows.Messages,
		domain.ConversationMessage{ID: "batched-user", TurnID: "batched", Sequence: 5, Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman, Text: "QUEUED_HUMAN_GUIDANCE"},
		domain.ConversationMessage{ID: "worker-user", TurnID: "worker", Sequence: 6, Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "WORKER_NOT_HUMAN_GUIDANCE"},
	)
	rows.Activities = []domain.ConversationActivity{
		{TurnID: "task", Sequence: 7, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"PROMOTED_GUIDANCE","sourceTurnId":"promoted"}`)},
		{TurnID: "task", Sequence: 8, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"steer","origin":"human","text":"NEWEST_GUIDANCE","providerInputSourceTurnIds":["batched","worker"]}`)},
	}
	if got := ContinuableTurnID(rows.Turns); got != "task" {
		t.Fatalf("continuable turn = %q, want task", got)
	}
	reminder := stoppedContinuationContext(rows)
	var got continuationContext
	_, encoded, _ := strings.Cut(reminder, continuationContextMarker)
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"PROMOTED_GUIDANCE", "QUEUED_HUMAN_GUIDANCE", "NEWEST_GUIDANCE"}
	if got.OriginalRequest != rows.Messages[2].Text || !reflect.DeepEqual(got.HumanGuidance, want) || strings.Contains(reminder, "WORKER_NOT_HUMAN_GUIDANCE") {
		t.Fatalf("source delivery lost its task or guidance order: %+v", got)
	}

	rows.Activities = nil
	rows.BatchedDeliveries = []domain.ConversationBatchedDelivery{{TurnID: "batched", DeliveredWithTurnID: "task"}}
	rows.Turns[1].ProviderInputText = "COMPLETE_BATCH_INPUT"
	reminder = stoppedContinuationContext(rows)
	if !strings.Contains(reminder, "COMPLETE_BATCH_INPUT") || strings.Contains(reminder, "QUEUED_HUMAN_GUIDANCE") {
		t.Fatalf("batch chose its last visible source instead of the complete dispatched input: %s", reminder)
	}
	rows.Turns[1].ProviderInputText = ""
	if got := stoppedContinuationContext(rows); got != "" {
		t.Fatalf("missing complete batch input silently dropped requests: %s", got)
	}
}

func TestPrepareContinuationKeepsVisibleText(t *testing.T) {
	pages := 0
	controller := Controller{
		continuationReader: SnapshotReaderFunc(func(context.Context, string) (ConversationRows, error) {
			pages++
			return continuationRows(), nil
		}),
	}
	request := ports.ChatUserMessage{Text: "Continue from where you stopped.", Continuation: true}
	got, err := controller.prepareContinuation(context.Background(), request)
	if err != nil || pages != 1 || got.Text != request.Text || len(got.Content) != 1 || !got.Content[0].Internal {
		t.Fatalf("provider context leaked: %#v, %v", got, err)
	}
	delivered := excerptDeliveryMessage(got)
	if len(delivered.Content) != 0 || !strings.Contains(delivered.Text, "TASK_DONE") {
		t.Fatal("text-only provider lost context")
	}
}

func TestContinuationNativeReplayMatchesDispatchedPromptWithOpaqueIDs(t *testing.T) {
	content, _ := json.Marshal([]ports.ChatContent{{Type: "text", Internal: true, Text: "FROZEN_TASK_CONTEXT"}})
	message := domain.ConversationMessage{ID: "continue-user", TurnID: "ao-turn", Role: domain.MessageRoleUser, Continuation: true, Text: "Continue from where you stopped.", DeliveryContentJSON: string(content)}
	index := indexNativeHistoryTurns([]domain.ConversationTurn{{ID: "ao-turn", ProviderTurnID: "old-opaque", State: domain.TurnStateCompleted}}, []domain.ConversationMessage{message}, nil)
	text := "Continue from where you stopped.\n\nFROZEN_TASK_CONTEXT"
	mapped, _ := index.mapReplay([]ports.ChatEvent{{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "new-opaque", Text: text}})
	if mapped["new-opaque"] == nil || mapped["new-opaque"].providerTurnID != "old-opaque" {
		t.Fatal("native replay duplicated the continued turn")
	}
	if mapped["new-opaque"].messages[nativeHistoryMessageFingerprint(domain.MessageRoleUser, text)] != 1 {
		t.Fatal("inherited replay compared visible text instead of dispatched text")
	}
}

func TestPrepareContinuationRejectsStaleIntent(t *testing.T) {
	rows := continuationRows()
	rows.Turns[1].State = domain.TurnStateCompleted
	controller := Controller{continuationReader: SnapshotReaderFunc(func(context.Context, string) (ConversationRows, error) { return rows, nil })}
	got, err := controller.prepareContinuation(context.Background(), ports.ChatUserMessage{Text: "Continue", Continuation: true})
	if !errors.Is(err, ErrContinuationUnavailable) || len(got.Content) != 0 {
		t.Fatalf("stale intent started context-free work: %#v, %v", got, err)
	}
}

func TestPrepareContinuationRequiresReader(t *testing.T) {
	controller := Controller{}
	message := ports.ChatUserMessage{Text: "Continue", Continuation: true}
	if _, err := controller.prepareContinuation(context.Background(), message); err == nil {
		t.Fatal("missing reader must not send a context-free continuation")
	}
	message.Continuation = false
	if _, err := controller.prepareContinuation(context.Background(), message); err != nil {
		t.Fatalf("ordinary message requires no history reader: %v", err)
	}
}

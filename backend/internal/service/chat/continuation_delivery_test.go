package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func continuationMessage(key string) ports.ChatUserMessage {
	return ports.ChatUserMessage{
		Text: "Continue from where you stopped.", ClientMessageID: key,
		Origin: domain.MessageOriginHuman, Continuation: true,
	}
}

func settleContinuationTestTurn(t *testing.T, h *harness, turn domain.ConversationTurn, partial string, state domain.TurnState) {
	t.Helper()
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
	if partial != "" {
		h.conv.emit(ports.ChatEvent{
			Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID,
			ProviderItemID: "answer-" + turn.ProviderTurnID, Delta: partial,
		})
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: state})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, current := range s.Turns {
			if current.ID == turn.ID {
				return current.State == state
			}
		}
		return false
	})
}

func seedContinuationTask(t *testing.T, h *harness) domain.ConversationTurn {
	t.Helper()
	turn, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "Write 30 AO tips without tools and end with TASK_DONE", ClientMessageID: "original-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	settleContinuationTestTurn(t, h, turn, "1. Use worktrees.\n2. Keep the", domain.TurnStateInterrupted)
	return turn
}

func TestContinuationDeliveryPersistsEnrichedPromptAndIntakeIdentity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedContinuationTask(t, h)
	request := continuationMessage("continue-1")
	turn, err := h.svc.Send(ctx, testSession, request)
	if err != nil {
		t.Fatal(err)
	}
	sent := h.conv.sentMessages()[1]
	if !strings.HasPrefix(sent.Text, "Continue from where you stopped.\n\n") {
		t.Fatalf("provider received %q, want the continue prompt followed by context", sent.Text)
	}
	if !strings.Contains(continuationProviderContext(sent), "TASK_DONE") || !strings.Contains(continuationProviderContext(sent), "Keep the") || !sent.Continuation {
		t.Fatalf("provider did not receive task context: %#v", sent)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 3 })
	if snapshot.Messages[2].Text != "Continue from where you stopped." || snapshot.Messages[2].DeliveryContentJSON == "" || !snapshot.Messages[2].Continuation ||
		snapshot.Messages[2].ClientPayloadHash == "" {
		t.Fatalf("context and identity not persisted: %#v", snapshot.Messages[2])
	}
	settleContinuationTestTurn(t, h, turn, "TASK_DONE", domain.TurnStateCompleted)
	duplicate, err := h.svc.Send(ctx, testSession, request)
	if err != nil || duplicate.ID != "" || h.conv.sendCallCount() != 2 {
		t.Fatalf("duplicate after completion = %#v, %v, calls=%d", duplicate, err, h.conv.sendCallCount())
	}
	request.Text = "A different request reusing the same id"
	if _, err := h.svc.Send(ctx, testSession, request); !errors.Is(err, domain.ErrClientMessageConflict) {
		t.Fatalf("changed request should conflict: %v", err)
	}
}

func TestContinuationDeliveryRetainsAcceptedHumanSteering(t *testing.T) {
	h, provider := steerHarness(t)
	ctx := context.Background()
	guidance := "Check every worker; report actual replies without authorizing new work."
	if _, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
		Text: guidance, ClientMessageID: "human-guidance", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateInterrupted})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateInterrupted
	})
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-guidance")); err != nil {
		t.Fatal(err)
	}
	if text := continuationProviderContext(provider.sentMessages()[1]); !strings.Contains(text, guidance) {
		t.Fatalf("Stop then Continue lost accepted human guidance: %s", text)
	}
}

func TestContinuationDeliveryRepeatedStopKeepsTaskAndNewestResponse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedContinuationTask(t, h)
	first, err := h.svc.Send(ctx, testSession, continuationMessage("continue-1"))
	if err != nil {
		t.Fatal(err)
	}
	settleContinuationTestTurn(t, h, first, "3. Give every session a", domain.TurnStateInterrupted)
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-2")); err != nil {
		t.Fatal(err)
	}
	text := continuationProviderContext(h.conv.sentMessages()[2])
	if !strings.Contains(text, "TASK_DONE") || !strings.Contains(text, "Give every session a") ||
		strings.Count(text, "Interrupted task context (JSON):") != 1 {
		t.Fatalf("repeat continuation nested or lost context: %s", text)
	}
}

func TestContinuationDeliveryNewQuestionRemainsUnchanged(t *testing.T) {
	h := newHarness(t)
	seedContinuationTask(t, h)
	question := "Ignore that task and reply exactly NEW_QUESTION_OK"
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: question}); err != nil {
		t.Fatal(err)
	}
	if got := h.conv.sentTexts()[1]; got != question {
		t.Fatalf("ordinary message gained continuation context: %q", got)
	}
}

func TestContinuationDeliveryRetryUsesFrozenContextAndMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedContinuationTask(t, h)
	continued, err := h.svc.Send(ctx, testSession, continuationMessage("continue-1"))
	if err != nil {
		t.Fatal(err)
	}
	frozen := h.conv.sentMessages()[1]
	settleContinuationTestTurn(t, h, continued, "A later partial response", domain.TurnStateFailed)
	retry, err := h.svc.RetryTurn(ctx, testSession, continued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.conv.sentMessages()[2]; got.Text != frozen.Text || !reflect.DeepEqual(got.Content, frozen.Content) || !got.Continuation {
		t.Fatalf("retry reconstructed context or lost marker: %#v", got)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, message := range s.Messages {
			if message.TurnID == retry.ID {
				return message.Continuation
			}
		}
		return false
	})
	if len(snapshot.Turns) != 3 {
		t.Fatalf("retry changed historical turns: %#v", snapshot.Turns)
	}
	settleContinuationTestTurn(t, h, retry, "STOPPED_RETRY_PARTIAL", domain.TurnStateInterrupted)
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-after-retry")); err != nil {
		t.Fatal(err)
	}
	text := continuationProviderContext(h.conv.sentMessages()[3])
	if !strings.Contains(text, "TASK_DONE") || !strings.Contains(text, "STOPPED_RETRY_PARTIAL") || strings.Count(text, "Interrupted task context (JSON):") != 1 {
		t.Fatalf("text-only retry lost or nested its context: %s", text)
	}
}

func TestContinuationDeliveryWithAttachmentsAcrossRetries(t *testing.T) {
	for _, retryCount := range []int{0, 2} {
		t.Run(fmt.Sprintf("retries-%d", retryCount), func(t *testing.T) {
			h := newHarness(t)
			caps := h.conv.Capabilities()
			caps[ports.ChatCapabilityImages] = true
			h.conv.setCapabilities(caps)
			ctx := context.Background()
			seedContinuationTask(t, h)
			h.advance(time.Second)
			request := continuationMessage("continue-attachment")
			request.Content = []ports.ChatContent{{Type: "image", MIMEType: "image/png", Data: strings.Repeat("YWFh", 100000)}}
			turn, err := h.svc.Send(ctx, testSession, request)
			if err != nil {
				t.Fatal(err)
			}
			for i := range retryCount {
				settleContinuationTestTurn(t, h, turn, fmt.Sprintf("FAILED_PARTIAL_%d", i), domain.TurnStateFailed)
				h.advance(time.Second)
				other, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "UNRELATED_TASK"})
				if err != nil {
					t.Fatal(err)
				}
				settleContinuationTestTurn(t, h, other, "UNRELATED_RESPONSE", domain.TurnStateCompleted)
				h.advance(time.Second)
				source := turn.ID
				turn, err = h.svc.RetryTurn(ctx, testSession, source)
				if err != nil {
					t.Fatal(err)
				}
				durable, err := h.st.TurnByID(ctx, turn.ID)
				if err != nil || durable.RetryOfTurnID != source {
					t.Fatalf("retry source was not persisted: %+v, %v", durable, err)
				}
				snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
				if err != nil {
					t.Fatal(err)
				}
				var retryMessage domain.ConversationMessage
				for _, message := range snapshot.Messages {
					if message.TurnID == turn.ID && message.Role == domain.MessageRoleUser {
						retryMessage = message
					}
				}
				var content []ports.ChatContent
				if err := json.Unmarshal([]byte(retryMessage.DeliveryContentJSON), &content); err != nil {
					t.Fatal(err)
				}
				if !retryMessage.Continuation || !reflect.DeepEqual(content, request.Content) {
					t.Fatal("retry did not retain attachments while removing rendered internal context")
				}
			}
			settleContinuationTestTurn(t, h, turn, "LATEST_PARTIAL", domain.TurnStateInterrupted)
			h.advance(time.Second)
			if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-again")); err != nil {
				t.Fatal(err)
			}
			sent := h.conv.sentMessages()
			text := continuationProviderContext(sent[len(sent)-1])
			for _, want := range []string{"TASK_DONE", "Keep the", "LATEST_PARTIAL"} {
				if !strings.Contains(text, want) {
					t.Fatalf("continued context omitted %q: %s", want, text)
				}
			}
			for i := range retryCount {
				if !strings.Contains(text, fmt.Sprintf("FAILED_PARTIAL_%d", i)) {
					t.Fatalf("continued context omitted a retry source response: %s", text)
				}
			}
			if strings.Contains(text, "UNRELATED") || strings.Count(text, "Interrupted task context (JSON):") != 1 {
				t.Fatalf("retry adopted another task or nested context: %s", text)
			}
		})
	}
}

func TestContinuationDeliveryQueuedPromptRetainsFrozenContext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "Write 30 AO tips and end with QUEUED_TASK_DONE", ClientMessageID: "original-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: first.ProviderTurnID},
		ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: first.ProviderTurnID, ProviderItemID: "answer", Delta: "An unfinished tip"},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Second)
	// Model the interval after the durable stop commits but before the provider
	// completion event clears the controller's busy gate.
	if err := h.st.SettleTurnByID(ctx, first.ID, domain.TurnStateInterrupted, "", h.now()); err != nil {
		t.Fatal(err)
	}
	queued, err := h.svc.Send(ctx, testSession, continuationMessage("continue-queued"))
	if err != nil || queued.State != domain.TurnStateQueued {
		t.Fatalf("queue continuation: %#v, %v", queued, err)
	}
	durable, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil || durable.TurnID != queued.ID {
		t.Fatalf("durable queue: %#v, %v", durable, err)
	}
	if !strings.Contains(durable.DeliveryContentJSON, "QUEUED_TASK_DONE") || !strings.Contains(durable.DeliveryContentJSON, "An unfinished tip") {
		t.Fatalf("queued continuation did not freeze context: %q", durable.Text)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: first.ProviderTurnID, TurnState: domain.TurnStateInterrupted})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return h.conv.sendCallCount() == 2 })
	var frozenContent []ports.ChatContent
	if err := json.Unmarshal([]byte(durable.DeliveryContentJSON), &frozenContent); err != nil {
		t.Fatal(err)
	}
	expected := durable.Text
	for _, block := range frozenContent {
		if block.Internal {
			expected += "\n\n" + block.Text
		}
	}
	if got := h.conv.sentMessages()[1]; got.Text != expected {
		t.Fatalf("queue drain changed its accepted prompt: %#v", got)
	}
}

func TestContinuationDeliveryRetrySurvivesControllerRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedContinuationTask(t, h)
	continued, err := h.svc.Send(ctx, testSession, continuationMessage("continue-restart"))
	if err != nil {
		t.Fatal(err)
	}
	frozen := h.conv.sentMessages()[1]
	settleContinuationTestTurn(t, h, continued, "", domain.TurnStateFailed)
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}

	provider := newFakeConversation()
	provider.turnSeq = 10 // a reconnected provider's turn IDs are still unique
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), PageReader: boundedSnapshotReader(h.st), Sessions: h.st,
		Drivers: fakeRegistry{driver: fakeDriver{conv: provider}},
		NewID:   uuid.NewString, Now: h.now,
	})
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop(ctx, testSession) })
	if _, err := svc.RetryTurn(ctx, testSession, continued.ID); err != nil {
		t.Fatal(err)
	}
	sent := provider.sentMessages()
	if len(sent) != 1 || sent[0].Text != frozen.Text || !reflect.DeepEqual(sent[0].Content, frozen.Content) || !sent[0].Continuation {
		t.Fatalf("restart retry lost its frozen context or marker: %#v", sent)
	}
}

func TestContinuationDeliveryHibernatedQueuePreservesContextAndMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedContinuationTask(t, h)
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read session: %v, found=%v", err, found)
	}
	now := time.Now()
	changed, err := h.st.SetSessionHibernated(ctx, testSession, record.Revision, &now)
	if err != nil || !changed {
		t.Fatalf("hibernate session: %v, changed=%v", err, changed)
	}
	request := continuationMessage("continue-hibernated")
	if _, err := h.svc.QueueUserMessage(ctx, testSession, request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	message := snapshot.Messages[len(snapshot.Messages)-1]
	if !message.Continuation || !strings.Contains(message.DeliveryContentJSON, "TASK_DONE") || !strings.Contains(message.DeliveryContentJSON, "Keep the") {
		t.Fatalf("queued continuation lost context or marker: %#v", message)
	}
	if duplicate, err := h.svc.QueueUserMessage(ctx, testSession, request); err != nil || duplicate.ID != "" {
		t.Fatalf("duplicate continuation: %#v, %v", duplicate, err)
	}
	queued, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil || queued.Text != message.Text {
		t.Fatalf("durable continuation queue lost frozen context: %#v, %v", queued, err)
	}
}

func TestContinuationDeliveryAfterPriorityOvertakesQueuedTask(t *testing.T) {
	provider := newSteerRecorder()
	h := newHarnessWithConversation(t, provider)
	h.conv = provider.fakeConversation
	ctx := context.Background()
	send := func(text string, priority bool) domain.ConversationTurn {
		t.Helper()
		turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
			Text: text, ClientMessageID: text, Origin: domain.MessageOriginHuman, InterruptAndSend: priority,
		})
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	a := send("UNRELATED_A", false)
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: a.ProviderTurnID})
	h.advance(time.Second)
	b := send("ORIGINAL_B", false)
	h.advance(time.Second)
	c := send("UNRELATED_C", true)
	h.advance(time.Second)
	settleContinuationTestTurn(t, h, a, "UNRELATED_A_RESPONSE", domain.TurnStateInterrupted)
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		turn, _ := turnByID(s, c.ID)
		return turn.State == domain.TurnStateRunning && turn.ProviderTurnID != ""
	})
	c, _ = turnByID(snapshot, c.ID)
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: c.ProviderTurnID})
	h.advance(time.Second)
	settleContinuationTestTurn(t, h, c, "UNRELATED_C_RESPONSE", domain.TurnStateCompleted)
	snapshot = h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		turn, _ := turnByID(s, b.ID)
		return turn.State == domain.TurnStateRunning && turn.ProviderTurnID != ""
	})
	b, _ = turnByID(snapshot, b.ID)
	if !reflect.DeepEqual(provider.sentTexts(), []string{"UNRELATED_A", "UNRELATED_C", "ORIGINAL_B"}) {
		t.Fatalf("fixture did not dispatch A,C,B: %v", provider.sentTexts())
	}
	if len(snapshot.Turns) != 3 || snapshot.Turns[0].ID != a.ID || snapshot.Turns[1].ID != b.ID || snapshot.Turns[2].ID != c.ID {
		t.Fatalf("fixture lost enqueue-ordered snapshot: %+v", snapshot.Turns)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: b.ProviderTurnID})
	if _, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
		Text: "B_GUIDANCE", ClientMessageID: "steer-b", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Second)
	swept := send("UNRELATED_SWEPT", false)
	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	settleContinuationTestTurn(t, h, b, "PARTIAL_B", domain.TurnStateInterrupted)
	snapshot = h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		turn, _ := turnByID(s, swept.ID)
		return turn.State == domain.TurnStateInterrupted
	})
	if got := chatsvc.ContinuableTurnID(snapshot.Turns); got != b.ID {
		t.Fatalf("continuable turn = %q, want B %q", got, b.ID)
	}
	h.advance(time.Second)
	continued, err := h.svc.Send(ctx, testSession, continuationMessage("continue-b"))
	if err != nil {
		t.Fatal(err)
	}
	text := continuationProviderContext(provider.sentMessages()[3])
	for _, want := range []string{"ORIGINAL_B", "B_GUIDANCE", "PARTIAL_B"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Continue omitted %q: %s", want, text)
		}
	}
	if strings.Contains(text, "UNRELATED") {
		t.Fatalf("Continue mixed unrelated tasks: %s", text)
	}
	settleContinuationTestTurn(t, h, continued, "B_DONE", domain.TurnStateCompleted)
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("stale-continue")); !errors.Is(err, chatsvc.ErrContinuationUnavailable) {
		t.Fatalf("completed continuation revived old stopped work: %v", err)
	}
}

func TestContinuationDeliveryRetainsPromotedAndBatchedHumanGuidance(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "promoted"
		if batch {
			name = "batched"
		}
		t.Run(name, func(t *testing.T) {
			h, provider := steerHarness(t)
			h.conv = provider.fakeConversation
			ctx := context.Background()
			h.advance(time.Second)
			source, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
				Text: "QUEUED_HUMAN_CONSTRAINT", ClientMessageID: "queued-guidance", Origin: domain.MessageOriginHuman,
			})
			if err != nil {
				t.Fatal(err)
			}
			if batch {
				_, err = h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
					Text: "NEWEST_HUMAN_CONSTRAINT", ClientMessageID: "batch-steer", Origin: domain.MessageOriginHuman,
				})
			} else {
				_, err = h.svc.PromoteQueuedTurn(ctx, testSession, source.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := h.svc.Interrupt(ctx, testSession); err != nil {
				t.Fatal(err)
			}
			provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateInterrupted})
			snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateInterrupted
			})
			if got := chatsvc.ContinuableTurnID(snapshot.Turns); got != snapshot.Turns[0].ID {
				t.Fatalf("promoted source hid interrupted target: %q", got)
			}
			if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-guidance")); err != nil {
				t.Fatal(err)
			}
			text := continuationProviderContext(provider.sentMessages()[1])
			if !strings.Contains(text, `"originalRequest":"do the long thing"`) || !strings.Contains(text, "QUEUED_HUMAN_CONSTRAINT") {
				t.Fatalf("Continue lost original request or delivered guidance: %s", text)
			}
			if batch && !strings.Contains(text, `"humanGuidance":["QUEUED_HUMAN_CONSTRAINT","NEWEST_HUMAN_CONSTRAINT"]`) {
				t.Fatalf("Continue lost batch guidance order: %s", text)
			}
		})
	}
}

func continuationProviderContext(message ports.ChatUserMessage) string {
	text := message.Text
	for _, block := range message.Content {
		if block.Internal {
			text += block.Text
		}
	}
	return text
}

func TestContinuationAfterControllerReplacementKeepsTaskContext(t *testing.T) {
	h, _, driver := newEditHarness(t, true)
	ctx := context.Background()
	first := completeTurn(t, h, "ORIGINAL_TASK", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	edited, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{Text: "EDITED_TASK"})
	if err != nil {
		t.Fatal(err)
	}
	h.conv = driver.fresh
	settleContinuationTestTurn(t, h, edited.Turn, "EDIT_PARTIAL", domain.TurnStateInterrupted)
	continued, err := h.svc.Send(ctx, testSession, continuationMessage("continue-edited"))
	if err != nil {
		t.Fatal(err)
	}
	if text := continuationProviderContext(h.conv.sentMessages()[1]); !strings.Contains(text, "EDITED_TASK") || !strings.Contains(text, "EDIT_PARTIAL") {
		t.Fatalf("edited controller lost context: %s", text)
	}
	settleContinuationTestTurn(t, h, continued, "", domain.TurnStateCompleted)
	if _, err := h.svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatal(err)
	}
	h.conv = driver.resumed["thread-1"]
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "ACTIVATED_TASK"})
	if err != nil {
		t.Fatal(err)
	}
	settleContinuationTestTurn(t, h, turn, "ACTIVATED_PARTIAL", domain.TurnStateInterrupted)
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-activated")); err != nil {
		t.Fatal(err)
	}
	if text := continuationProviderContext(h.conv.sentMessages()[1]); !strings.Contains(text, "ACTIVATED_TASK") || !strings.Contains(text, "ACTIVATED_PARTIAL") {
		t.Fatalf("activated controller lost context: %s", text)
	}
}

func TestContinuationAfterFailedEditRecoveryKeepsTaskContext(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	completeTurn(t, h, "EARLIER_TASK", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	first := completeTurn(t, h, "TASK_BEFORE_FAILED_EDIT", "provider-turn-2")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 4 })
	replacement := driver.resumed["thread-forked"]
	replacement.mu.Lock()
	replacement.sendErr = refusedError{msg: "provider declined edit"}
	replacement.mu.Unlock()
	if _, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{Text: "REJECTED_EDIT"}); !errors.Is(err, chatsvc.ErrProviderRefused) {
		t.Fatalf("expected rejected edit, got %v", err)
	}
	h.conv = driver.resumed["thread-1"]
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "RECOVERED_TASK"})
	if err != nil {
		t.Fatal(err)
	}
	settleContinuationTestTurn(t, h, turn, "RECOVERED_PARTIAL", domain.TurnStateInterrupted)
	if _, err := h.svc.Send(ctx, testSession, continuationMessage("continue-recovered")); err != nil {
		t.Fatal(err)
	}
	if text := continuationProviderContext(h.conv.sentMessages()[1]); !strings.Contains(text, "RECOVERED_TASK") || !strings.Contains(text, "RECOVERED_PARTIAL") {
		t.Fatalf("recovered controller lost context: %s", text)
	}
}

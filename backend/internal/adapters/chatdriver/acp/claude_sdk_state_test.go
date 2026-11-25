package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeSDKIdleSettlesOnlyTurnlessOutput(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			conv, _ := replayBoundaryConversation(t)
			conv.mu.Lock()
			conv.providerReportsState = true
			if !active {
				conv.activeTurn = ""
			}
			conv.mu.Unlock()
			if err := conv.processUpdate(acpsdk.SessionNotification{
				SessionId: "review-session", Update: acpsdk.UpdateAgentMessageText("background reply"),
			}); err != nil {
				t.Fatal(err)
			}
			delta := nextEvent(t, conv.Events())
			if delta.Kind != ports.ChatEventMessageDelta {
				t.Fatalf("expected message delta, got %+v", delta)
			}
			idle := []byte(`{"sessionId":"review-session","message":{"type":"system","subtype":"session_state_changed","state":"idle"}}`)
			if err := conv.handleClaudeSDKMessage(idle); err != nil {
				t.Fatal(err)
			}
			event := nextEvent(t, conv.Events())
			if !active {
				if event.Kind != ports.ChatEventMessageCompleted || event.ProviderTurnID != "" ||
					event.ProviderItemID != delta.ProviderItemID || event.Text != "" {
					t.Fatalf("turnless reply did not settle: %+v", event)
				}
				event = nextEvent(t, conv.Events())
			}
			if event.Kind != ports.ChatEventProviderState {
				t.Fatalf("idle must not settle a prompted turn: %+v", event)
			}
			if err := conv.handleClaudeSDKMessage(idle); err != nil {
				t.Fatal(err)
			}
			if event = nextEvent(t, conv.Events()); event.Kind != ports.ChatEventProviderState {
				t.Fatalf("repeated idle finalized old output again: %+v", event)
			}
		})
	}
}

func TestBackgroundReplySurvivesNextPromptAndUsesDistinctFallbackIDs(t *testing.T) {
	conv, _ := replayBoundaryConversation(t)
	conv.mu.Lock()
	conv.activeTurn = ""
	conv.providerReportsState = true
	conv.mu.Unlock()
	var previousID string
	for range 2 {
		if err := conv.processUpdate(acpsdk.SessionNotification{
			SessionId: "review-session", Update: acpsdk.UpdateAgentMessageText("background reply"),
		}); err != nil {
			t.Fatal(err)
		}
		delta := nextEvent(t, conv.Events())
		if delta.Kind != ports.ChatEventMessageDelta || delta.ProviderItemID == previousID {
			t.Fatalf("background cycles share a message identity: %+v", delta)
		}
		previousID = delta.ProviderItemID
		// Detach before resetting buffers, as StartDeferredTurn does. A trailing
		// SDK idle must never settle the new prompt's output.
		conv.mu.Lock()
		ended := conv.detachProviderCycleLocked()
		conv.activeTurn = "next-turn"
		conv.messages = map[string]string{"next-message": "still working"}
		conv.mu.Unlock()
		conv.settleEndedCycle(ended)
		if err := conv.handleClaudeSDKMessage([]byte(`{"sessionId":"review-session","message":{"type":"system","subtype":"session_state_changed","state":"idle"}}`)); err != nil {
			t.Fatal(err)
		}
		settled := nextEvent(t, conv.Events())
		if settled.Kind != ports.ChatEventMessageCompleted || settled.ProviderTurnID != "" ||
			settled.ProviderItemID != delta.ProviderItemID || settled.Text != "" {
			t.Fatalf("next prompt discarded or adopted background output: %+v", settled)
		}
		if event := nextEvent(t, conv.Events()); event.Kind != ports.ChatEventProviderState {
			t.Fatalf("idle settled a message from the next prompt: %+v", event)
		}
		conv.mu.Lock()
		conv.activeTurn = ""
		conv.mu.Unlock()
	}
}

func TestNextPromptFinalizesBackgroundReplyBeforeStarting(t *testing.T) {
	conv, _ := replayBoundaryConversation(t)
	conv.mu.Lock()
	conv.activeTurn = ""
	conv.providerReportsState = true
	conv.prepared = &preparedTurn{id: "next-turn"}
	conv.mu.Unlock()
	if err := conv.processUpdate(acpsdk.SessionNotification{
		SessionId: "review-session", Update: acpsdk.UpdateAgentMessageText("background reply"),
	}); err != nil {
		t.Fatal(err)
	}
	delta := nextEvent(t, conv.Events())
	if err := conv.StartDeferredTurn("next-turn"); err != nil {
		t.Fatal(err)
	}
	settled := nextEvent(t, conv.Events())
	if settled.Kind != ports.ChatEventMessageCompleted || settled.ProviderTurnID != "" ||
		settled.ProviderItemID != delta.ProviderItemID || settled.Text != "" {
		t.Fatalf("prompt start lost background reply: %+v", settled)
	}
	if event := nextEvent(t, conv.Events()); event.Kind != ports.ChatEventTurnStarted || event.ProviderTurnID != "next-turn" {
		t.Fatalf("prompt start was reordered: %+v", event)
	}
}

func TestBackgroundReplyRetainsReceiptIdentityAcrossAttachments(t *testing.T) {
	for _, scope := range []string{"", "provider-scope"} {
		t.Run(scope, func(t *testing.T) {
			for range 2 {
				conv, _ := replayBoundaryConversation(t)
				conv.mu.Lock()
				conv.activeTurn = ""
				conv.providerReportsState = true
				conv.providerScopeID = scope
				conv.mu.Unlock()
				// Reasoning can open the cycle before its first assistant message.
				if err := conv.processUpdate(acpsdk.SessionNotification{
					SessionId: "review-session", Meta: map[string]any{persistenthost.ACPEventIDMetaKey: "reasoning-receipt"},
					Update: acpsdk.UpdateAgentThoughtText("checking"),
				}); err != nil {
					t.Fatal(err)
				}
				_ = nextEvent(t, conv.Events())
				_ = nextEvent(t, conv.Events())
				if err := conv.processUpdate(acpsdk.SessionNotification{
					SessionId: "review-session", Meta: map[string]any{persistenthost.ACPEventIDMetaKey: "message-receipt"},
					Update: acpsdk.UpdateAgentMessageText("retained tail"),
				}); err != nil {
					t.Fatal(err)
				}
				delta := nextEvent(t, conv.Events())
				if delta.Kind != ports.ChatEventMessageDelta || delta.ProviderItemID != conv.providerItemID("background-message-receipt") ||
					delta.ProviderEventID != "message-receipt:0" || delta.ProviderTurnID != "" {
					t.Fatalf("host replay identity changed: %+v", delta)
				}
				if err := conv.handleClaudeSDKMessage([]byte(`{"sessionId":"review-session","message":{"type":"system","subtype":"session_state_changed","state":"idle"}}`)); err != nil {
					t.Fatal(err)
				}
				settled := nextEvent(t, conv.Events())
				if settled.Kind != ports.ChatEventMessageCompleted || settled.ProviderItemID != delta.ProviderItemID || settled.Text != "" {
					t.Fatalf("replayed tail could overwrite the durable prefix: %+v", settled)
				}
			}
		})
	}
}

func TestClaudeSDKStateThroughOrderedTransport(t *testing.T) {
	conv, host := replayBoundaryConversation(t)
	go func() {
		_, _ = fmt.Fprintln(host, `{"jsonrpc":"2.0","method":"_claude/sdkMessage","params":{"sessionId":"review-session","message":{"type":"system","subtype":"session_state_changed","state":"running"}}}`)
	}()
	select {
	case event := <-conv.events:
		if event.Kind != ports.ChatEventProviderState || event.ThreadState == nil || event.ThreadState.Status != domain.ThreadStatusActive {
			t.Fatalf("provider state missing from SDK transport: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("SDK transport did not deliver provider state")
	}
}

// Claude Code's own running/idle report becomes provider.state, so a cycle the
// agent starts without an ACP prompt still shows as work.
func TestClaudeSDKSessionStateBecomesProviderState(t *testing.T) {
	conv := &conversation{
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessionID: "claude-session",
		events:    make(chan ports.ChatEvent, 8),
	}
	notify := func(params string) {
		t.Helper()
		if _, err := conv.HandleExtensionMethod(context.Background(), claudeSDKMessageMethod, []byte(params)); err != nil {
			t.Fatal(err)
		}
	}

	notify(`{"sessionId":"claude-session","message":{"type":"system","subtype":"session_state_changed","state":"running"},"_meta":{"` +
		persistenthost.ACPEventIDMetaKey + `":"ev-1"}}`)
	notify(`{"sessionId":"claude-session","message":{"type":"system","subtype":"session_state_changed","state":"idle"}}`)
	// Ignored: an approval reports requires_action itself, other sessions and
	// other SDK messages say nothing about this conversation's work.
	notify(`{"sessionId":"claude-session","message":{"type":"system","subtype":"session_state_changed","state":"requires_action"}}`)
	notify(`{"sessionId":"other-session","message":{"type":"system","subtype":"session_state_changed","state":"running"}}`)
	notify(`{"sessionId":"claude-session","message":{"type":"assistant"}}`)

	want := []struct {
		status  domain.ThreadStatus
		eventID string
	}{{domain.ThreadStatusActive, "ev-1:0"}, {domain.ThreadStatusIdle, ""}}
	for _, w := range want {
		select {
		case event := <-conv.events:
			if event.Kind != ports.ChatEventProviderState || event.ThreadState == nil ||
				event.ThreadState.Status != w.status || event.ProviderEventID != w.eventID {
				t.Fatalf("event = %+v (state %+v), want provider.state %s with id %q", event, event.ThreadState, w.status, w.eventID)
			}
		default:
			t.Fatalf("missing provider.state %s", w.status)
		}
	}
	select {
	case event := <-conv.events:
		t.Fatalf("unexpected event %+v", event)
	default:
	}
}

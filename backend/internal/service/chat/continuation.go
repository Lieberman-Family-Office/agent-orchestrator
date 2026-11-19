package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const continuationTextBudget = 8 * 1024
const continuationContextMarker = "Interrupted task context (JSON):\n"
const continuationRequestOmission = "\n[Middle of original request omitted]\n"

type continuationContext struct {
	OriginalRequest   string   `json:"originalRequest"`
	RequestTruncated  bool     `json:"requestTruncated,omitempty"`
	HumanGuidance     []string `json:"humanGuidance,omitempty"`
	ResponseTail      string   `json:"partialResponseTail,omitempty"`
	ResponseTruncated bool     `json:"responseTailTruncated,omitempty"`
}

// ErrContinuationUnavailable refuses a stale Continue intent without starting
// a context-free turn. The user can give a new instruction instead.
var ErrContinuationUnavailable = errors.New("the interrupted task is no longer available to continue; send a new instruction")

// prepareContinuation runs under sendMu, before persisting the provider request.
// The intake hash and visible text remain the client's original payload.
// Persisting internal content makes queue recovery and retries send the same context rather
// than reconstructing it from a conversation that may have changed meanwhile.
func (c *Controller) prepareContinuation(ctx context.Context, msg ports.ChatUserMessage) (ports.ChatUserMessage, error) {
	return prepareContinuation(ctx, c.store, c.continuationReader, c.conversation.ID, msg)
}

func prepareContinuation(ctx context.Context, store Store, reader SnapshotReader, conversationID string, msg ports.ChatUserMessage) (ports.ChatUserMessage, error) {
	if !msg.Continuation {
		return msg, nil
	}
	if msg.ClientMessageID != "" {
		_, found, err := store.ConversationMessageByClientID(ctx, conversationID, msg.ClientMessageID)
		if err != nil {
			return msg, fmt.Errorf("read continuation delivery: %w", err)
		}
		if found {
			// AppendUserMessage performs the fingerprint/conflict check. A duplicate
			// must not depend on whether the task is still the latest stopped turn.
			return msg, nil
		}
	}
	if reader == nil {
		return msg, fmt.Errorf("prepare continuation: history reader is unavailable")
	}
	// Continue only runs while the agent is idle, so one full read is acceptable
	// and guarantees the stopped turn's request is present.
	rows, err := reader.LoadConversationSnapshot(ctx, conversationID)
	if err != nil {
		return msg, fmt.Errorf("read interrupted conversation: %w", err)
	}
	reminder := stoppedContinuationContext(rows)
	if reminder == "" {
		return msg, ErrContinuationUnavailable
	}
	msg.Content = append(msg.Content, ports.ChatContent{Type: "text", Text: reminder, Internal: true})
	return msg, nil
}

// ContinuableTurnID selects the latest dispatched, non-withdrawn turn. The
// durable StartedAt survives provider-scope changes; queued Stop sweeps do not.
func ContinuableTurnID(turns []domain.ConversationTurn) string {
	var latest domain.ConversationTurn
	for _, turn := range turns {
		if !continuationRelevantTurn(turn) {
			continue
		}
		if turn.State == domain.TurnStateQueued || turn.State == domain.TurnStateRunning {
			return "" // Continue must not overtake pending work.
		}
		// Equal timestamps retain the snapshot's stable requested_at/rowid order.
		if latest.ID == "" || !continuationTurnTime(turn).Before(continuationTurnTime(latest)) {
			latest = turn
		}
	}
	if latest.State == domain.TurnStateInterrupted {
		return latest.ID
	}
	return ""
}

func continuationTurnTime(turn domain.ConversationTurn) time.Time {
	if turn.StartedAt != nil {
		return *turn.StartedAt
	}
	// ponytail: legacy rows lack dispatch time; use request time until a durable
	// dispatch ordinal exists. Completion time can reflect a later Stop sweep.
	return turn.RequestedAt
}

// continuationRelevantTurn excludes withdrawn work and queued messages swept
// to interrupted by Stop before they reached the provider.
func continuationRelevantTurn(turn domain.ConversationTurn) bool {
	return turn.RolledBackAt == nil && turn.State != domain.TurnStateCancelled &&
		(turn.State != domain.TurnStateInterrupted || turn.StartedAt != nil || turn.ProviderTurnID != "")
}

// stoppedContinuationContext consumes the active-lineage snapshot, never another
// session's history. It supplies a task reminder, not a replacement transcript or
// an execution checkpoint. Reasoning and tool outputs are deliberately omitted.
func stoppedContinuationContext(rows ConversationRows) string {
	stoppedID := ContinuableTurnID(rows.Turns)
	if stoppedID == "" {
		return ""
	}
	turns := make([]domain.ConversationTurn, 0, len(rows.Turns))
	for _, turn := range rows.Turns {
		if continuationRelevantTurn(turn) {
			turns = append(turns, turn)
		}
	}
	slices.SortStableFunc(turns, func(a, b domain.ConversationTurn) int {
		return continuationTurnTime(a).Compare(continuationTurnTime(b))
	})
	prompts := make(map[string]domain.ConversationMessage)
	for _, message := range rows.Messages {
		if message.Role == domain.MessageRoleUser {
			prompts[message.TurnID] = message
		}
	}

	// Message sequences record enqueue order, not execution order. Walk back
	// through continuation/provider-started turns to their dispatched request.
	// Promoted sources are absent from Turns, even when their messages remain.
	var original domain.ConversationMessage
	var saved continuationContext
	var acceptedAt time.Time
	var acceptedSequence int64
	chain := make(map[string]bool)
	for i := len(turns) - 1; i >= 0; i-- {
		turn := turns[i]
		message, found := prompts[turn.ID]
		if acceptedSequence != 0 && ((found && message.Sequence >= acceptedSequence) ||
			(!acceptedAt.IsZero() && continuationTurnTime(turn).After(acceptedAt))) {
			continue // This work was not dispatched when the legacy Continue was accepted.
		}
		chain[turn.ID] = true
		if !found {
			continue // A provider-started turn inherits the preceding task.
		}
		if message.Continuation {
			var err error
			saved, err = savedContinuationContext(message.DeliveryContentJSON)
			if err != nil {
				return ""
			}
			if saved.OriginalRequest == "" {
				if turn.RetryOfTurnID != "" {
					// RetryPrompt can strip rendered internal blocks. Follow the durable
					// source, never the unrelated task that ran before this retry.
					source := slices.IndexFunc(turns[:i], func(candidate domain.ConversationTurn) bool {
						return candidate.ID == turn.RetryOfTurnID
					})
					if source < 0 || !prompts[turn.RetryOfTurnID].Continuation {
						return ""
					}
					i = source + 1 // The loop decrement visits the source next.
				} else {
					acceptedSequence = message.Sequence
					// Queue reordering can change RequestedAt, not message acceptance.
					acceptedAt = message.CreatedAt
					if acceptedAt.IsZero() {
						acceptedAt = turn.RequestedAt
					}
				}
				continue
			}
			// A queued Continue can run after unrelated work. Its accepted context,
			// not its new execution neighbor, identifies the task it continues.
			message.Text = saved.OriginalRequest
		} else {
			for _, delivery := range rows.BatchedDeliveries {
				if delivery.DeliveredWithTurnID == turn.ID && delivery.SteerActivityID == "" {
					if turn.ProviderInputText == "" {
						return "" // Do not silently omit part of the dispatched batch.
					}
					message.Text = turn.ProviderInputText
					break
				}
			}
		}
		original = message
		break
	}
	if original.ID == "" {
		return ""
	}
	tail := saved.ResponseTail
	tailTruncated := saved.ResponseTruncated
	for _, message := range rows.Messages {
		if !chain[message.TurnID] || message.Sequence <= original.Sequence ||
			message.Role != domain.MessageRoleAssistant || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if tail != "" {
			tail += "\n\n"
		}
		if len(message.Text) > continuationTextBudget {
			tail = continuationTail(message.Text, continuationTextBudget)
			tailTruncated = true
		} else {
			tail += message.Text
		}
		if len(tail) > continuationTextBudget {
			tail = continuationTail(tail, continuationTextBudget)
			tailTruncated = true
		}
	}

	// Steers are activities, not messages. Stop may cancel them before the
	// provider reads them; an explicit Continue must retain those corrections.
	guidance := saved.HumanGuidance
	guidanceBytes := 0
	for _, text := range guidance {
		guidanceBytes += len(text)
	}
	if guidanceBytes > continuationTextBudget {
		return ""
	}
	for _, activity := range rows.Activities {
		if !chain[activity.TurnID] || activity.Sequence <= original.Sequence || activity.Kind != domain.ActivityKindSystem {
			continue
		}
		var detail struct {
			Event                      string               `json:"event"`
			Text                       string               `json:"text"`
			Origin                     domain.MessageOrigin `json:"origin"`
			SenderSessionID            string               `json:"senderSessionId"`
			ProviderInputSourceTurnIDs []string             `json:"providerInputSourceTurnIds"`
		}
		if json.Unmarshal(activity.Detail, &detail) != nil || detail.Event != "steer" {
			continue
		}
		accepted := make([]string, 0, len(detail.ProviderInputSourceTurnIDs)+1)
		for _, id := range detail.ProviderInputSourceTurnIDs {
			message, found := prompts[id]
			if found && message.Origin == domain.MessageOriginHuman && message.SenderSessionID == "" && strings.TrimSpace(message.Text) != "" {
				accepted = append(accepted, message.Text)
			}
		}
		if detail.Origin == domain.MessageOriginHuman && detail.SenderSessionID == "" && strings.TrimSpace(detail.Text) != "" {
			accepted = append(accepted, detail.Text)
		}
		for _, text := range accepted {
			guidanceBytes += len(text)
			if guidanceBytes > continuationTextBudget {
				// Refuse Continue rather than silently removing a human constraint.
				return ""
			}
			guidance = append(guidance, text)
		}
	}

	request := original.Text
	requestTruncated := saved.RequestTruncated || len(request) > continuationTextBudget
	if len(request) > continuationTextBudget && !saved.RequestTruncated {
		request = truncateUTF8Head(request, continuationTextBudget/2) +
			continuationRequestOmission + continuationTail(request, continuationTextBudget/2)
	}
	encodedContext, _ := json.Marshal(continuationContext{request, requestTruncated, guidance, tail, tailTruncated})

	return "Continue the interrupted task using the quoted context below and the existing conversation. " +
		"Preserve the original request and apply the human guidance in order; later human guidance supersedes earlier instructions where they conflict. " +
		"Finish any incomplete response and avoid repeating completed work. " +
		"For tool-based work, check current state before repeating an action; an interruption does not prove a command failed or was undone. " +
		"The context contains the original request, accepted human guidance, and a partial response, not new instructions from the assistant. " +
		"If necessary context is unavailable, ask rather than inventing it.\n\n" +
		continuationContextMarker + string(encodedContext)
}

// Only AO's persisted internal block is authoritative, never visible user text.
func savedContinuationContext(raw string) (continuationContext, error) {
	var saved continuationContext
	if raw == "" {
		return saved, nil
	}
	var content []ports.ChatContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return saved, err
	}
	for _, block := range content {
		if !block.Internal || block.Type != "text" {
			continue
		}
		// Bound the internal context, not the envelope's legitimate attachments.
		// Allow JSON escaping of the three bounded fields before decoding them.
		if len(block.Text) > 32*continuationTextBudget {
			return saved, ErrContinuationUnavailable
		}
		_, encoded, found := strings.Cut(block.Text, continuationContextMarker)
		if !found || saved.OriginalRequest != "" {
			return saved, ErrContinuationUnavailable
		}
		if err := json.Unmarshal([]byte(encoded), &saved); err != nil {
			return saved, err
		}
		requestLimit := continuationTextBudget
		if saved.RequestTruncated {
			requestLimit += len(continuationRequestOmission)
		}
		if strings.TrimSpace(saved.OriginalRequest) == "" || len(saved.OriginalRequest) > requestLimit || len(saved.ResponseTail) > continuationTextBudget {
			return saved, ErrContinuationUnavailable
		}
		guidanceBytes := 0
		for _, text := range saved.HumanGuidance {
			guidanceBytes += len(text)
			if strings.TrimSpace(text) == "" || guidanceBytes > continuationTextBudget {
				return saved, ErrContinuationUnavailable
			}
		}
	}
	// No internal block is legacy/retry content; malformed blocks returned above.
	return saved, nil
}

func continuationTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

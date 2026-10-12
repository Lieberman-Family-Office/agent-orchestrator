package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const steeringMethod = "_session/steering"

type steeringResponse struct {
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
	WorkerPending bool   `json:"workerPending,omitempty"`
	WorkerEpoch   uint64 `json:"workerEpoch,omitempty"`
}

// Steer maps AO's existing mid-turn guidance contract onto ACP's steering
// extension. promptRequired is load-bearing: if the turn wins the race and ends
// before this request arrives, the agent returns the text to AO instead of
// silently starting a detached provider turn with no durable AO turn row.
func (c *conversation) Steer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	if strings.TrimSpace(msg.Text) == "" {
		return ports.ChatTurnRef{}, errors.New("steer message text is empty")
	}
	prompt, err := c.promptContent(msg)
	if err != nil {
		return ports.ChatTurnRef{}, fmt.Errorf("%w: %w", ports.ErrChatSteerContentUnsupported, err)
	}

	c.mu.Lock()
	activeTurn := c.activeTurn
	sessionID := c.sessionID
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ports.ChatTurnRef{}, errConversationClosed
	}
	if providerTurnID == "" {
		providerTurnID = activeTurn
	}
	if providerTurnID == "" || providerTurnID != activeTurn {
		return ports.ChatTurnRef{}, ports.ErrChatNoSteerableTurn
	}

	steering := map[string]any{"idleBehavior": "promptRequired"}
	if c.harness == domain.HarnessClaudeCode {
		if msg.Origin == domain.MessageOriginAutomation && msg.SenderSessionID != "" {
			if !c.workerSafeBoundary {
				return ports.ChatTurnRef{}, fmt.Errorf("%w: safe worker steering is unavailable", ports.ErrChatTurnNotSteerable)
			}
			steering["senderSessionId"] = msg.SenderSessionID
		} else if !c.humanSafeBoundary {
			return ports.ChatTurnRef{}, fmt.Errorf("%w: safe human steering is unavailable", ports.ErrChatTurnNotSteerable)
		}
		steering["priority"] = "next"
	}
	raw, err := c.conn.CallExtension(ctx, steeringMethod, map[string]any{
		"sessionId": sessionID,
		"prompt":    prompt,
		"_meta": map[string]any{
			"steering": steering,
		},
	})
	if err != nil {
		return ports.ChatTurnRef{}, fmt.Errorf("ACP %s: %w", steeringMethod, err)
	}
	var response steeringResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return ports.ChatTurnRef{}, fmt.Errorf("decode ACP steering response: %w", err)
	}
	switch response.Outcome {
	case "injected":
		if steering["priority"] == "next" && response.WorkerPending {
			c.yieldWorkerQuestions(response.WorkerEpoch)
		}
		return ports.ChatTurnRef{ProviderTurnID: providerTurnID}, nil
	case "promptRequired":
		return ports.ChatTurnRef{}, ports.ErrChatNoSteerableTurn
	case "startedNewTurn":
		// AO explicitly requested promptRequired, so this means the extension
		// contract was not honored. Claiming this joined the active turn would
		// misattribute the provider's detached work in durable history.
		return ports.ChatTurnRef{}, fmt.Errorf("ACP steering started a detached turn")
	default:
		return ports.ChatTurnRef{}, fmt.Errorf("ACP steering returned unknown outcome %q", response.Outcome)
	}
}

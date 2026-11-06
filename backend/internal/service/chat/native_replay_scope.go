package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// nativeReplayCandidates keeps all prompt matching evidence, then reads complete
// activities only for turns named by that evidence or stable provider items.
func (s *Service) nativeReplayCandidates(ctx context.Context, harness domain.AgentHarness, active domain.ConversationBranch, events []ports.ChatEvent) (ConversationRows, error) {
	if len(events) == 0 {
		return ConversationRows{}, nil
	}
	turns, err := s.store.ConversationTurns(ctx, active.ConversationID)
	if err != nil {
		return ConversationRows{}, err
	}
	messages, err := s.store.ConversationMessages(ctx, active.ConversationID)
	if err != nil {
		return ConversationRows{}, err
	}
	rows, err := s.nativeReplayRows(ctx, active, ConversationRows{Turns: turns, Messages: messages})
	if err != nil {
		return ConversationRows{}, err
	}
	var itemIDs []string
	for _, event := range events {
		if event.ProviderItemID != "" {
			itemIDs = append(itemIDs, event.ProviderItemID)
		}
		itemIDs = append(itemIDs, event.ProviderItemAliases...)
	}
	rows.Activities, err = s.store.ConversationReplayActivities(ctx, active.ConversationID, nil, itemIDs)
	if err != nil {
		return ConversationRows{}, err
	}
	rows, err = s.nativeReplayRows(ctx, active, rows)
	if err != nil {
		return ConversationRows{}, err
	}
	mapped, _ := indexNativeHistoryTurnsForHarness(harness, rows.Turns, rows.Messages, rows.Activities).mapReplay(events)
	matched := make(map[string]bool, len(mapped))
	for _, turn := range mapped {
		matched[turn.providerTurnID] = true
	}
	var turnIDs []string
	for _, turn := range rows.Turns {
		if matched[turn.ProviderTurnID] {
			turnIDs = append(turnIDs, turn.ID)
		}
	}
	rows.Activities, err = s.store.ConversationReplayActivities(ctx, active.ConversationID, turnIDs, itemIDs)
	return rows, err
}

// nativeReplayRows excludes predecessor providers from reconciliation, including
// on ordinary restarts after a handoff. Identical text and opaque IDs from two
// independent native contexts must never cause one to be deduplicated as the
// other. Same-scope edit branches still share reconciliation history.
func (s *Service) nativeReplayRows(ctx context.Context, active domain.ConversationBranch, rows ConversationRows) (ConversationRows, error) {
	if active.ProviderScopeID == "" {
		return rows, nil // legacy unscoped conversation
	}
	branches := map[string]bool{active.ID: true}
	turns := make(map[string]bool)
	filtered := ConversationRows{}
	for _, turn := range rows.Turns {
		eligible, known := branches[turn.BranchID]
		if !known {
			branch, err := s.store.ConversationBranch(ctx, active.ConversationID, turn.BranchID)
			if err != nil {
				return ConversationRows{}, fmt.Errorf("read native replay ownership: %w", err)
			}
			eligible = branch.ProviderScopeID == active.ProviderScopeID
			branches[turn.BranchID] = eligible
		}
		if eligible {
			filtered.Turns = append(filtered.Turns, turn)
			turns[turn.ID] = true
		}
	}
	for _, message := range rows.Messages {
		if turns[message.TurnID] {
			filtered.Messages = append(filtered.Messages, message)
		}
	}
	for _, activity := range rows.Activities {
		if turns[activity.TurnID] {
			filtered.Activities = append(filtered.Activities, activity)
		}
	}
	return filtered, nil
}

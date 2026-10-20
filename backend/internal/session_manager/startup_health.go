package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrWorkspaceWriterStopUnproven preserves an incomplete published workspace
// until recovery can establish that its prior setup writers stopped.
var ErrWorkspaceWriterStopUnproven = fmt.Errorf("%w: prior setup writer stop is unproven; preserve workspace and resolve prior writers before manual recovery", ErrIncompleteHandle)

// uncertainWorkspaceLaunch recognizes an early publication, not a completed
// controller whose native identity should follow normal recovery. Setup=done
// proves the command returned, not that all of its descendants stopped. A Chat
// generation reservation precedes ControllerReady; it is not publication proof.
// ponytail: No durable setup execution owner exists; add native ownership only
// when automatic recovery must prove every prior workspace writer stopped.
func uncertainWorkspaceLaunch(rec domain.SessionRecord) bool {
	incomplete := (rec.ClientRequestID != "" && !rec.ClientRequestCommitted) ||
		slices.ContainsFunc(rec.ProvisionSteps, func(step domain.SessionProvisionStep) bool {
			return step.ID == domain.SessionProvisionStepAgent && step.Status != domain.SessionProvisionStepDone
		})
	restarted := rec.ProvisionState == domain.SessionProvisionFailed && rec.ProvisionError == interruptedProvisioningError
	if rec.ProvisionState == domain.SessionProvisionFailed && !restarted {
		return false // Keep retries of known in-process failures unchanged.
	}
	return (incomplete || restarted) && rec.Metadata.WorkspacePath != "" && (rec.Metadata.Prompt == "" || restarted) &&
		rec.Metadata.RuntimeHandleID == "" && rec.Metadata.RuntimeLaunchID == "" &&
		rec.Metadata.ProviderConversationID == "" &&
		rec.Metadata.AgentSessionID == "" && rec.Metadata.AgentSessionIDLaunchID == ""
}

// checkSessionHealth observes existing controllers. Startup must never create a
// provider, restore a worktree, or replay a task to discover whether it is alive.
func (m *Manager) checkSessionHealth(ctx context.Context, rec domain.SessionRecord) error {
	if rec.ProvisionState.WithDefault() != domain.SessionProvisionReady {
		return nil
	}
	if uncertainWorkspaceLaunch(rec) || unfinishedWorkspaceSetup(rec) {
		return fmt.Errorf("check session %s: %w", rec.ID, ErrWorkspaceWriterStopUnproven)
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return err
	}
	if interruptedClientSetup(rec) {
		release := sync.OnceFunc(m.acquireWorkspaceGate(rec.ProjectID))
		defer release()
		ctx = context.WithValue(ctx, spawnWorkspaceGateKey{}, release)
		current, found, err := m.store.GetSession(ctx, rec.ID)
		if err != nil || !found {
			return err
		}
		// An initial spawn can finish while this pass waits for the project gate.
		if current.CreatedAt.Equal(rec.CreatedAt) && interruptedClientSetup(current) {
			var info *ports.WorkspaceProjectInfo
			if project.Kind.WithDefault() == domain.ProjectKindWorkspace {
				rows, _, err := m.workspaceProjectRows(ctx, current)
				if err != nil {
					return err
				}
				info = &ports.WorkspaceProjectInfo{Root: workspaceInfo(current), Worktrees: rows}
			}
			m.rollbackSeedSpawnWorkspace(ctx, current, workspaceInfo(current), info, false, false)
			return nil
		}
		rec = current
	}
	// Preserve cleanup of interrupted spawns that never committed a workspace.
	// Their client request IDs must remain retryable after a daemon crash.
	if rec.Metadata.WorkspacePath == "" ||
		(rec.Metadata.Branch == "" && projectKindForSession(project, rec.ProjectID) != domain.ProjectKindScratch) {
		m.rollbackSpawnSeedRow(ctx, rec.ID)
		return nil
	}
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		if m.chat == nil {
			return ports.ErrChatUnsupported
		}
		if m.chat.HasLiveChatController(rec.ID) {
			return nil
		}
		if rec.Metadata.ProviderConversationID == "" {
			return fmt.Errorf("check session %s: %w", rec.ID, ErrIncompleteHandle)
		}
		_, err = m.resumeChatController(ctx, "check session", rec, project,
			workspaceInfo(rec), false, true, "", domain.SessionInterfaceTransitionHistoryStrict)
		if errors.Is(err, ports.ErrChatHostNotRunning) {
			return m.recordAgentExited(ctx, rec)
		}
		return err
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		return m.recordAgentExited(ctx, rec)
	}
	alive, err := m.runtime.IsAlive(ctx, handle)
	if err != nil {
		return fmt.Errorf("check session %s: %w", rec.ID, err)
	}
	if alive {
		// The start hook may fail without stopping the TUI. Keep a live runtime
		// intact; its unverified identity will not qualify for native restore.
		return nil
	}
	return m.recordAgentExited(ctx, rec)
}

// Setup's durable completion is written before any controller may launch.
func interruptedClientSetup(rec domain.SessionRecord) bool {
	return rec.ClientRequestID != "" && !rec.ClientRequestCommitted && !rec.IsTerminated &&
		rec.Metadata.RuntimeHandleID == "" && rec.Metadata.ProviderConversationID == "" &&
		slices.ContainsFunc(rec.ProvisionSteps, func(step domain.SessionProvisionStep) bool {
			return step.ID == domain.SessionProvisionStepSetup && step.Status == domain.SessionProvisionStepRunning
		})
}

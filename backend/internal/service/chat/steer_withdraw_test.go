package chat_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// waitingSteerer is a provider whose Steer waits for the agent to collect the
// guidance, the way a drain agent such as Qwen Code does, until it is withdrawn.
type waitingSteerer struct {
	*steerRecorder
	parked       chan struct{}
	withdrawn    chan struct{}
	parkOnce     sync.Once
	withdrawOnce sync.Once
}

func (w *waitingSteerer) Steer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (ports.ChatTurnRef, error) {
	if _, err := w.steerRecorder.Steer(ctx, providerTurnID, msg); err != nil {
		return ports.ChatTurnRef{}, err
	}
	w.parkOnce.Do(func() { close(w.parked) })
	select {
	case <-w.withdrawn:
		return ports.ChatTurnRef{}, ports.ErrChatTurnNotSteerable
	case <-ctx.Done():
		return ports.ChatTurnRef{}, ctx.Err()
	case <-time.After(time.Minute):
		return ports.ChatTurnRef{ProviderTurnID: providerTurnID}, nil
	}
}

func (w *waitingSteerer) WithdrawPendingSteers() func() {
	w.withdrawOnce.Do(func() { close(w.withdrawn) })
	return nil
}

// A steer waiting on the agent holds the dispatch lock. Stop must withdraw it
// rather than wait for it to time out.
func TestInterruptWithdrawsASteerWaitingForTheAgent(t *testing.T) {
	provider := &waitingSteerer{
		steerRecorder: newSteerRecorder(),
		parked:        make(chan struct{}),
		withdrawn:     make(chan struct{}),
	}
	h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store { return st })
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "do the long thing", ClientMessageID: "turn-1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})

	steered := make(chan error, 1)
	go func() {
		_, err := h.svc.Steer(ctx, testSession, ports.ChatUserMessage{
			Text: "use staging instead", ClientMessageID: "steer-1", Origin: domain.MessageOriginHuman,
		})
		steered <- err
	}()
	select {
	case <-provider.parked:
	case <-time.After(5 * time.Second):
		t.Fatal("steer never reached the provider")
	}

	interrupted := make(chan error, 1)
	go func() { interrupted <- h.svc.Interrupt(ctx, testSession) }()
	select {
	case err := <-interrupted:
		if err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited behind the steer")
	}
	if err := <-steered; !errors.Is(err, chatsvc.ErrTurnNotSteerable) {
		t.Fatalf("Steer error = %v, want ErrTurnNotSteerable", err)
	}
}

type interruptSendSteerer struct {
	*waitingSteerer
	withdrawals atomic.Int32
	restores    atomic.Int32
	attempts    chan string
	interrupt   func(context.Context) error
	onWithdraw  func()
}

func (p *interruptSendSteerer) WithdrawPendingSteers() func() {
	p.withdrawals.Add(1)
	p.waitingSteerer.WithdrawPendingSteers()
	if p.onWithdraw != nil {
		p.onWithdraw()
	}
	return sync.OnceFunc(func() { p.restores.Add(1) })
}

func (p *interruptSendSteerer) Interrupt(ctx context.Context, target string) error {
	p.attempts <- target
	if p.interrupt != nil {
		return p.interrupt(ctx)
	}
	return nil
}

func newInterruptSendSteerer() *interruptSendSteerer {
	return &interruptSendSteerer{
		waitingSteerer: &waitingSteerer{
			steerRecorder: newSteerRecorder(), parked: make(chan struct{}), withdrawn: make(chan struct{}),
		},
		attempts: make(chan string, 8),
	}
}

func TestInterruptAndSendWithdrawsWaitingSteerAndLeavesQueueIntact(t *testing.T) {
	provider := newInterruptSendSteerer()
	h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store {
		return &interruptSendFailureStore{Store: st}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	running, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "running"})
	if err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: running.ProviderTurnID})
	h.awaitSnapshot(t, func(store.ConversationSnapshot) bool { return h.ctrl.State() == ports.ChatControllerBusy })
	if _, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "older queue"}); err != nil {
		t.Fatal(err)
	}
	steered := make(chan error, 1)
	go func() {
		_, err := h.ctrl.Steer(ctx, ports.ChatUserMessage{Text: "waiting guidance", ClientMessageID: "waiting-steer"})
		steered <- err
	}()
	select {
	case <-provider.parked:
	case <-ctx.Done():
		t.Fatal("steer did not reach provider")
	}
	request := ports.ChatUserMessage{Text: "priority", ClientMessageID: "priority", InterruptAndSend: true}
	priority, err := h.ctrl.Send(ctx, request)
	if err != nil || priority.InterruptTargetProviderTurnID != running.ProviderTurnID {
		t.Fatalf("interrupt-and-send: %+v, %v", priority, err)
	}
	if err := <-steered; !errors.Is(err, chatsvc.ErrTurnNotSteerable) {
		t.Fatalf("withdrawn steer: %v", err)
	}
	select {
	case target := <-provider.attempts:
		if target != running.ProviderTurnID {
			t.Fatalf("interrupted %q instead of original turn", target)
		}
	default:
		t.Fatal("provider cancellation was not attempted")
	}
	if provider.withdrawals.Load() == 0 || provider.restores.Load() != 0 {
		t.Fatal("accepted cancellation did not retain its steering fence")
	}
	withdrawals := provider.withdrawals.Load()
	if _, err := h.ctrl.Send(ctx, request); err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: running.ProviderTurnID, TurnState: domain.TurnStateInterrupted})
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		turn, _ := turnByID(s, priority.ID)
		return turn.State == domain.TurnStateRunning && turn.ProviderTurnID != ""
	})
	if turnStateByText(t, snapshot)["older queue"] != domain.TurnStateQueued {
		t.Fatal("interrupt-and-send cancelled or dispatched the older queue")
	}
	if _, err := h.ctrl.Send(ctx, request); err != nil {
		t.Fatal(err)
	}
	if provider.withdrawals.Load() != withdrawals {
		t.Fatal("duplicate retry withdrew steering from an accepted or successor turn")
	}
	select {
	case target := <-provider.attempts:
		t.Fatalf("duplicate retry interrupted %q", target)
	default:
	}
}

type interruptSendFailureStore struct {
	chatsvc.Store
	appendErr error
	markErr   error
}

// These live-send tests start empty; startup recovery must not consume their new intents.
func (s *interruptSendFailureStore) PendingInterruptTurns(context.Context, string) ([]domain.ConversationTurn, error) {
	return nil, nil
}

func (s *interruptSendFailureStore) AppendUserMessage(ctx context.Context, conversationID string, session domain.SessionID, generation string, msg domain.ConversationMessage, turnID string, now time.Time) (bool, error) {
	if msg.InterruptTargetProviderTurnID != "" && s.appendErr != nil {
		return false, s.appendErr
	}
	return s.Store.AppendUserMessage(ctx, conversationID, session, generation, msg, turnID, now)
}

func (s *interruptSendFailureStore) MarkTurnInterruptDelivered(ctx context.Context, turnID string) error {
	if s.markErr != nil {
		return s.markErr
	}
	return s.Store.MarkTurnInterruptDelivered(ctx, turnID)
}

func TestInterruptAndSendRestoresOnlyUnacceptedWithdrawals(t *testing.T) {
	for _, scenario := range []string{"admission", "cancelled-admission", "provider-refusal", "cancelled-interrupt", "no-active-turn", "receipt-failure"} {
		t.Run(scenario, func(t *testing.T) {
			provider := newInterruptSendSteerer()
			failure := errors.New("injected interrupt-send failure")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store {
				wrapped := &interruptSendFailureStore{Store: st}
				if scenario == "admission" {
					wrapped.appendErr = failure
				}
				if scenario == "receipt-failure" {
					wrapped.markErr = failure
				}
				return wrapped
			})
			running, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "running"})
			if err != nil {
				t.Fatal(err)
			}
			provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: running.ProviderTurnID})
			wantErr := failure
			switch scenario {
			case "cancelled-admission":
				provider.onWithdraw = cancel
				wantErr = context.Canceled
			case "provider-refusal":
				provider.interrupt = func(context.Context) error { return failure }
			case "cancelled-interrupt":
				provider.interrupt = func(ctx context.Context) error {
					cancel()
					return ctx.Err()
				}
				wantErr = context.Canceled
			case "no-active-turn":
				provider.interrupt = func(context.Context) error { return ports.ErrChatNoActiveTurn }
				wantErr = nil
			}
			turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "priority", ClientMessageID: "priority", InterruptAndSend: true})
			if !errors.Is(err, wantErr) {
				t.Fatalf("interrupt-and-send error=%v, want %v", err, wantErr)
			}
			admitted := scenario != "admission" && scenario != "cancelled-admission"
			if admitted != (turn.ID != "") {
				t.Fatalf("durable admission=%v, turn=%+v", admitted, turn)
			}
			if admitted && wantErr != nil && !errors.Is(err, chatsvc.ErrInterruptSendUncertain) {
				t.Fatalf("accepted prompt lost uncertain outcome: %v", err)
			}
			withdrawals, restores := provider.withdrawals.Load(), provider.restores.Load()
			wantWithdrawals := int32(1)
			if admitted {
				wantWithdrawals = 2
			}
			if withdrawals != wantWithdrawals {
				t.Fatalf("withdrawals=%d, want %d", withdrawals, wantWithdrawals)
			}
			if scenario == "receipt-failure" {
				if restores != 0 {
					t.Fatal("accepted provider cancellation restored steering after receipt failure")
				}
			} else if restores != withdrawals {
				t.Fatalf("unaccepted withdrawals=%d, restores=%d", withdrawals, restores)
			}
			select {
			case target := <-provider.attempts:
				if !admitted || target != running.ProviderTurnID {
					t.Fatalf("cancellation before admission or against wrong target: %q", target)
				}
			default:
				if admitted {
					t.Fatal("accepted prompt did not attempt cancellation")
				}
			}
		})
	}
}

func TestInterruptAndSendRetryOfFailedTargetDoesNotWithdrawSuccessor(t *testing.T) {
	provider := newInterruptSendSteerer()
	provider.interrupt = func(context.Context) error { return ports.ErrChatNoActiveTurn }
	h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store {
		return &interruptSendFailureStore{Store: st}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	original, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "original"})
	if err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: original.ProviderTurnID})
	request := ports.ChatUserMessage{Text: "held priority", ClientMessageID: "held-priority", InterruptAndSend: true}
	priority, err := h.ctrl.Send(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: original.ProviderTurnID, TurnState: domain.TurnStateFailed})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		turn, _ := turnByID(s, original.ID)
		return turn.State == domain.TurnStateFailed
	})
	if _, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "successor"}); err != nil {
		t.Fatal(err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["successor"] == domain.TurnStateRunning
	})
	withdrawals, attempts := provider.withdrawals.Load(), len(provider.attempts)
	if replay, err := h.ctrl.Send(ctx, request); err != nil || replay.ID != priority.ID {
		t.Fatalf("failed-target retry: %+v, %v", replay, err)
	}
	if provider.withdrawals.Load() != withdrawals || len(provider.attempts) != attempts {
		t.Fatal("old queued retry withdrew or interrupted its successor")
	}
}

func TestInterruptAndSendWithoutActiveTurnDoesNotWithdrawSteering(t *testing.T) {
	provider := newInterruptSendSteerer()
	h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store {
		return &interruptSendFailureStore{Store: st}
	})
	request := ports.ChatUserMessage{Text: "first", ClientMessageID: "first", InterruptAndSend: true}
	first, err := h.ctrl.Send(t.Context(), request)
	if err != nil || first.ID == "" {
		t.Fatalf("idle interrupt-and-send: %+v, %v", first, err)
	}
	if replay, err := h.ctrl.Send(t.Context(), request); err != nil || replay.ID != first.ID {
		t.Fatalf("idle retry: %+v, %v", replay, err)
	}
	if provider.withdrawals.Load() != 0 || len(provider.attempts) != 0 || provider.sendCallCount() != 1 {
		t.Fatal("idle send or retry withdrew steering, cancelled, or duplicated its own turn")
	}
}

// failingStopSteerer refuses to interrupt, as a provider whose cancel did not
// get through does, and records whether Stop gave steering back.
type failingStopSteerer struct {
	*waitingSteerer
	restored chan struct{}
}

func (f *failingStopSteerer) Interrupt(context.Context, string) error {
	return errors.New("session/cancel did not reach the agent")
}

func (f *failingStopSteerer) WithdrawPendingSteers() func() {
	f.waitingSteerer.WithdrawPendingSteers()
	return func() {
		select {
		case <-f.restored:
		default:
			close(f.restored)
		}
	}
}

// Stop withdraws the turn's steers before it interrupts. When the interrupt
// fails the turn keeps running, so Stop gives steering back.
func TestInterruptThatFailsRestoresSteering(t *testing.T) {
	provider := &failingStopSteerer{
		waitingSteerer: &waitingSteerer{
			steerRecorder: newSteerRecorder(),
			parked:        make(chan struct{}),
			withdrawn:     make(chan struct{}),
		},
		restored: make(chan struct{}),
	}
	h := newHarnessWithConversationAndStore(t, provider, func(st *store.Store) chatsvc.Store { return st })
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "do the long thing", ClientMessageID: "turn-1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})

	if err := h.svc.Interrupt(ctx, testSession); err == nil {
		t.Fatal("Interrupt succeeded although the provider refused it")
	}
	select {
	case <-provider.withdrawn:
	default:
		t.Fatal("Stop did not withdraw the turn's steers first")
	}
	select {
	case <-provider.restored:
	default:
		t.Fatal("Stop that failed did not give steering back")
	}
}

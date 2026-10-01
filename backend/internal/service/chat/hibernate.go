package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// View leases survive a missed renderer heartbeat but expire after a crash.
const chatViewLease = 30 * time.Second

type hibernationStore interface {
	SetSessionHibernated(context.Context, domain.SessionID, int64, *time.Time) (bool, error)
}

// SetWakeCallback connects a cold Chat session to Session Manager's native
// resume path. It is installed after both services have been constructed.
func (s *Service) SetWakeCallback(wake func(context.Context, domain.SessionID) error) {
	s.wakeChat = wake
}

// SetHibernateCallback connects a closed Chat view to Session Manager's
// operation gate. The periodic sweep also catches turns that finish later.
func (s *Service) SetHibernateCallback(hibernate func(context.Context, domain.SessionID) error) {
	s.hibernateChat = hibernate
}

// SetChatView records a short-lived view lease. Registration and the final
// hibernation check use the same controller gate, so opening a view either
// prevents shutdown or waits for shutdown and then wakes the native session.
func (s *Service) SetChatView(ctx context.Context, id domain.SessionID, viewID string, active bool) error {
	gate := s.controllerGate(domain.SessionConversationOwner(id))
	if err := gate.lock(ctx); err != nil {
		return err
	}
	if !active {
		remaining := s.setViewLease(id, viewID, false)
		gate.unlock()
		if !remaining && s.hibernateChat != nil {
			return s.hibernateChat(context.WithoutCancel(ctx), id)
		}
		return nil
	}
	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		gate.unlock()
		return err
	}
	if rec.IsTerminated {
		s.viewMu.Lock()
		delete(s.viewLeases, id)
		s.viewMu.Unlock()
		gate.unlock()
		return nil
	}
	s.setViewLease(id, viewID, true)
	gate.unlock()
	if !s.hasChatView(id) {
		return nil
	}
	if rec.ProvisionState.WithDefault() != domain.SessionProvisionReady ||
		rec.Metadata.ProviderConversationID == "" ||
		(rec.HibernatedAt == nil && s.HasLiveChatController(id)) {
		return nil
	}
	err = s.wakeHibernated(ctx, id)
	// A slow native reconnect can outlive one lease interval. Extend this
	// viewer's lease only if a concurrent leave has not removed it.
	s.viewMu.Lock()
	if views := s.viewLeases[id]; views != nil {
		if _, present := views[viewID]; present {
			views[viewID] = s.now().Add(chatViewLease)
		}
	}
	s.viewMu.Unlock()
	// A leave may have raced with the provider reconnect. If this was the
	// final view, close it now rather than keeping an unused process warm.
	if !s.hasChatView(id) && s.hibernateChat != nil {
		_ = s.hibernateChat(context.WithoutCancel(ctx), id)
	}
	return err
}

func (s *Service) setViewLease(id domain.SessionID, viewID string, active bool) bool {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	views := s.liveViewLeasesLocked(id)
	if active {
		if views == nil {
			views = make(map[string]time.Time)
			if s.viewLeases == nil {
				s.viewLeases = make(map[domain.SessionID]map[string]time.Time)
			}
			s.viewLeases[id] = views
		}
		views[viewID] = s.now().Add(chatViewLease)
	} else {
		delete(views, viewID)
	}
	if len(views) == 0 {
		delete(s.viewLeases, id)
		return false
	}
	return true
}

func (s *Service) hasChatView(id domain.SessionID) bool {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	return len(s.liveViewLeasesLocked(id)) != 0
}

// Callers hold viewMu. Expired leases cannot keep a provider alive after a
// renderer crash or a missed release request.
func (s *Service) liveViewLeasesLocked(id domain.SessionID) map[string]time.Time {
	views := s.viewLeases[id]
	now := s.now()
	for key, expiry := range views {
		if !expiry.After(now) {
			delete(views, key)
		}
	}
	if len(views) == 0 {
		delete(s.viewLeases, id)
		return nil
	}
	return views
}

// HibernateChat stops a quiescent provider without ending its AO session. A
// false result means the final locked eligibility check found useful work.
func (s *Service) HibernateChat(ctx context.Context, id domain.SessionID) (bool, error) {
	marker, ok := s.sessions.(hibernationStore)
	if !ok {
		return false, errors.New("chat hibernation store is unavailable")
	}
	owner := domain.SessionConversationOwner(id)
	gate := s.controllerGate(owner)
	if err := gate.lock(ctx); err != nil {
		return false, err
	}
	defer gate.unlock()

	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		return false, err
	}
	if !hibernateSessionEligible(rec) || s.hasChatView(id) {
		return false, nil
	}
	controller, err := s.Controller(id)
	if err != nil || controller.State() != ports.ChatControllerReady ||
		!controller.Capabilities().Has(ports.ChatCapabilityResume) {
		return false, nil
	}
	hibernator, ok := controller.conv.(ports.ChatProviderHibernator)
	if !ok {
		return false, nil
	}
	if s.reader == nil {
		return false, errors.New("chat hibernation snapshot reader is unavailable")
	}

	// Send and provider lifecycle projection use the same dispatch lock. Fence
	// intake only after verifying the durable queue and latest primary turn.
	controller.sendMu.Lock()
	controller.mu.Lock()
	busy := controller.state != ports.ChatControllerReady ||
		controller.handoff != controllerHandoffNone ||
		controller.pendingTurnID != "" || controller.dispatchingTurnID != "" ||
		controller.compactionPending || controller.pendingTitle != ""
	controller.mu.Unlock()
	if busy {
		controller.sendMu.Unlock()
		return false, nil
	}
	if _, err := s.store.NextQueuedTurn(ctx, controller.conversation.ID); err == nil {
		controller.sendMu.Unlock()
		return false, nil
	} else if !errors.Is(err, domain.ErrNoQueuedTurn) {
		controller.sendMu.Unlock()
		return false, fmt.Errorf("check queued chat turns: %w", err)
	}
	if running, err := s.store.ListVisibleRunningTurnProviderIDs(ctx, controller.conversation.ID); err != nil {
		controller.sendMu.Unlock()
		return false, fmt.Errorf("check running chat turns: %w", err)
	} else if len(running) != 0 {
		controller.sendMu.Unlock()
		return false, nil
	}
	if pending, err := s.store.HasPendingConversationInteractions(ctx, controller.conversation.ID); err != nil {
		controller.sendMu.Unlock()
		return false, fmt.Errorf("check pending chat interactions: %w", err)
	} else if pending {
		controller.sendMu.Unlock()
		return false, nil
	}
	rows, err := s.reader.LoadConversationSnapshot(ctx, controller.conversation.ID)
	if err != nil {
		controller.sendMu.Unlock()
		return false, fmt.Errorf("check latest chat turn: %w", err)
	}
	if !latestPrimaryTurnCompleted(rows, id) {
		controller.sendMu.Unlock()
		return false, nil
	}
	// Activity from outside Chat can change while the snapshot is loaded. Recheck
	// before the provider is stopped; the post-stop CAS is only a crash fence.
	fresh, err := s.requireChatSession(ctx, id)
	if err != nil {
		controller.sendMu.Unlock()
		return false, err
	}
	if !hibernateSessionEligible(fresh) {
		controller.sendMu.Unlock()
		return false, nil
	}
	if s.hibernationEnabled == nil || !s.hibernationEnabled() {
		controller.sendMu.Unlock()
		return false, nil
	}
	controller.mu.Lock()
	controller.handoff = controllerHandoffHibernate
	controller.suppressStoppedActivity = true
	controller.mu.Unlock()
	controller.sendMu.Unlock()

	// Closing the host can wait on provider event projection, so do not retain
	// sendMu while the process exits. The intake fence blocks new dispatches.
	if err := hibernator.Hibernate(); err != nil {
		controller.reportFailedBranchHandoff(ctx)
		controller.AbortHandoff()
		return false, fmt.Errorf("hibernate chat provider: %w", err)
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	marked := false
	defer func() {
		if !marked {
			controller.reportFailedBranchHandoff(finishCtx)
		}
	}()
	select {
	case <-controller.stopped:
	case <-finishCtx.Done():
		return false, fmt.Errorf("wait for hibernated chat controller: %w", finishCtx.Err())
	}

	// Process shutdown precedes the durable marker. A crash in this gap may
	// cause startup to reconnect, but can never strand a live process as cold.
	for range 3 {
		fresh, err := s.requireChatSession(finishCtx, id)
		if err != nil {
			return false, err
		}
		if !hibernateSessionEligible(fresh) {
			return false, nil
		}
		at := s.now()
		applied, err := marker.SetSessionHibernated(finishCtx, id, fresh.Revision, &at)
		if err != nil {
			return false, err
		}
		if applied {
			marked = true
			s.log.Info("chat session hibernated", "session", id, "harness", fresh.Harness)
			return true, nil
		}
	}
	return false, errors.New("chat hibernation marker changed concurrently")
}

func hibernateSessionEligible(rec domain.SessionRecord) bool {
	return !rec.IsTerminated && !rec.IsTaskPreparation && rec.HibernatedAt == nil &&
		domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat &&
		rec.ProvisionState.WithDefault() == domain.SessionProvisionReady &&
		rec.Activity.State == domain.ActivityIdle &&
		!rec.Activity.LastActivityAt.IsZero() &&
		rec.Metadata.ProviderConversationID != ""
}

// Explicit controller teardown (kill or interface switch) consumes the cold
// marker so a later Chat controller cannot inherit a stale sleep state.
func (s *Service) clearHibernation(ctx context.Context, id domain.SessionID) error {
	if s.sessions == nil {
		return nil
	}
	marker, ok := s.sessions.(hibernationStore)
	for range 3 {
		rec, found, err := s.sessions.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return err
		}
		if !ok {
			return errors.New("chat hibernation store is unavailable")
		}
		cleared, err := marker.SetSessionHibernated(ctx, id, rec.Revision, nil)
		if err != nil {
			return err
		}
		if cleared {
			return nil
		}
	}
	return errors.New("chat hibernation marker changed concurrently")
}

// The most recent user prompt must belong to this controller, have a durable
// successful completion. An idle status following a failed or interrupted turn
// is deliberately insufficient.
func latestPrimaryTurnCompleted(rows ConversationRows, id domain.SessionID) bool {
	turns := make(map[string]domain.ConversationTurn, len(rows.Turns))
	for _, turn := range rows.Turns {
		turns[turn.ID] = turn
	}
	for i := len(rows.Messages) - 1; i >= 0; i-- {
		message := rows.Messages[i]
		if message.Role != domain.MessageRoleUser {
			continue
		}
		turn, ok := turns[message.TurnID]
		return ok && turn.HandledBySessionID == id && turn.State == domain.TurnStateCompleted &&
			turn.CompletedAt != nil
	}
	return false
}

// Provider catalog reads are passive: opening a chat must not wake it. Hold
// the same gate as hibernation so a provider cannot stop during the read.
func (s *Service) readingController(ctx context.Context, id domain.SessionID) (*Controller, domain.SessionRecord, func(), error) {
	gate := s.controllerGate(domain.SessionConversationOwner(id))
	if err := gate.lock(ctx); err != nil {
		return nil, domain.SessionRecord{}, nil, err
	}
	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		gate.unlock()
		return nil, domain.SessionRecord{}, nil, err
	}
	if rec.HibernatedAt != nil {
		gate.unlock()
		return nil, domain.SessionRecord{}, nil, ErrNoController
	}
	controller, err := s.Controller(id)
	if err != nil || controller.State() == ports.ChatControllerStopped {
		gate.unlock()
		return nil, domain.SessionRecord{}, nil, ErrNoController
	}
	return controller, rec, gate.unlock, nil
}

// workingController holds the start/stop gate across provider work. A cold
// session is resumed outside that gate because native Start takes it too.
func (s *Service) workingController(ctx context.Context, id domain.SessionID) (*Controller, func(), error) {
	gate := s.controllerGate(domain.SessionConversationOwner(id))
	for {
		if !gate.tryLock() {
			if controller, err := s.Controller(id); err == nil {
				controller.mu.Lock()
				handoff := controller.handoff
				controller.mu.Unlock()
				if handoff != controllerHandoffNone && handoff != controllerHandoffHibernate {
					return nil, nil, ErrControllerHandoff
				}
			}
			if err := gate.lock(ctx); err != nil {
				return nil, nil, err
			}
		}
		rec, err := s.requireChatSession(ctx, id)
		if err != nil {
			gate.unlock()
			return nil, nil, err
		}
		if rec.HibernatedAt == nil {
			controller, err := s.Controller(id)
			if err == nil {
				controller.mu.Lock()
				hibernating := controller.handoff == controllerHandoffHibernate
				stopped := controller.state == ports.ChatControllerStopped
				controller.mu.Unlock()
				if hibernating {
					gate.unlock()
					select {
					case <-controller.stopped:
						continue
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					}
				}
				if !stopped {
					return controller, gate.unlock, nil
				}
			}
		}
		gate.unlock()
		if err := s.wakeHibernated(ctx, id); err != nil {
			return nil, nil, err
		}
	}
}

func (s *Service) wakeHibernated(ctx context.Context, id domain.SessionID) error {
	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		return err
	}
	if rec.HibernatedAt == nil {
		if s.HasLiveChatController(id) {
			return nil
		}
	}
	if s.wakeChat == nil {
		return ErrNoController
	}
	s.wakeMu.Lock()
	s.waking[id]++
	s.wakeMu.Unlock()
	defer func() {
		s.wakeMu.Lock()
		s.waking[id]--
		if s.waking[id] == 0 {
			delete(s.waking, id)
		}
		s.wakeMu.Unlock()
	}()
	if err := s.wakeChat(ctx, id); err != nil {
		return err
	}
	if !s.HasLiveChatController(id) {
		return ErrNoController
	}
	return nil
}

func (s *Service) isWaking(id domain.SessionID) bool {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	return s.waking[id] > 0
}

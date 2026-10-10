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
const chatHibernateGrace = 30 * time.Second

type wakeRun struct {
	done chan struct{}
	err  error
}

type hibernationStore interface {
	SetSessionHibernated(context.Context, domain.SessionID, int64, *time.Time) (bool, error)
}

type hibernationTurnReader interface {
	LatestVisibleUserTurnSettled(context.Context, string, domain.SessionID) (bool, error)
}

// SetWakeCallback connects a cold Chat session to Session Manager's native
// resume path. It is installed after both services have been constructed.
func (s *Service) SetWakeCallback(wake func(context.Context, domain.SessionID) error) {
	s.wakeChat = wake
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
		s.setViewLease(id, viewID, false)
		gate.unlock()
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
	newView := s.setViewLease(id, viewID, true)
	gate.unlock()
	// Renewing a lease keeps the view open; only a newly opened view wakes a
	// sleeping provider. A failed resume must not spawn another process on
	// every heartbeat. Explicit sends and newly opened views can still retry.
	if !newView || !s.hasChatView(id) {
		return nil
	}
	if rec.ProvisionState.WithDefault() != domain.SessionProvisionReady ||
		rec.HibernatedAt == nil || rec.Metadata.ProviderConversationID == "" {
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
	return err
}

func (s *Service) setViewLease(id domain.SessionID, viewID string, active bool) (newView bool) {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	views := s.liveViewLeasesLocked(id)
	if active {
		delete(s.viewClosedAt, id)
		if views == nil {
			views = make(map[string]time.Time)
			if s.viewLeases == nil {
				s.viewLeases = make(map[domain.SessionID]map[string]time.Time)
			}
			s.viewLeases[id] = views
		}
		_, existing := views[viewID]
		newView = !existing
		views[viewID] = s.now().Add(chatViewLease)
	} else if _, exists := views[viewID]; exists {
		delete(views, viewID)
		if len(views) == 0 {
			if s.viewClosedAt == nil {
				s.viewClosedAt = make(map[domain.SessionID]time.Time)
			}
			s.viewClosedAt[id] = s.now()
		}
	}
	if len(views) == 0 {
		delete(s.viewLeases, id)
		return false
	}
	return newView
}

func (s *Service) hasChatView(id domain.SessionID) bool {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	views := s.liveViewLeasesLocked(id)
	if s.viewClosedAt[id].Add(chatHibernateGrace).After(s.now()) {
		return true
	}
	delete(s.viewClosedAt, id)
	return len(views) != 0
}

// Callers hold viewMu. Expired leases cannot keep a provider alive after a
// renderer crash or a missed release request.
func (s *Service) liveViewLeasesLocked(id domain.SessionID) map[string]time.Time {
	views := s.viewLeases[id]
	now := s.now()
	for key, expiry := range views {
		if !expiry.After(now) {
			if s.viewClosedAt == nil {
				s.viewClosedAt = make(map[domain.SessionID]time.Time)
			}
			if expiry.After(s.viewClosedAt[id]) {
				s.viewClosedAt[id] = expiry
			}
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
	return s.hibernateChat(ctx, id, false)
}

// HibernateChatForRestart does the same for a chat about to be started again:
// at once, watched or not, and whatever the idle setting. Work still stops it.
func (s *Service) HibernateChatForRestart(ctx context.Context, id domain.SessionID) (bool, error) {
	return s.hibernateChat(ctx, id, true)
}

// chatResting reports a chat quiet for long enough to stop its provider.
func (s *Service) chatResting(rec domain.SessionRecord, restart bool) bool {
	if restart {
		return rec.EligibleForChatRestart()
	}
	return s.hibernationEnabled != nil && s.hibernationEnabled() && rec.EligibleForChatHibernation() &&
		!rec.Activity.LastActivityAt.Add(chatHibernateGrace).After(s.now())
}

func (s *Service) hibernateChat(ctx context.Context, id domain.SessionID, restart bool) (bool, error) {
	// Skip eligibility and queue reads while the feature is off. Recheck before
	// stopping the provider in case the setting changes during those reads.
	if !restart && (s.hibernationEnabled == nil || !s.hibernationEnabled()) {
		return false, nil
	}
	marker, ok := s.sessions.(hibernationStore)
	if !ok {
		return false, errors.New("chat hibernation store is unavailable")
	}
	turns, ok := s.sessions.(hibernationTurnReader)
	if !ok {
		return false, errors.New("chat hibernation turn reader is unavailable")
	}
	owner := domain.SessionConversationOwner(id)
	gate := s.controllerGate(owner)
	if !gate.tryLock() {
		return false, nil
	}
	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		gate.unlock()
		return false, err
	}
	if !s.chatResting(rec, restart) || (!restart && s.hasChatView(id)) {
		gate.unlock()
		return false, nil
	}
	controller, err := s.Controller(id)
	if err != nil || controller.State() != ports.ChatControllerReady ||
		!controller.Capabilities().Has(ports.ChatCapabilityResume) {
		gate.unlock()
		return false, nil
	}
	hibernator, ok := controller.conv.(ports.ChatProviderHibernator)
	if !ok {
		gate.unlock()
		return false, nil
	}
	// Inventory is provider I/O. Keep hibernation out with an operation count,
	// while allowing new views, sends and explicit Kill to proceed.
	release := controller.beginOperation()
	gate.unlock()
	ready, err := hibernator.CanHibernate(ctx)
	release()
	if err != nil || !ready {
		if err != nil {
			return false, fmt.Errorf("check chat provider background work: %w", err)
		}
		return false, nil
	}
	if !gate.tryLock() {
		return false, nil
	}
	defer gate.unlock()
	current, err := s.Controller(id)
	if err != nil || current != controller || (!restart && s.hasChatView(id)) {
		return false, nil
	}
	// Send and provider lifecycle projection use the same dispatch lock. Fence
	// intake only after verifying the durable queue and latest primary turn.
	if !controller.sendMu.TryLock() {
		return false, nil
	}
	controller.mu.Lock()
	busy := controller.state != ports.ChatControllerReady ||
		controller.handoff != controllerHandoffNone ||
		controller.pendingTurnID != "" || controller.dispatchingTurnID != "" ||
		controller.compactionPending || controller.operations != 0
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
	settled, err := turns.LatestVisibleUserTurnSettled(ctx, controller.conversation.ID, id)
	if err != nil {
		controller.sendMu.Unlock()
		return false, fmt.Errorf("check latest chat turn: %w", err)
	}
	if !settled {
		controller.sendMu.Unlock()
		return false, nil
	}
	// Activity from outside Chat can change while eligibility is read. Recheck
	// before recording the shutdown intent with a revision-checked write.
	fresh, err := s.requireChatSession(ctx, id)
	if err != nil {
		controller.sendMu.Unlock()
		return false, err
	}
	if !s.chatResting(fresh, restart) {
		controller.sendMu.Unlock()
		return false, nil
	}
	// Persist the intent while intake is fenced. A failed or contested write
	// leaves the provider alive; a crash during shutdown can finish it on boot.
	at := s.now()
	applied, err := marker.SetSessionHibernated(ctx, id, fresh.Revision, &at)
	if err != nil || !applied {
		controller.sendMu.Unlock()
		return false, err
	}
	controller.mu.Lock()
	controller.handoff = controllerHandoffHibernate
	controller.suppressStoppedActivity = true
	controller.mu.Unlock()
	controller.sendMu.Unlock()

	stopErr := hibernator.Hibernate()
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if stopErr == nil {
		select {
		case <-controller.stopped:
		case <-finishCtx.Done():
			stopErr = finishCtx.Err()
		}
	}
	if stopErr != nil {
		// The adapter may have consumed its close-once guard or already sent
		// shutdown. Complete the same owner's shutdown, preserving queued work
		// and resume intent instead of reopening intake on a dying provider.
		if s.stopProviderHost == nil {
			return false, fmt.Errorf("hibernate chat provider: %w", stopErr)
		}
		recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer recoveryCancel()
		if err := s.stopProviderHost(recoveryCtx, id); err != nil {
			return false, fmt.Errorf("confirm hibernated chat shutdown: %w", errors.Join(stopErr, err))
		}
		select {
		case <-controller.stopped:
		case <-recoveryCtx.Done():
			return false, fmt.Errorf("wait for hibernated chat controller: %w", recoveryCtx.Err())
		}
	}
	s.log.Info("chat session hibernated", "session", id, "harness", fresh.Harness)
	return true, nil
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
	release := controller.beginOperation()
	gate.unlock()
	return controller, rec, release, nil
}

// workingController admits provider work under the start/stop gate, then
// releases that gate so explicit Kill can interrupt a stuck provider call.
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
					release := controller.beginOperation()
					gate.unlock()
					return controller, release, nil
				}
			}
			if !s.isWaking(id) {
				gate.unlock()
				return nil, nil, ErrNoController
			}
		}
		gate.unlock()
		if err := s.wakeHibernated(ctx, id); err != nil {
			return nil, nil, err
		}
	}
}

// WakeChat starts a sleeping Chat provider again and delivers what was queued.
func (s *Service) WakeChat(ctx context.Context, id domain.SessionID) error {
	if err := s.wakeHibernated(ctx, id); err != nil {
		return err
	}
	return s.DrainQueued(ctx, id)
}

func (s *Service) wakeHibernated(ctx context.Context, id domain.SessionID) error {
	if s.wakeChat == nil {
		return ErrNoController
	}
	if controller, err := s.Controller(id); err == nil {
		controller.mu.Lock()
		hibernating := controller.handoff == controllerHandoffHibernate
		controller.mu.Unlock()
		if hibernating {
			waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			gate := s.controllerGate(domain.SessionConversationOwner(id))
			if err := gate.lock(waitCtx); err != nil {
				return err
			}
			select {
			case <-controller.stopped:
			default:
				current, _ := s.Controller(id)
				if current == controller && s.stopProviderHost != nil {
					// New user intent can complete a previously failed shutdown.
					// Never send to the fenced source or terminate a replacement.
					if err := s.stopProviderHost(waitCtx, id); err != nil {
						gate.unlock()
						return fmt.Errorf("finish hibernated chat shutdown: %w", err)
					}
				}
			}
			gate.unlock()
			select {
			case <-controller.stopped:
			case <-waitCtx.Done():
				return waitCtx.Err()
			}
		}
	}
	s.wakeMu.Lock()
	if run := s.wakeRuns[id]; run != nil {
		s.wakeMu.Unlock()
		select {
		case <-run.done:
			return run.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Read after joining any existing wake. A stale pre-lock snapshot must not
	// create another automatic attempt after a failed wake clears the marker.
	rec, err := s.requireChatSession(ctx, id)
	if err != nil {
		s.wakeMu.Unlock()
		return err
	}
	if rec.HibernatedAt == nil {
		s.wakeMu.Unlock()
		if s.HasLiveChatController(id) {
			return nil
		}
		return ErrNoController
	}
	run := &wakeRun{done: make(chan struct{})}
	s.wakeRuns[id] = run
	s.waking[id]++
	s.wakeMu.Unlock()
	finish := func(resultErr error) error {
		s.wakeMu.Lock()
		run.err = resultErr
		delete(s.wakeRuns, id)
		s.waking[id]--
		if s.waking[id] == 0 {
			delete(s.waking, id)
		}
		close(run.done)
		s.wakeMu.Unlock()
		return resultErr
	}
	wakeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	if err := s.wakeChat(wakeCtx, id); err != nil {
		return finish(err)
	}
	if !s.HasLiveChatController(id) {
		return finish(ErrNoController)
	}
	return finish(nil)
}

// startBackgroundWake accepts a hibernated send before provider startup. The
// durable queue is the acknowledgement boundary; wake and drain happen after
// the HTTP request returns so the composer never exposes startup latency.
func (s *Service) startBackgroundWake(id domain.SessionID) {
	s.wakeMu.Lock()
	if s.backgroundWakes[id] {
		s.wakeMu.Unlock()
		return
	}
	s.backgroundWakes[id] = true
	s.wakeMu.Unlock()
	go func() {
		defer func() {
			s.wakeMu.Lock()
			delete(s.backgroundWakes, id)
			s.wakeMu.Unlock()
		}()
		ctx := context.Background()
		if err := s.wakeHibernated(ctx, id); err != nil {
			s.failQueuedTurns(ctx, id, fmt.Sprintf("could not wake the agent: %v", err))
			return
		}
		if err := s.DrainQueued(ctx, id); err != nil {
			s.failQueuedTurns(ctx, id, fmt.Sprintf("could not deliver the message: %v", err))
		}
	}()
}

func (s *Service) failQueuedTurns(ctx context.Context, id domain.SessionID, message string) {
	conversation, err := s.store.ConversationForSession(ctx, id)
	if err != nil {
		return
	}
	for {
		queued, err := s.store.NextQueuedTurn(ctx, conversation.ID)
		if errors.Is(err, domain.ErrNoQueuedTurn) {
			return
		}
		if err != nil {
			return
		}
		if err := s.store.SettleTurnByID(ctx, queued.TurnID, domain.TurnStateFailed, message, s.now()); err != nil {
			return
		}
	}
}

func (s *Service) isWaking(id domain.SessionID) bool {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	return s.waking[id] > 0
}

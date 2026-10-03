package chat

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrSideClosed indicates that a side is closed or has a stale generation.
	ErrSideClosed = errors.New("side chat is closed")
	// ErrSideLaunchUnclaimed indicates that no desktop launch owns the side state.
	ErrSideLaunchUnclaimed = errors.New("desktop launch has not claimed side chats")
	// ErrSideIdempotencyConflict indicates that a request key was reused for another question.
	ErrSideIdempotencyConflict = errors.New("side request key belongs to another question")
)

// memorySideStore is deliberately process-local. Electron owns recovery across
// supervised daemon restarts; AO never writes side transcripts or drafts to DB.
type memorySideStore struct {
	mu         sync.Mutex
	runID      string
	sides      map[string]domain.SideConversation
	turns      map[string][]domain.SideTurn
	messages   map[string][]domain.SideMessage
	activities map[string][]domain.SideActivity
	drafts     map[string]string
	cleanup    map[string]domain.SideProviderCleanup
}

// SideRecoveryRecord travels only between the daemon and Electron main process.
// Electron holds it in RAM for this desktop launch and never writes it to disk.
type SideRecoveryRecord struct {
	Side             domain.SideConversation `json:"side"`
	ProviderHostID   string                  `json:"providerHostId"`
	ProviderForkID   string                  `json:"providerForkId"`
	NativeAnchorID   string                  `json:"nativeAnchorId"`
	Harness          domain.AgentHarness     `json:"harness"`
	Generation       string                  `json:"generation"`
	ReferenceContext string                  `json:"referenceContext"`
	ReferencePending bool                    `json:"referencePending"`
	SeedHistory      string                  `json:"seedHistory"`
	Turns            []SideRecoveryTurn      `json:"turns"`
	Messages         []domain.SideMessage    `json:"messages"`
	Activities       []domain.SideActivity   `json:"activities"`
	Draft            string                  `json:"draft"`
}

// SideRecoveryTurn pairs a recoverable side turn with its structured content.
type SideRecoveryTurn struct {
	Turn    domain.SideTurn      `json:"turn"`
	Content []domain.SideContent `json:"content,omitempty"`
}

func (m *memorySideStore) export(runID string) []SideRecoveryRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []SideRecoveryRecord{}
	for id, side := range m.sides {
		if side.AppRunID != runID || side.ClosedAt != nil {
			continue
		}
		turns := make([]SideRecoveryTurn, 0, len(m.turns[id]))
		for _, turn := range m.turns[id] {
			turns = append(turns, SideRecoveryTurn{Turn: turn, Content: turn.Content})
		}
		out = append(out, SideRecoveryRecord{Side: side, ProviderHostID: side.ProviderHostID,
			ProviderForkID: side.ProviderForkID, NativeAnchorID: side.NativeAnchorID,
			Harness: side.Harness, Generation: side.Generation,
			ReferenceContext: side.ReferenceContext, ReferencePending: side.ReferencePending, SeedHistory: side.SeedHistory,
			Turns:      turns,
			Messages:   append([]domain.SideMessage(nil), m.messages[id]...),
			Activities: append([]domain.SideActivity(nil), m.activities[id]...), Draft: m.drafts[id]})
	}
	return out
}

func (m *memorySideStore) recover(runID string, records []SideRecoveryRecord, now time.Time) ([]domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if runID == "" || runID != m.runID {
		return nil, ErrSideLaunchUnclaimed
	}
	seen := map[string]bool{}
	seenIDs := map[string]bool{}
	alreadyRestored := map[string]bool{}
	created := []domain.SideConversation{}
	for _, record := range records {
		side := record.Side
		anchorKey := side.MainConversationID + "\x00" + side.AnchorTurnID
		if side.ID == "" || side.MainConversationID == "" || record.ProviderHostID != "btw-"+side.ID || (record.ProviderForkID == "" && side.State == "ready") || seen[anchorKey] || seenIDs[side.ID] {
			return nil, ErrSideUnavailable
		}
		seen[anchorKey] = true
		seenIDs[side.ID] = true
		for _, existing := range m.sides {
			if existing.MainConversationID == side.MainConversationID && existing.AnchorTurnID == side.AnchorTurnID && existing.ClosedAt == nil {
				if existing.ID != side.ID || existing.Generation != record.Generation ||
					existing.ProviderHostID != record.ProviderHostID || existing.ProviderForkID != record.ProviderForkID {
					return nil, ErrSideUnavailable
				}
				alreadyRestored[side.ID] = true
			}
		}
	}
	for _, record := range records {
		side := record.Side
		if alreadyRestored[side.ID] {
			continue
		}
		side.AppRunID = runID
		side.ProviderHostID = record.ProviderHostID
		side.ProviderForkID = record.ProviderForkID
		side.NativeAnchorID = record.NativeAnchorID
		side.Harness = record.Harness
		side.Generation = record.Generation
		side.ReferenceContext = record.ReferenceContext
		side.ReferencePending = record.ReferencePending
		side.SeedHistory = record.SeedHistory
		if side.ProviderForkID == "" {
			side.State = "failed"
			side.ErrorMessage = "Side opening was interrupted; close and reopen it."
		} else {
			side.State = "recovering"
		}
		side.UpdatedAt = now
		m.sides[side.ID] = side
		m.turns[side.ID] = make([]domain.SideTurn, 0, len(record.Turns))
		for _, item := range record.Turns {
			item.Turn.Content = item.Content
			m.turns[side.ID] = append(m.turns[side.ID], item.Turn)
		}
		for i := range m.turns[side.ID] {
			if m.turns[side.ID][i].State == "running" {
				m.turns[side.ID][i].State = "failed"
				m.turns[side.ID][i].ErrorMessage = "Daemon restarted; retry this turn."
				m.turns[side.ID][i].CompletedAt = &now
			}
		}
		m.messages[side.ID] = append([]domain.SideMessage(nil), record.Messages...)
		m.activities[side.ID] = append([]domain.SideActivity(nil), record.Activities...)
		m.drafts[side.ID] = record.Draft
		if side.ProviderForkID != "" {
			m.cleanup[side.ProviderForkID] = domain.SideProviderCleanup{ForkID: side.ProviderForkID, SessionID: side.SessionID}
		}
		created = append(created, side)
	}
	return created, nil
}

func newMemorySideStore() *memorySideStore {
	return &memorySideStore{sides: map[string]domain.SideConversation{}, turns: map[string][]domain.SideTurn{}, messages: map[string][]domain.SideMessage{}, activities: map[string][]domain.SideActivity{}, drafts: map[string]string{}, cleanup: map[string]domain.SideProviderCleanup{}}
}

func (m *memorySideStore) ClaimSideLaunch(_ context.Context, runID string, _ time.Time) ([]domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if runID == "" {
		return nil, ErrSideLaunchUnclaimed
	}
	if m.runID == runID {
		return nil, nil
	}
	retired := make([]domain.SideConversation, 0, len(m.sides))
	for _, side := range m.sides {
		retired = append(retired, side)
	}
	m.runID = runID
	m.sides = map[string]domain.SideConversation{}
	m.turns = map[string][]domain.SideTurn{}
	m.messages = map[string][]domain.SideMessage{}
	m.activities = map[string][]domain.SideActivity{}
	m.drafts = map[string]string{}
	return retired, nil
}
func (m *memorySideStore) RecoverInterruptedSideTurns(_ context.Context, runID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runID != runID {
		return nil
	}
	for id, turns := range m.turns {
		for i := range turns {
			if turns[i].State == "running" {
				turns[i].State = "failed"
				turns[i].ErrorMessage = "Daemon restarted; retry this turn."
				turns[i].CompletedAt = &now
			}
		}
		m.turns[id] = turns
	}
	return nil
}
func (m *memorySideStore) CreateSideConversation(_ context.Context, side domain.SideConversation) (domain.SideConversation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if side.AppRunID == "" || side.AppRunID != m.runID {
		return domain.SideConversation{}, false, ErrSideLaunchUnclaimed
	}
	for _, current := range m.sides {
		if current.MainConversationID == side.MainConversationID && current.AnchorTurnID == side.AnchorTurnID && current.ClosedAt == nil {
			return current, false, nil
		}
	}
	m.sides[side.ID] = side
	return side, true, nil
}
func (m *memorySideStore) SideConversation(_ context.Context, id string) (domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[id]
	if !ok || side.ClosedAt != nil {
		return side, ErrSideClosed
	}
	return side, nil
}
func (m *memorySideStore) ListSideConversations(_ context.Context, session domain.SessionID, runID string) ([]domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.SideConversation{}
	for _, side := range m.sides {
		if side.SessionID == session && side.AppRunID == runID && side.ClosedAt == nil {
			out = append(out, side)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
func (m *memorySideStore) ListOpenSidesForRun(_ context.Context, runID string) ([]domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.SideConversation{}
	for _, side := range m.sides {
		if side.AppRunID == runID && side.ClosedAt == nil {
			out = append(out, side)
		}
	}
	return out, nil
}
func (m *memorySideStore) updateSide(id, generation string, fn func(*domain.SideConversation)) error {
	side, ok := m.sides[id]
	if !ok || side.ClosedAt != nil || (generation != "" && side.Generation != generation) {
		return ErrSideClosed
	}
	fn(&side)
	m.sides[id] = side
	return nil
}
func (m *memorySideStore) SetSideRecovering(_ context.Context, id, generation string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(id, generation, func(side *domain.SideConversation) {
		side.State = "recovering"
		side.ErrorMessage = ""
		side.UpdatedAt = now
	})
}
func (m *memorySideStore) SetSideReady(_ context.Context, id, generation, providerID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(id, generation, func(s *domain.SideConversation) {
		s.State = "ready"
		s.ErrorMessage = ""
		s.ProviderForkID = providerID
		s.UpdatedAt = now
	})
}
func (m *memorySideStore) RegisterSideFork(_ context.Context, id, generation, providerID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(id, generation, func(s *domain.SideConversation) {
		s.ProviderForkID = providerID
		s.UpdatedAt = now
		m.cleanup[providerID] = domain.SideProviderCleanup{ForkID: providerID, SessionID: s.SessionID}
	})
}
func (m *memorySideStore) SetSideFailed(_ context.Context, id, generation, message string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(id, generation, func(s *domain.SideConversation) { s.State = "failed"; s.ErrorMessage = message; s.UpdatedAt = now })
}
func (m *memorySideStore) CloseSideConversation(_ context.Context, id string, now time.Time) (domain.SideConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[id]
	if !ok || side.ClosedAt != nil {
		return side, ErrSideClosed
	}
	side.ClosedAt = &now
	side.Generation = ""
	delete(m.sides, id)
	delete(m.turns, id)
	delete(m.messages, id)
	delete(m.activities, id)
	delete(m.drafts, id)
	return side, nil
}
func (m *memorySideStore) CompleteSideProviderCleanup(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cleanup, id)
	return nil
}
func (m *memorySideStore) SideProviderCleanupPending(_ context.Context) ([]domain.SideProviderCleanup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.SideProviderCleanup, 0, len(m.cleanup))
	for _, item := range m.cleanup {
		out = append(out, item)
	}
	return out, nil
}
func (m *memorySideStore) ReserveSideTurn(_ context.Context, turn domain.SideTurn, runID string) (domain.SideTurn, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[turn.SideID]
	if !ok || side.ClosedAt != nil || side.AppRunID != runID {
		return domain.SideTurn{}, false, ErrSideClosed
	}
	for _, existing := range m.turns[turn.SideID] {
		if existing.ClientMessageID == turn.ClientMessageID {
			if existing.Text != turn.Text || !reflect.DeepEqual(existing.Content, turn.Content) || !reflect.DeepEqual(existing.References, turn.References) {
				return domain.SideTurn{}, false, ErrSideIdempotencyConflict
			}
			return existing, false, nil
		}
	}
	if side.State != "ready" && side.State != "opening" {
		return domain.SideTurn{}, false, ErrSideNotReady
	}
	side.ReferencePending = false
	m.sides[side.ID] = side
	turn.State = "queued"
	m.turns[turn.SideID] = append(m.turns[turn.SideID], turn)
	m.messages[turn.SideID] = append(m.messages[turn.SideID], domain.SideMessage{
		ID: turn.ID + "-user", SideID: turn.SideID, TurnID: turn.ID, Role: "user", Text: turn.Text,
		Sequence: int64(len(m.messages[turn.SideID]) + 1), CreatedAt: turn.CreatedAt, UpdatedAt: turn.CreatedAt,
	})
	return turn, true, nil
}
func (m *memorySideStore) ClaimNextSideTurn(_ context.Context, runID string, excluded map[string]bool, now time.Time) (domain.SideTurn, domain.SideConversation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var chosen domain.SideTurn
	var side domain.SideConversation
	found := false
	for id, turns := range m.turns {
		current, ok := m.sides[id]
		if !ok || current.AppRunID != runID || current.State != "ready" || excluded[id] {
			continue
		}
		busy := false
		for _, turn := range turns {
			if turn.State == "running" {
				busy = true
				break
			}
		}
		if busy {
			continue
		}
		for _, turn := range turns {
			if turn.State == "queued" && (!found || turn.CreatedAt.Before(chosen.CreatedAt)) {
				chosen, side, found = turn, current, true
			}
		}
	}
	if !found {
		return chosen, side, false, nil
	}
	for i := range m.turns[side.ID] {
		if m.turns[side.ID][i].ID == chosen.ID {
			m.turns[side.ID][i].State = "running"
			m.turns[side.ID][i].StartedAt = &now
			chosen = m.turns[side.ID][i]
			break
		}
	}
	return chosen, side, true, nil
}
func (m *memorySideStore) SettleSideTurn(_ context.Context, sideID, turnID, generation, state, providerID, message string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[sideID]
	if !ok || side.Generation != generation {
		return ErrSideClosed
	}
	for i := range m.turns[sideID] {
		if m.turns[sideID][i].ID != turnID {
			continue
		}
		turn := &m.turns[sideID][i]
		turn.State = state
		turn.ProviderTurnID = providerID
		turn.ErrorMessage = message
		turn.CompletedAt = &now
		return nil
	}
	return ErrSideClosed
}
func (m *memorySideStore) RequeueSideTurn(_ context.Context, sideID, turnID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.turns[sideID] {
		if m.turns[sideID][i].ID == turnID {
			m.turns[sideID][i].State = "queued"
			return nil
		}
	}
	return ErrSideClosed
}
func (m *memorySideStore) RetrySideTurn(_ context.Context, sideID, turnID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.turns[sideID] {
		if m.turns[sideID][i].ID == turnID && m.turns[sideID][i].State == "failed" {
			m.turns[sideID][i].State = "queued"
			m.turns[sideID][i].ErrorMessage = ""
			return nil
		}
	}
	return ErrSideClosed
}
func (m *memorySideStore) UpdateSideSettings(_ context.Context, id, model, effort string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(id, "", func(s *domain.SideConversation) { s.Model = model; s.Effort = effort; s.UpdatedAt = now })
}
func (m *memorySideStore) EditQueuedSideTurn(_ context.Context, sideID, turnID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.turns[sideID] {
		if m.turns[sideID][i].ID == turnID && m.turns[sideID][i].State == "queued" {
			m.turns[sideID][i].Text = text
			for j := range m.messages[sideID] {
				if m.messages[sideID][j].TurnID == turnID && m.messages[sideID][j].Role == "user" {
					m.messages[sideID][j].Text = text
				}
			}
			return nil
		}
	}
	return ErrSideClosed
}
func (m *memorySideStore) SideTurn(_ context.Context, sideID, turnID string) (domain.SideTurn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, turn := range m.turns[sideID] {
		if turn.ID == turnID {
			return turn, nil
		}
	}
	return domain.SideTurn{}, ErrSideClosed
}
func (m *memorySideStore) SideTurns(_ context.Context, sideID string, before time.Time, limit int) ([]domain.SideTurn, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	turns := append([]domain.SideTurn{}, m.turns[sideID]...)
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].CreatedAt.Before(turns[j].CreatedAt) })
	if !before.IsZero() {
		filtered := turns[:0]
		for _, turn := range turns {
			if turn.CreatedAt.Before(before) {
				filtered = append(filtered, turn)
			}
		}
		turns = filtered
	}
	if limit <= 0 || limit > len(turns) {
		limit = len(turns)
	}
	more := len(turns) > limit
	return append([]domain.SideTurn{}, turns[len(turns)-limit:]...), more, nil
}
func (m *memorySideStore) UpsertSideMessage(_ context.Context, msg domain.SideMessage, generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[msg.SideID]
	if !ok || side.Generation != generation {
		return ErrSideClosed
	}
	for i := range m.messages[msg.SideID] {
		if m.messages[msg.SideID][i].ID != msg.ID {
			continue
		}
		previous := m.messages[msg.SideID][i]
		msg.Revision = previous.Revision
		if previous.Text != msg.Text || previous.Streaming != msg.Streaming {
			msg.Revision++
		}
		msg.Sequence = m.messages[msg.SideID][i].Sequence
		msg.CreatedAt = m.messages[msg.SideID][i].CreatedAt
		m.messages[msg.SideID][i] = msg
		return nil
	}
	msg.Sequence = int64(len(m.messages[msg.SideID]) + 1)
	msg.Revision = 1
	m.messages[msg.SideID] = append(m.messages[msg.SideID], msg)
	return nil
}
func (m *memorySideStore) SideMessages(_ context.Context, sideID string, turnIDs []string) ([]domain.SideMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for _, id := range turnIDs {
		set[id] = true
	}
	out := []domain.SideMessage{}
	for _, msg := range m.messages[sideID] {
		if set[msg.TurnID] {
			out = append(out, msg)
		}
	}
	return out, nil
}
func (m *memorySideStore) UpsertSideActivity(sideID, generation string, activity domain.SideActivity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[sideID]
	if !ok || side.Generation != generation {
		return ErrSideClosed
	}
	for i := range m.activities[sideID] {
		if m.activities[sideID][i].ID != activity.ID {
			continue
		}
		old := m.activities[sideID][i]
		if activity.Status == "running" && old.Status != "running" && old.Status != "pending" {
			activity.Status = old.Status
		}
		if activity.Kind == "system" && old.Kind != "" {
			activity.Kind = old.Kind
		}
		if len(activity.Detail) == 0 {
			activity.Detail = old.Detail
		}
		if activity.Summary == "" {
			activity.Summary = old.Summary
		}
		if activity.RequestID == "" {
			activity.RequestID = old.RequestID
		}
		if len(activity.Decisions) == 0 {
			activity.Decisions = old.Decisions
		}
		if activity.Input == nil {
			activity.Input = old.Input
		}
		activity.CreatedAt = old.CreatedAt
		m.activities[sideID][i] = activity
		return nil
	}
	m.activities[sideID] = append(m.activities[sideID], activity)
	return nil
}
func (m *memorySideStore) SideActivities(sideID string, turnIDs []string) []domain.SideActivity {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for _, id := range turnIDs {
		set[id] = true
	}
	out := []domain.SideActivity{}
	for _, item := range m.activities[sideID] {
		if set[item.TurnID] {
			out = append(out, item)
		}
	}
	return out
}
func (m *memorySideStore) SideApproval(sideID, requestID string) (domain.SideActivity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.activities[sideID] {
		if item.RequestID == requestID && item.Kind == "approval" && item.Status == "pending" {
			return item, nil
		}
	}
	return domain.SideActivity{}, ErrSideClosed
}
func (m *memorySideStore) SideInput(sideID, requestID string) (domain.SideActivity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.activities[sideID] {
		if item.RequestID == requestID && item.Kind == "user_input" && item.Status == "pending" {
			return item, nil
		}
	}
	return domain.SideActivity{}, ErrSideClosed
}
func (m *memorySideStore) SetSideDraft(_ context.Context, sideID, runID, content string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	side, ok := m.sides[sideID]
	if !ok || side.AppRunID != runID {
		return ErrSideClosed
	}
	m.drafts[sideID] = content
	return nil
}
func (m *memorySideStore) SideDraft(_ context.Context, sideID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drafts[sideID], nil
}
func (m *memorySideStore) SetSideReference(_ context.Context, sideID string, ref ports.ChatExcerptReference, selection, contextText string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateSide(sideID, "", func(s *domain.SideConversation) {
		s.SelectedText = selection
		s.ReferenceContext = contextText
		s.ReferencePending = true
		s.SourceMessageID = ref.MessageID
		s.SourceRevision = ref.Revision
		s.UpdatedAt = now
	})
}

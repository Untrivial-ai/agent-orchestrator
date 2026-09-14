package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type liveConversationService interface {
	SubscribeLive(context.Context, domain.SessionID) (*chatsvc.LiveSubscription, error)
}

// RegisterStreams keeps live text outside the ordinary REST request timeout.
func (c *ConversationsController) RegisterStreams(r chi.Router) {
	r.Get("/sessions/{sessionId}/conversation/events", c.streamLive)
}

func (c *ConversationsController) streamLive(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(liveConversationService)
	if !ok {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/conversation/events")
		return
	}
	sub, err := svc.SubscribeLive(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	var after int64
	writeFrame := func() error {
		frame := sub.Snapshot(after)
		out := ConversationLiveResponse{
			Generation: frame.Generation, ConversationID: frame.ConversationID, BranchID: frame.BranchID,
			AfterSequence: frame.AfterSequence, Sequence: frame.Sequence,
			ResetSequence: frame.ResetSequence,
			Events:        make([]ConversationLiveEventResponse, 0, len(frame.Events)),
		}
		for _, event := range frame.Events {
			out.Events = append(out.Events, ConversationLiveEventResponse{
				Sequence: event.Sequence, Kind: string(event.Kind), ProviderItemID: event.ProviderItemID,
				ProviderTurnID: event.ProviderTurnID, Delta: event.Delta, Text: event.Text,
				CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		payload, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err = fmt.Fprintf(w, "event: conversation_text\ndata: %s\n\n", payload); err != nil {
			return err
		}
		after = frame.Sequence
		return rc.Flush()
	}
	if writeFrame() != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-sub.Changed():
			if writeFrame() != nil || !open {
				return
			}
		case <-heartbeat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

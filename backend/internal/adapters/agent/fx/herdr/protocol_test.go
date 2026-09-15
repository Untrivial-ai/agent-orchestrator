package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type signalRecord struct {
	id     domain.SessionID
	signal ports.ActivitySignal
}
type captureSink struct {
	records []signalRecord
	err     error
}

func (s *captureSink) ApplyActivitySignal(_ context.Context, id domain.SessionID, signal ports.ActivitySignal) error {
	s.records = append(s.records, signalRecord{id, signal})
	return s.err
}

func TestProtocolStateTranslation(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  domain.ActivityState
	}{
		{"working", domain.ActivityActive}, {"idle", domain.ActivityWaitingInput}, {"blocked", domain.ActivityBlocked},
	} {
		t.Run(tc.state, func(t *testing.T) {
			sink := &captureSink{}
			server := &Server{sink: sink, logger: slog.Default()}
			line := `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"` + tc.state + `"}}`
			response := server.handle(context.Background(), []byte(line))
			if !response.OK || response.ID != "7" || len(sink.records) != 1 {
				t.Fatalf("response = %+v; signals = %+v", response, sink.records)
			}
			got := sink.records[0]
			if got.id != "mer-1" || !got.signal.Valid || got.signal.State != tc.want || got.signal.LaunchID != "launch-1" || got.signal.ExpectedHarness != domain.HarnessFX || got.signal.Timestamp.IsZero() {
				t.Fatalf("signal = %+v", got)
			}
		})
	}
}

func TestProtocolNativeSessionIsMetadataOnly(t *testing.T) {
	sink := &captureSink{}
	server := &Server{sink: sink, logger: slog.Default()}
	response := server.handle(context.Background(), []byte(`{"id":"3","method":"pane.report_agent_session","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","agent_session_id":"native-fx-42"}}`))
	if !response.OK || len(sink.records) != 1 {
		t.Fatalf("response=%+v, signals=%+v", response, sink.records)
	}
	got := sink.records[0].signal
	if got.Valid || got.State != "" || got.AgentSessionID != "native-fx-42" || got.LaunchID != "launch-1" {
		t.Fatalf("metadata signal = %+v", got)
	}
}

func TestProtocolAcknowledgesNativeRenameAndClearWithoutSignals(t *testing.T) {
	for _, line := range []string{
		`{"id":"4","method":"pane.rename","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","label":"fx"}}`,
		`{"id":"5","method":"agent.rename","params":{"target":"ao:1:bWVyLTE:bGF1bmNoLTE","name":"fx"}}`,
		`{"id":"6","method":"agent.rename","params":{"target":"ao:1:bWVyLTE:bGF1bmNoLTE","name":null}}`,
		`{"id":"7","method":"pane.clear_agent_authority","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx"}}`,
		`{"id":"8","method":"pane.rename","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","label":null}}`,
	} {
		sink := &captureSink{}
		server := &Server{sink: sink, logger: slog.Default()}
		if response := server.handle(context.Background(), []byte(line)); !response.OK || len(sink.records) != 0 {
			t.Fatalf("%s: response=%+v, signals=%+v", line, response, sink.records)
		}
	}
}

func TestProtocolRejectsUntrustedReports(t *testing.T) {
	valid := `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"working"}}`
	for _, line := range []string{
		`{`, `null`, `{}`, `[]`, valid + `{}`,
		strings.Replace(valid, `"id":"7"`, `"id":7`, 1),
		strings.Replace(valid, "custom:fx", "custom:codex", 1),
		strings.Replace(valid, "custom:fx", "custom:fx ", 1),
		strings.Replace(valid, `"agent":"fx"`, `"agent":"codex"`, 1),
		strings.Replace(valid, `"agent":"fx"`, `"agent":"FX"`, 1),
		strings.Replace(valid, `"source":"custom:fx",`, ``, 1),
		strings.Replace(valid, `"agent":"fx",`, ``, 1),
		strings.Replace(valid, "working", "unknown", 1),
		strings.Replace(valid, "pane.report_agent", "unknown", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "mer-1", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "ao:2:bWVyLTE:bGF1bmNoLTE", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "ao:1::bGF1bmNoLTE", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "ao:1:bWVyLTE:", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "ao:1:!:bGF1bmNoLTE", 1),
		strings.Replace(valid, "ao:1:bWVyLTE:bGF1bmNoLTE", "ao:1:bWVyLTE:bGF1bmNoLTE:extra", 1),
	} {
		sink := &captureSink{}
		server := &Server{sink: sink, logger: slog.Default()}
		if response := server.handle(context.Background(), []byte(line)); response.OK || len(sink.records) != 0 {
			t.Fatalf("untrusted report accepted: %s; %+v", line, response)
		}
	}
}

func TestProtocolPreservesBlockedDiagnosticAndReportsSinkErrors(t *testing.T) {
	for _, status := range []string{"permission", "question", "recovery"} {
		var logs bytes.Buffer
		sink := &captureSink{err: errors.New("storage unavailable")}
		server := &Server{sink: sink, logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
		line := `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"blocked","custom_status":"` + status + `"}}`
		if response := server.handle(context.Background(), []byte(line)); response.OK {
			t.Fatal("sink error acknowledged as applied")
		}
		if !bytes.Contains(logs.Bytes(), []byte(`"custom_status":"`+status+`"`)) {
			t.Fatalf("missing blocked diagnostic: %s", logs.String())
		}
		if len(sink.records) != 1 || sink.records[0].signal.State != domain.ActivityBlocked {
			t.Fatalf("blocked signal=%+v", sink.records)
		}
		if _, err := json.Marshal(server.handle(context.Background(), []byte(`{"id":"x","method":"future"}`))); err != nil {
			t.Fatal(err)
		}
	}
}

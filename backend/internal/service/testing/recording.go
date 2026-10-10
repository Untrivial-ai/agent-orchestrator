package testing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) deliveryMode() string {
	if policy, ok := s.deps.Desktop.(ports.TestingDesktopPolicy); ok {
		return policy.DeliveryMode()
	}
	return "foreground"
}

func (s *Service) recordingDirectory(st *attemptState) string {
	return filepath.Join(s.deps.EvidenceRoot, string(st.record.RunID), string(st.record.ID), "recording-staging")
}

func (s *Service) recordingJournal(ctx context.Context, record domain.TestActionRecord) error {
	if err := s.deps.Evidence.AppendAction(ctx, record); err != nil {
		return fmt.Errorf("%w: %w", apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save recording journal"), err)
	}
	return nil
}

func (s *Service) setRecordingGap(ctx context.Context, st *attemptState, gap string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.record.RecordingGap = gap
	return s.deps.Store.UpdateTestAttempt(ctx, st.record)
}

func (s *Service) startRecording(ctx context.Context, st *attemptState, target domain.TestTargetIdentity) error {
	record := domain.TestActionRecord{AttemptID: st.record.ID, WindowID: target.WindowID, LaunchID: target.LaunchID, RequestID: "recording-" + uuid.NewString(), Tool: "start_recording", Input: json.RawMessage(`{}`), State: "dispatching", At: s.deps.Clock.Now().UTC()}
	if err := s.recordingJournal(ctx, record); err != nil {
		return err
	}
	var result ports.TestingRecordingResult
	var setupErr, startErr error
	recorder, configured := s.deps.Desktop.(ports.TestingDesktopRecorder)
	if configured {
		if !filepath.IsAbs(s.deps.EvidenceRoot) {
			setupErr = fmt.Errorf("recording evidence directory is not configured")
		} else {
			result, startErr = recorder.StartRecording(ctx, target, s.recordingDirectory(st))
			result.Gap = ""
			if startErr != nil {
				result.Gap = startErr.Error()
			}
		}
	} else {
		record.Detail = "Recording provider is not configured; no recording was started."
	}
	s.mu.Lock()
	st.recording = configured && setupErr == nil && (startErr == nil || result.RecorderPID > 0)
	s.mu.Unlock()
	record.At = s.deps.Clock.Now().UTC()
	record.State = "completed"
	record.RecordingGap = result.Gap
	record.Recording, _ = json.Marshal(result)
	if startErr != nil {
		record.State = "failed"
		record.Detail = "StartRecording failed: " + result.Gap
	} else if setupErr != nil {
		record.State, record.Detail = "failed", setupErr.Error()
	} else if record.Detail != "" {
		record.State = "skipped"
	}
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	journalErr := s.recordingJournal(completionCtx, record)
	gapErr := s.setRecordingGap(completionCtx, st, result.Gap)
	return errors.Join(journalErr, gapErr, setupErr)
}

func (s *Service) stopRecording(ctx context.Context, st *attemptState, target domain.TestTargetIdentity) error {
	s.mu.Lock()
	started := st.recording
	st.recording = false
	s.mu.Unlock()
	if !started {
		return nil
	}
	recorder, ok := s.deps.Desktop.(ports.TestingDesktopRecorder)
	if !ok {
		return ProviderNotConfigured()
	}
	record := domain.TestActionRecord{AttemptID: st.record.ID, WindowID: target.WindowID, LaunchID: target.LaunchID, RequestID: "recording-" + uuid.NewString(), Tool: "stop_recording", Input: json.RawMessage(`{}`), State: "dispatching", At: s.deps.Clock.Now().UTC()}
	journalErr := s.recordingJournal(ctx, record)
	// Stop even when the journal fails: cancellation must not leave a recorder
	// running. The cleanup still reports the evidence failure explicitly.
	result, stopErr := recorder.StopRecording(ctx, target)
	result.Gap = ""
	// Final evidence must survive an expired recording-stop deadline.
	copyCtx, copyCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	var saveErr error
	if stopErr == nil {
		saveErr = s.saveRecording(copyCtx, st, result)
	}
	copyCancel()
	// Terminal diagnostics get a fresh budget after the copy finishes or fails.
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := errors.Join(journalErr, stopErr, saveErr)
	if err != nil {
		result.Gap = err.Error()
	}
	metadata, _ := json.Marshal(result)
	_, metadataErr := s.deps.Evidence.Write(completionCtx, st.record.ID, ports.TestingEvidenceArtifact{Kind: "recording_metadata", MIMEType: "application/json"}, strings.NewReader(string(metadata)))
	if metadataErr != nil {
		metadataErr = fmt.Errorf("%w: %w", apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save recording metadata"), metadataErr)
		err = errors.Join(err, metadataErr)
		result.Gap = err.Error()
	}
	record.At = s.deps.Clock.Now().UTC()
	record.State = "completed"
	if err != nil {
		record.State, record.Detail = "failed", err.Error()
	}
	record.RecordingGap = result.Gap
	record.Recording, _ = json.Marshal(result)
	completionErr := s.recordingJournal(completionCtx, record)
	if completionErr != nil {
		result.Gap = errors.Join(err, completionErr).Error()
	}
	gapErr := s.setRecordingGap(completionCtx, st, result.Gap)
	return errors.Join(err, completionErr, gapErr)
}

func (s *Service) saveRecording(ctx context.Context, st *attemptState, result ports.TestingRecordingResult) error {
	if (result.MIMEType != "video/quicktime" && result.MIMEType != "video/mp4") || result.Duration <= 0 || result.Width < 1 || result.Height < 1 || result.StartedAt.IsZero() || result.StoppedAt.Before(result.StartedAt) || !filepath.IsAbs(result.Path) {
		return fmt.Errorf("recording provider returned invalid finalized movie metadata")
	}
	dir, err := filepath.EvalSymlinks(s.recordingDirectory(st))
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(result.Path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("recording path is outside attempt evidence storage")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	movie, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = movie.Close() }()
	info, err := movie.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("recording path is not a nonempty regular movie")
	}
	if _, err := s.deps.Evidence.Write(ctx, st.record.ID, ports.TestingEvidenceArtifact{Kind: "recording", MIMEType: result.MIMEType}, movie); err != nil {
		return fmt.Errorf("%w: %w", apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save finalized recording"), err)
	}
	return nil
}

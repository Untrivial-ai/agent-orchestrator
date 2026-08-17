package sessionmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	handoffTerminalMaxLines    = 600
	handoffTerminalMaxBytes    = 64 << 10
	handoffTranscriptMaxLines  = 600
	handoffTranscriptMaxBytes  = 64 << 10
	transcriptOmittedMarker    = "[... earlier provider transcript content omitted ...]"
	transcriptPartialMarker    = "[... newest provider transcript record exceeded AO's excerpt limit; showing its bounded suffix ...]"
	transcriptIncompleteMarker = "[... incomplete final provider transcript record omitted ...]"
	finalizedHandoffMaxBytes   = 256 << 10
)

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
)

type switchPRFact struct {
	URL          string `json:"url"`
	Number       int    `json:"number,omitempty"`
	State        string `json:"state,omitempty"`
	CI           string `json:"ci,omitempty"`
	Review       string `json:"review,omitempty"`
	Mergeability string `json:"mergeability,omitempty"`
}

type switchWorkspaceFact struct {
	Repository string                  `json:"repository,omitempty"`
	Path       string                  `json:"path"`
	Branch     string                  `json:"branch,omitempty"`
	HeadSHA    string                  `json:"headSha,omitempty"`
	Dirty      bool                    `json:"dirty"`
	Staged     bool                    `json:"staged"`
	Untracked  bool                    `json:"untracked"`
	Changes    []ports.WorkspaceChange `json:"changes,omitempty"`
	Commits    []ports.WorkspaceCommit `json:"commits,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

// switchTranscriptFact is an ephemeral verified source-transcript reference
// for the target continuation. Tail is populated through a read-only descriptor
// only when no semantic handoff exists and AO needs bounded fallback context.
// It is captured only after the source has conclusively stopped; AO never
// rewrites the provider file or persists the potentially large tail.
type switchTranscriptFact struct {
	Path      string
	Tail      string
	Truncated bool
}

// deterministicSwitchContext is the context AO can build without asking a
// model. It is intentionally assembled in memory and rendered directly into
// the target continuation. Operational switch state remains durable, but AO
// does not retain a second canonical conversation or deterministic transcript.
type deterministicSwitchContext struct {
	OriginalTask          string
	LatestUserPrompt      string
	LatestAssistantUpdate string
	SemanticHandoff       json.RawMessage
	SourceTranscriptPath  string
	TerminalTail          string
	Workspaces            []switchWorkspaceFact
	PullRequests          []switchPRFact
	CapturedAt            time.Time
}

type writtenAgentHandoff struct {
	Path string
	Hash string
}

// finalizedAgentHandoff is the one permanent, AO-owned context snapshot for a
// completed switch. Continuation is the exact bounded historical context added
// to the target's hidden system instructions. Keeping that exact text makes a
// later native restore deterministic without retaining a second conversation.
type finalizedAgentHandoff struct {
	SchemaVersion int                  `json:"schemaVersion"`
	SwitchID      domain.AgentSwitchID `json:"switchId"`
	SessionID     domain.SessionID     `json:"sessionId"`
	FromHarness   domain.AgentHarness  `json:"fromAgent"`
	TargetHarness domain.AgentHarness  `json:"targetAgent"`
	Continuation  string               `json:"continuation"`
}

func normalizeTerminalTail(output string) string {
	lines := strings.Split(normalizeHistoricalContext([]byte(output)), "\n")
	if len(lines) > handoffTerminalMaxLines {
		lines = lines[len(lines)-handoffTerminalMaxLines:]
	}
	for len(lines) > 0 {
		joined := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if len(joined) <= handoffTerminalMaxBytes {
			return joined
		}
		if len(lines) == 1 {
			const marker = "[... terminal line truncated ...]\n"
			budget := handoffTerminalMaxBytes - len(marker)
			if budget <= 0 {
				return ""
			}
			data := []byte(lines[0])
			if len(data) > budget {
				data = data[len(data)-budget:]
			}
			return marker + string(bytes.ToValidUTF8(data, []byte("?")))
		}
		lines = lines[1:]
	}
	return ""
}

func readNativeTranscriptTailWithOpen(ctx context.Context, path, configDir string, openFile func(string) (*os.File, error)) (tail string, truncated, ok bool) {
	if ctx.Err() != nil {
		return "", false, false
	}
	path = safeNativeTranscriptPath(ctx, path, configDir)
	if path == "" || openFile == nil {
		return "", false, false
	}
	f, err := openFile(path) //nolint:gosec // production opener is os.Open on a validated provider-owned path.
	if err != nil {
		return "", false, false
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() == 0 {
		return "", false, false
	}
	// Close the validation/open race as far as portable os APIs permit: resolve
	// the path again and require that it still names the exact file descriptor
	// AO already opened beneath the provider config directory.
	if ctx.Err() != nil {
		return "", false, false
	}
	revalidated := safeNativeTranscriptPath(ctx, path, configDir)
	if revalidated == "" || revalidated != path {
		return "", false, false
	}
	currentInfo, err := os.Stat(revalidated)
	if err != nil || !os.SameFile(openedInfo, currentInfo) {
		return "", false, false
	}

	markerReserve := len(transcriptOmittedMarker) + len(transcriptPartialMarker) + len(transcriptIncompleteMarker) + 3
	readLimit := handoffTranscriptMaxBytes - markerReserve
	start := openedInfo.Size() - int64(readLimit)
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false, false
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(readLimit)))
	if err != nil {
		return "", false, false
	}
	if ctx.Err() != nil {
		return "", false, false
	}
	earlierOmitted := start > 0
	partialRecord := false
	if earlierOmitted {
		// The bounded read normally begins in the middle of a JSONL record. Drop
		// that partial record when a later complete record exists. If the newest
		// record alone exceeds the budget, retain its suffix with the marker.
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 && newline < len(data)-1 {
			data = data[newline+1:]
		} else {
			partialRecord = true
		}
	}

	hadTrailingLineBreak := len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r')
	text := normalizeHistoricalContext(data)
	text = strings.TrimRight(text, "\n")
	lines := []string(nil)
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	incompleteFinal := false
	if !partialRecord && !hadTrailingLineBreak && len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && !json.Valid([]byte(last)) {
			lines = lines[:len(lines)-1]
			incompleteFinal = true
		}
	}

	markerLines := 0
	if earlierOmitted {
		markerLines++
	}
	if partialRecord {
		markerLines++
	}
	if incompleteFinal {
		markerLines++
	}
	lineLimit := max(0, handoffTranscriptMaxLines-markerLines)
	if len(lines) > lineLimit {
		if !earlierOmitted {
			earlierOmitted = true
			markerLines++
			lineLimit = max(0, handoffTranscriptMaxLines-markerLines)
		}
		lines = lines[len(lines)-lineLimit:]
	}

	parts := make([]string, 0, len(lines)+3)
	if earlierOmitted {
		parts = append(parts, transcriptOmittedMarker)
	}
	if partialRecord {
		parts = append(parts, transcriptPartialMarker)
	}
	parts = append(parts, lines...)
	if incompleteFinal {
		parts = append(parts, transcriptIncompleteMarker)
	}
	result := strings.Join(parts, "\n")
	if result == "" || len(result) > handoffTranscriptMaxBytes {
		return "", false, false
	}
	return result, earlierOmitted || partialRecord || incompleteFinal, true
}

func normalizeHistoricalContext(data []byte) string {
	value := string(bytes.ToValidUTF8(data, []byte("?")))
	value = ansiOSC.ReplaceAllString(value, "")
	value = ansiCSI.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	var clean strings.Builder
	clean.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\n' || r == '\t':
			clean.WriteRune(r)
		case r == '\r':
			clean.WriteByte('\n')
		case r >= 0x20 && r != 0x7f:
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func (m *Manager) handoffDirectory(sessionID domain.SessionID, switchID string) (string, error) {
	if strings.TrimSpace(m.dataDir) == "" {
		return "", errors.New("agent switch: AO data directory is unavailable")
	}
	if !handoffPathComponent(string(sessionID)) || !handoffPathComponent(switchID) {
		return "", errors.New("agent switch: invalid handoff path identity")
	}
	return filepath.Join(m.dataDir, "handoffs", string(sessionID), switchID), nil
}

func handoffPathComponent(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, `/\`)
}

// prepareAgentHandoffPaths creates the private per-switch directory and returns
// the source-writable candidate and AO-owned accepted semantic-handoff paths.
// Both are temporary inputs to AO's finalized handoff; handoff.json is the sole
// permanent artifact after a successful switch.
func (m *Manager) prepareAgentHandoffPaths(ctx context.Context, sessionID domain.SessionID, switchID string) (candidatePath, finalPath string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	dir, err := m.handoffDirectory(sessionID, switchID)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		return "", "", fmt.Errorf("agent switch: create AO data directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	dataInfo, err := os.Lstat(m.dataDir)
	if err != nil || !dataInfo.IsDir() || dataInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("agent switch: AO data path is not a real directory")
	}
	for _, directory := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if err := ensurePrivateHandoffDirectory(directory); err != nil {
			return "", "", err
		}
	}
	if err := validateHandoffDirectoryChain(dir); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "agent-handoff-candidate.json"), filepath.Join(dir, "agent-handoff.json"), nil
}

func (m *Manager) finalizedHandoffPath(sessionID domain.SessionID, switchID string) (string, error) {
	dir, err := m.handoffDirectory(sessionID, switchID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "handoff.json"), nil
}

func ensurePrivateHandoffDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("agent switch: create handoff directory %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("agent switch: inspect handoff directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent switch: handoff path %s is not a real directory", path)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // Owner traversal is required for this private directory.
		return fmt.Errorf("agent switch: secure handoff directory %s: %w", path, err)
	}
	return nil
}

func validateHandoffDirectoryChain(dir string) error {
	// dir has the fixed shape dataDir/handoffs/session/switch. Include the exact
	// dataDir itself: checking only descendants would still follow an attacker-
	// replaced dataDir symlink into another otherwise-real directory tree.
	for _, directory := range []string{
		filepath.Dir(filepath.Dir(filepath.Dir(dir))),
		filepath.Dir(filepath.Dir(dir)),
		filepath.Dir(dir),
		dir,
	} {
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent switch: handoff path %s is not a real directory", directory)
		}
	}
	return nil
}

// writeAgentHandoffFile temporarily persists the validated source-authored
// semantic report. After source stop AO embeds it in handoff.json and removes
// this intermediate file.
func (m *Manager) writeAgentHandoffFile(ctx context.Context, sessionID domain.SessionID, switchID string, body json.RawMessage) (writtenAgentHandoff, error) {
	if err := ctx.Err(); err != nil {
		return writtenAgentHandoff{}, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || !json.Valid(trimmed) || trimmed[0] != '{' {
		return writtenAgentHandoff{}, errors.New("agent switch: semantic handoff must be one JSON object")
	}
	_, finalPath, err := m.prepareAgentHandoffPaths(ctx, sessionID, switchID)
	if err != nil {
		return writtenAgentHandoff{}, err
	}
	if err := validateHandoffDirectoryChain(filepath.Dir(finalPath)); err != nil {
		return writtenAgentHandoff{}, err
	}
	bodyWithNewline := append(append([]byte(nil), trimmed...), '\n')
	if err := writeAtomicImmutableFile(ctx, finalPath, bodyWithNewline); err != nil {
		return writtenAgentHandoff{}, err
	}
	return writtenAgentHandoff{Path: finalPath, Hash: agentHandoffContentHash(trimmed)}, nil
}

func (m *Manager) writeFinalizedHandoffFile(ctx context.Context, sw domain.AgentSwitch, continuation string) (writtenAgentHandoff, error) {
	if err := ctx.Err(); err != nil {
		return writtenAgentHandoff{}, err
	}
	continuation = strings.TrimSpace(continuation)
	if continuation == "" || len(continuation) > handoffContinuationMaxBytes {
		return writtenAgentHandoff{}, errors.New("agent switch: finalized continuation is empty or exceeds its limit")
	}
	artifact := finalizedAgentHandoff{
		SchemaVersion: 1,
		SwitchID:      sw.ID,
		SessionID:     sw.SessionID,
		FromHarness:   sw.FromHarness,
		TargetHarness: sw.TargetHarness,
		Continuation:  continuation,
	}
	body, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return writtenAgentHandoff{}, fmt.Errorf("agent switch: encode finalized handoff: %w", err)
	}
	body = append(body, '\n')
	if len(body) > finalizedHandoffMaxBytes {
		return writtenAgentHandoff{}, errors.New("agent switch: encoded finalized handoff exceeds its limit")
	}
	path, err := m.finalizedHandoffPath(sw.SessionID, string(sw.ID))
	if err != nil {
		return writtenAgentHandoff{}, err
	}
	if err := validateHandoffDirectoryChain(filepath.Dir(path)); err != nil {
		return writtenAgentHandoff{}, err
	}
	if err := writeAtomicImmutableFile(ctx, path, body); err != nil {
		return writtenAgentHandoff{}, err
	}
	return writtenAgentHandoff{Path: path, Hash: contentHash(body)}, nil
}

func agentHandoffContentHash(canonical json.RawMessage) string {
	bodyWithNewline := append(append([]byte(nil), bytes.TrimSpace(canonical)...), '\n')
	return contentHash(bodyWithNewline)
}

func contentHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func validContentHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func writeAtomicImmutableFile(ctx context.Context, path string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("agent switch: inspect handoff directory: %w", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("agent switch: handoff directory is not a real directory")
	}
	if existing, found, err := readRegularFileWithoutSymlink(ctx, path, int64(len(body)+1)); err != nil {
		return fmt.Errorf("agent switch: inspect existing handoff %s: %w", path, err)
	} else if found {
		if bytes.Equal(existing, body) {
			return syncHandoffDirectory(ctx, filepath.Dir(path))
		}
		return fmt.Errorf("agent switch: immutable handoff already exists with different contents: %s", path)
	}
	f, err := os.CreateTemp(dir, ".agent-handoff-*.tmp") //nolint:gosec // private AO-owned directory.
	if err != nil {
		return fmt.Errorf("agent switch: create temporary handoff: %w", err)
	}
	temporaryPath := f.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("agent switch: secure temporary handoff: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("agent switch: write temporary handoff: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("agent switch: sync temporary handoff: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("agent switch: close temporary handoff: %w", err)
	}
	// Publish with a same-directory hard link instead of Rename. Link is an
	// atomic create-if-absent operation; Rename would replace an existing final
	// file on Unix and let a racing different submission violate immutability.
	if err := os.Link(temporaryPath, path); err != nil {
		if existing, found, readErr := readRegularFileWithoutSymlink(ctx, path, int64(len(body)+1)); readErr == nil && found && bytes.Equal(existing, body) {
			return syncHandoffDirectory(ctx, dir)
		}
		return fmt.Errorf("agent switch: publish handoff %s: %w", path, err)
	}
	_ = os.Remove(temporaryPath)
	return syncHandoffDirectory(ctx, dir)
}

func syncHandoffDirectory(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dirHandle, err := os.Open(dir) //nolint:gosec // validated AO-owned directory.
	if err != nil {
		return fmt.Errorf("agent switch: open handoff directory for sync: %w", err)
	}
	defer func() { _ = dirHandle.Close() }()
	if err := dirHandle.Sync(); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("agent switch: sync handoff directory: %w", err)
	}
	return nil
}

// readRegularFileWithoutSymlink reads only the exact regular file observed by
// Lstat. It rejects symlinks and replacement races so an agent cannot make AO
// follow a pre-created handoff path outside the private switch directory.
func readRegularFileWithoutSymlink(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	pathInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, false, errors.New("handoff path is not a regular file")
	}
	f, err := os.Open(path) //nolint:gosec // exact AO-owned path validated with Lstat and SameFile.
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, false, errors.New("handoff path changed while it was opened")
	}
	if limit <= 0 {
		return nil, false, errors.New("handoff read limit must be positive")
	}
	body, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return body, true, nil
}

// readVerifiedAgentHandoffForDelivery reopens the AO-owned semantic file after the
// source process has stopped and proves it still matches the durable digest.
// Durable status remains provenance; callers use this result only to decide
// whether semantic context is safe and available for this target delivery.
func (m *Manager) readVerifiedAgentHandoffForDelivery(ctx context.Context, sw domain.AgentSwitch) (json.RawMessage, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	if sw.AgentHandoffStatus != domain.AgentHandoffReceived || !validContentHash(sw.AgentHandoffHash) {
		return nil, false
	}
	dir, err := m.handoffDirectory(sw.SessionID, string(sw.ID))
	if err != nil {
		return nil, false
	}
	expectedPath := filepath.Join(dir, "agent-handoff.json")
	if filepath.Clean(sw.AgentHandoffPath) != filepath.Clean(expectedPath) {
		return nil, false
	}
	if err := validateHandoffDirectoryChain(dir); err != nil {
		return nil, false
	}
	body, found, err := readRegularFileWithoutSymlink(ctx, expectedPath, sourceSemanticHandoffMaxBytes+2)
	if err != nil || !found || len(body) == 0 || len(body) > sourceSemanticHandoffMaxBytes+1 {
		return nil, false
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != sw.AgentHandoffHash {
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if !json.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	return append(json.RawMessage(nil), trimmed...), true
}

func (m *Manager) readVerifiedFinalizedHandoff(ctx context.Context, sw domain.AgentSwitch) (finalizedAgentHandoff, bool) {
	if ctx.Err() != nil {
		return finalizedAgentHandoff{}, false
	}
	if !validContentHash(sw.FinalHandoffHash) {
		return finalizedAgentHandoff{}, false
	}
	expectedPath, err := m.finalizedHandoffPath(sw.SessionID, string(sw.ID))
	if err != nil || filepath.Clean(sw.FinalHandoffPath) != filepath.Clean(expectedPath) {
		return finalizedAgentHandoff{}, false
	}
	if err := validateHandoffDirectoryChain(filepath.Dir(expectedPath)); err != nil {
		return finalizedAgentHandoff{}, false
	}
	body, found, err := readRegularFileWithoutSymlink(ctx, expectedPath, finalizedHandoffMaxBytes+1)
	if err != nil || !found || len(body) == 0 || len(body) > finalizedHandoffMaxBytes {
		return finalizedAgentHandoff{}, false
	}
	if contentHash(body) != sw.FinalHandoffHash {
		return finalizedAgentHandoff{}, false
	}
	var artifact finalizedAgentHandoff
	if err := json.Unmarshal(body, &artifact); err != nil ||
		artifact.SchemaVersion != 1 || artifact.SwitchID != sw.ID ||
		artifact.SessionID != sw.SessionID || artifact.FromHarness != sw.FromHarness ||
		artifact.TargetHarness != sw.TargetHarness || strings.TrimSpace(artifact.Continuation) == "" ||
		len(artifact.Continuation) > handoffContinuationMaxBytes {
		return finalizedAgentHandoff{}, false
	}
	return artifact, true
}

// cleanupAgentHandoffArtifacts removes temporary or unowned files derived
// from one durable switch identity. A final file is retained only when the
// durable row owns its exact canonical path and digest. This makes both the
// live defer and restart reconciliation safe to repeat.
func (m *Manager) cleanupAgentHandoffArtifacts(ctx context.Context, sw domain.AgentSwitch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := m.handoffDirectory(sw.SessionID, string(sw.ID))
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	// Never traverse a source-replaced symlink while deleting even fixed AO
	// filenames. Cleanup is best effort and fails closed if any AO-owned
	// directory below dataDir changed type.
	if err := validateHandoffDirectoryChain(dir); err != nil {
		return fmt.Errorf("agent switch: refuse unsafe handoff cleanup: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	_, keepFinal := m.readVerifiedFinalizedHandoff(ctx, sw)
	_, keepSemantic := m.readVerifiedAgentHandoffForDelivery(ctx, sw)
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		name := entry.Name()
		if name == "handoff.json" && keepFinal {
			continue
		}
		if !keepFinal && name == "agent-handoff.json" && keepSemantic {
			continue
		}
		// The switch directory is private and switch-unique. Once the source has
		// stopped or the saga is terminal, every sibling is temporary/unowned,
		// including editor backups or alternate files the source happened to
		// create. Remove the whole entry without following a top-level symlink so
		// the verified final is literally the sole retained handoff file.
		if removeErr := os.RemoveAll(filepath.Join(dir, name)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, removeErr)
		}
	}
	if removeErr := os.Remove(dir); removeErr == nil {
		sessionDir := filepath.Dir(dir)
		_ = os.Remove(sessionDir) // succeeds only when this AO-owned directory is empty.
	}
	return errors.Join(errs...)
}

func removeTemporaryAgentHandoff(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *Manager) removeTemporaryAgentHandoff(ctx context.Context, sessionID domain.SessionID, switchID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := m.handoffDirectory(sessionID, switchID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateHandoffDirectoryChain(dir); err != nil {
		return fmt.Errorf("agent switch: refuse unsafe candidate cleanup: %w", err)
	}
	return removeTemporaryAgentHandoff(ctx, filepath.Join(dir, "agent-handoff-candidate.json"))
}

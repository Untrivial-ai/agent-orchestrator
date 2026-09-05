package conpty

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// protocol-v2 hosts shipped before authenticated status responses. They
// cannot be taught a token after they already own a live PTY, so upgrade
// recovery proves the immutable OS process incarnation and its listener
// instead. New hosts never use this path.
const (
	legacyRegistrationWindow = 30 * time.Second
	// The initial Windows proof may cold-start PowerShell and CIM twice. Keep
	// this separate from the short loopback STATUS probe deadline.
	legacyIdentityProofTimeout = 15 * time.Second
)

type legacyProcessIdentity struct {
	pid        int
	ppid       int
	startedAt  time.Time
	executable string
	argv       []string
}

type legacyHostIdentityEvidence struct {
	listenerPID int
	host        legacyProcessIdentity
	child       *legacyProcessIdentity
}

type legacyHostIdentityFingerprint struct {
	hostPID        int
	hostStartedAt  time.Time
	childPID       int
	childStartedAt time.Time
}

type legacyProcessIncarnation struct {
	pid         int
	ppid        int
	parentKnown bool
	startedAt   time.Time
}

// collectLegacyHostIdentity owns the platform-independent proof sequence. The
// build-tagged files provide only the OS primitives for listener ownership and
// process inspection, keeping the security algorithm identical on every
// supported desktop platform.
func collectLegacyHostIdentity(ctx context.Context, sess *hostSession, status StatusPayload) (legacyHostIdentityEvidence, error) {
	listenerPID, err := legacyListenerPID(ctx, sess.addr, sess.pid)
	if err != nil {
		return legacyHostIdentityEvidence{}, err
	}
	host, err := legacyProcessIdentityForPID(ctx, sess.pid)
	if err != nil {
		return legacyHostIdentityEvidence{}, fmt.Errorf("inspect recorded host pid %d: %w", sess.pid, err)
	}
	evidence := legacyHostIdentityEvidence{listenerPID: listenerPID, host: host}
	if status.Alive {
		child, childErr := legacyProcessIdentityForPID(ctx, status.PID)
		if childErr != nil {
			return legacyHostIdentityEvidence{}, fmt.Errorf("inspect status child pid %d: %w", status.PID, childErr)
		}
		evidence.child = &child
	}
	return evidence, nil
}

func revalidateLegacyHostIdentity(ctx context.Context, sess *hostSession, status StatusPayload, proof legacyHostIdentityFingerprint) error {
	if proof.hostPID != sess.pid {
		return fmt.Errorf("cached host pid %d does not match registry pid %d", proof.hostPID, sess.pid)
	}
	listenerPID, err := legacyListenerPID(ctx, sess.addr, sess.pid)
	if err != nil {
		return err
	}
	if listenerPID != sess.pid {
		return fmt.Errorf("listener owner pid = %d, want recorded host pid %d", listenerPID, sess.pid)
	}
	host, err := legacyProcessIncarnationForPID(ctx, sess.pid)
	if err != nil {
		return fmt.Errorf("revalidate recorded host pid %d: %w", sess.pid, err)
	}
	if host.pid != sess.pid || !host.startedAt.Equal(proof.hostStartedAt) {
		return fmt.Errorf("recorded host pid %d changed process incarnation", sess.pid)
	}
	if !status.Alive {
		return nil
	}
	if status.PID != proof.childPID || proof.childPID <= 0 {
		return fmt.Errorf("legacy status child pid changed from %d to %d", proof.childPID, status.PID)
	}
	child, err := legacyProcessIncarnationForPID(ctx, status.PID)
	if err != nil {
		return fmt.Errorf("revalidate status child pid %d: %w", status.PID, err)
	}
	if child.pid != status.PID || !child.startedAt.Equal(proof.childStartedAt) ||
		(child.parentKnown && child.ppid != sess.pid) {
		return fmt.Errorf("status child pid %d changed process incarnation or parent", status.PID)
	}
	return nil
}

func (r *Runtime) verifyLegacyHostIdentity(ctx context.Context, sess *hostSession, status StatusPayload) error {
	if err := validateLegacyStatusEnvelope(status); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, legacyIdentityProofTimeout)
	defer cancel()

	sess.legacyMu.Lock()
	defer sess.legacyMu.Unlock()
	if sess.legacyProof != nil {
		return r.legacyRevalidator(verifyCtx, sess, status, *sess.legacyProof)
	}
	evidence, err := r.legacyCollector(verifyCtx, sess, status)
	if err != nil {
		return err
	}
	if err := validateLegacyHostIdentity(sess, status, evidence); err != nil {
		return err
	}
	proof := legacyHostIdentityFingerprint{
		hostPID:       evidence.host.pid,
		hostStartedAt: evidence.host.startedAt,
	}
	if evidence.child != nil {
		proof.childPID = evidence.child.pid
		proof.childStartedAt = evidence.child.startedAt
	}
	sess.legacyProof = &proof
	return nil
}

func validateLegacyHostIdentity(sess *hostSession, status StatusPayload, evidence legacyHostIdentityEvidence) error {
	if err := validateLegacyStatusEnvelope(status); err != nil {
		return err
	}
	if evidence.listenerPID != sess.pid {
		return fmt.Errorf("listener owner pid = %d, want recorded host pid %d", evidence.listenerPID, sess.pid)
	}
	if evidence.host.pid != sess.pid {
		return fmt.Errorf("host process pid = %d, want %d", evidence.host.pid, sess.pid)
	}
	if !isAOExecutable(evidence.host.executable) {
		return fmt.Errorf("host executable %q is not AO", evidence.host.executable)
	}
	if len(evidence.host.argv) < 4 ||
		!isAOExecutable(evidence.host.argv[0]) ||
		evidence.host.argv[1] != "pty-host" ||
		evidence.host.argv[2] != sess.sessionID {
		return fmt.Errorf("host argv does not identify pty-host session %q", sess.sessionID)
	}
	if sessionID, launchID, found := supervisedOwnerFromArgv(evidence.host.argv[4:]); sess.launchID != "" {
		if !found || sessionID != sess.sessionID || launchID != sess.launchID {
			return fmt.Errorf("host supervisor argv does not match session/launch ownership")
		}
	} else if found && (sessionID != sess.sessionID || launchID != "") {
		return fmt.Errorf("host supervisor argv does not match auxiliary session ownership")
	}

	registeredAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(sess.registeredAt))
	if err != nil {
		return fmt.Errorf("parse legacy registry timestamp %q: %w", sess.registeredAt, err)
	}
	if evidence.host.startedAt.IsZero() {
		return fmt.Errorf("host process start time is unavailable")
	}
	// Registry publication follows READY immediately. Allow sub-second
	// truncation to put the RFC3339 value just before process creation, but a
	// materially different time means this PID belongs to a later incarnation.
	if registeredAt.Before(evidence.host.startedAt.Add(-time.Second)) ||
		registeredAt.After(evidence.host.startedAt.Add(legacyRegistrationWindow)) {
		return fmt.Errorf(
			"host process start %s does not match registry publication %s",
			evidence.host.startedAt.UTC().Format(time.RFC3339Nano),
			registeredAt.UTC().Format(time.RFC3339Nano),
		)
	}

	if status.Alive {
		if status.PID <= 0 || evidence.child == nil {
			return fmt.Errorf("live legacy host did not yield child process identity")
		}
		if evidence.child.pid != status.PID || evidence.child.ppid != sess.pid {
			return fmt.Errorf(
				"status child pid %d is not parented by recorded host pid %d",
				status.PID,
				sess.pid,
			)
		}
		if evidence.child.startedAt.Before(evidence.host.startedAt) {
			return fmt.Errorf("status child predates its recorded pty-host")
		}
		if sess.launchID != "" && !isAOExecutable(evidence.child.executable) {
			return fmt.Errorf("status child executable %q is not AO supervisor", evidence.child.executable)
		}
		if sessionID, launchID, found := supervisedOwnerFromArgv(evidence.child.argv); sess.launchID != "" {
			if !found || sessionID != sess.sessionID || launchID != sess.launchID {
				return fmt.Errorf("status child supervisor argv does not match session/launch ownership")
			}
		} else if found && (sessionID != sess.sessionID || launchID != "") {
			return fmt.Errorf("status child supervisor argv does not match auxiliary session ownership")
		}
	}
	return nil
}

func validateLegacyStatusEnvelope(status StatusPayload) error {
	if status.ProtocolVersion != conPTYStyledOutputProtocolVersion {
		return fmt.Errorf("legacy protocol version = %d, want %d", status.ProtocolVersion, conPTYStyledOutputProtocolVersion)
	}
	if status.SessionID != "" || status.LaunchID != "" || status.HostPID != 0 || status.HostToken != "" {
		return fmt.Errorf("legacy status unexpectedly contains partial authenticated identity")
	}
	return nil
}

func isAOExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	name = strings.TrimSuffix(name, ".exe")
	return name == "ao" || name == "agent-orchestrator"
}

// normalizeLinuxProcExecutable removes the exact suffix Linux adds to
// /proc/<pid>/exe after the running executable has been unlinked. Strip only
// one trailing kernel marker so lookalike paths remain rejected.
func normalizeLinuxProcExecutable(path string) string {
	return strings.TrimSuffix(path, " (deleted)")
}

func supervisedOwnerFromArgv(argv []string) (sessionID, launchID string, found bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "agent-process" || argv[i+1] != "supervise" {
			continue
		}
		for j := i + 2; j+1 < len(argv); j++ {
			switch argv[j] {
			case "--session":
				sessionID = argv[j+1]
				j++
			case "--launch":
				launchID = argv[j+1]
				j++
			case "--":
				return sessionID, launchID, true
			}
		}
		return sessionID, launchID, true
	}
	return "", "", false
}

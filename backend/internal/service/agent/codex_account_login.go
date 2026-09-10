package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

func (m *codexAccountManager) openLoginTerminal(ctx context.Context, targetAccountID string) (CodexAccountLoginTerminalStart, error) {
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	defer release()
	if m.terminal == nil || m.executable == nil {
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex login terminal is unavailable")
	}
	targetAccountID = strings.TrimSpace(targetAccountID)
	if targetAccountID != "" {
		record, ok := m.catalog.record(targetAccountID)
		if !ok || (record.Snapshot.Status != domain.CodexAccountStatusValid && record.Snapshot.Status != domain.CodexAccountStatusSignedOut) {
			return CodexAccountLoginTerminalStart{}, apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
		}
	}
	id := m.newID()
	now := m.now()
	snapshot := domain.CodexAccountLoginOperation{OperationID: id, AccountID: targetAccountID, Status: domain.CodexAccountLoginPending, ReasonCode: domain.CodexAccountLoginReasonPending, Reason: "Waiting for Codex sign-in.", ExpiresAt: now.Add(codexAccountLoginLifetime)}
	m.mu.Lock()
	if m.login != nil && !terminalLoginStatus(m.login.snapshot.Status) {
		m.mu.Unlock()
		return CodexAccountLoginTerminalStart{}, apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "A Codex account login is already in progress", nil)
	}
	previous := m.login
	m.login = &accountLoginOperation{snapshot: snapshot, targetAccountID: targetAccountID}
	m.mu.Unlock()
	if previous != nil {
		m.cleanupLoginFiles(previous)
	}
	pendingDir, home, err := createPendingCredentialHome(m.pendingRoot, id)
	if err != nil {
		m.clearLoginReservation(id)
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex login could not be prepared")
	}
	executable, err := m.executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		_ = os.RemoveAll(pendingDir)
		m.clearLoginReservation(id)
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex login terminal is unavailable")
	}
	title := "Add Codex account"
	if targetAccountID != "" {
		title = "Sign in to Codex account"
	}
	terminal, err := m.terminal.OpenCommandTerminal(ctx, shellterm.OpenCommandTerminalInput{Argv: []string{executable, "codex-login"}, Env: map[string]string{"CODEX_HOME": home}, WorkingDir: home, Title: title})
	if err != nil {
		_ = os.RemoveAll(pendingDir)
		m.clearLoginReservation(id)
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex login terminal could not be opened")
	}
	m.mu.Lock()
	if m.login == nil || m.login.snapshot.OperationID != id {
		m.mu.Unlock()
		_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminal.HandleID)
		_ = os.RemoveAll(pendingDir)
		return CodexAccountLoginTerminalStart{}, apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "A Codex account login changed concurrently", nil)
	}
	m.login.pendingDir, m.login.home, m.login.terminalHandle = pendingDir, home, terminal.HandleID
	m.login.terminalTitle, m.login.terminalCreated = terminal.Title, terminal.CreatedAt
	m.mu.Unlock()
	go m.expireLogin(m.ctx, id, snapshot.ExpiresAt)
	m.publish()
	return CodexAccountLoginTerminalStart{Operation: snapshot, ShellTerminal: terminal}, nil
}

func (m *codexAccountManager) clearLoginReservation(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.login != nil && m.login.snapshot.OperationID == id {
		m.login = nil
	}
}

func (m *codexAccountManager) cleanupLoginFiles(op *accountLoginOperation) {
	if op == nil {
		return
	}
	if op.terminalHandle != "" && m.terminal != nil {
		_ = m.terminal.CloseShellTerminal(context.Background(), op.terminalHandle)
	}
	if op.pendingDir != "" {
		_ = os.RemoveAll(op.pendingDir)
	}
}

func terminalLoginStatus(status domain.CodexAccountLoginStatus) bool {
	return status == domain.CodexAccountLoginCompleted || status == domain.CodexAccountLoginCancelled || status == domain.CodexAccountLoginExpired || status == domain.CodexAccountLoginFailed
}

func (m *codexAccountManager) verifyLogin(ctx context.Context, operationID string) (domain.CodexAccountLoginOperation, error) {
	m.mu.Lock()
	op := m.login
	if op == nil || op.snapshot.OperationID != operationID {
		m.mu.Unlock()
		return domain.CodexAccountLoginOperation{}, apierr.NotFound("CODEX_ACCOUNT_LOGIN_NOT_FOUND", "Codex account login operation not found")
	}
	if terminalLoginStatus(op.snapshot.Status) {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	if op.closing || op.committing {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	if op.snapshot.Status == domain.CodexAccountLoginVerifying {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	op.snapshot.Status = domain.CodexAccountLoginVerifying
	op.snapshot.Reason = "Verifying the Codex account."
	home, pendingDir, terminalHandle := op.home, op.pendingDir, op.terminalHandle
	m.mu.Unlock()
	m.publish()
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-m.ctx.Done():
		return domain.CodexAccountLoginOperation{}, m.ctx.Err()
	}
	verifyCtx, cancel := context.WithTimeout(m.ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: home, Managed: true})
	if err != nil {
		return m.finishLoginUnverified(operationID, "Codex could not verify this account."), nil
	}
	observation, readErr := client.Read(verifyCtx, true)
	_ = client.Close()
	if readErr != nil || observation.Authentication == domain.AgentAuthenticationUnknown {
		return m.finishLoginUnverified(operationID, "Codex could not verify this account."), nil
	}
	if observation.Authentication == domain.AgentAuthenticationUnauthorized {
		return m.finishLogin(operationID, domain.CodexAccountLoginUnauthorized, domain.CodexAccountLoginReasonUnauthorized, "Codex is still signed out.", nil), nil
	}
	if observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable {
		return m.finishLoginUnverified(operationID, "Codex could not verify this account."), nil
	}
	exclusive, exclusiveErr := m.acquireGlobalMutation(ctx)
	if exclusiveErr != nil {
		return domain.CodexAccountLoginOperation{}, exclusiveErr
	}
	if exclusive != nil {
		defer exclusive.Release()
	}
	releaseMutation, mutationErr := m.acquireAccountMutation(ctx)
	if mutationErr != nil {
		return domain.CodexAccountLoginOperation{}, mutationErr
	}
	defer releaseMutation()
	m.mu.Lock()
	op = m.login
	if op == nil || op.snapshot.OperationID != operationID || terminalLoginStatus(op.snapshot.Status) || op.closing {
		var result domain.CodexAccountLoginOperation
		if op != nil && op.snapshot.OperationID == operationID {
			result = op.snapshot
		}
		m.mu.Unlock()
		return result, nil
	}
	op.committing = true
	op.commitDone = make(chan struct{})
	commitDone := op.commitDone
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.login != nil && m.login.snapshot.OperationID == operationID && m.login.commitDone == commitDone {
			m.login.committing = false
			close(commitDone)
			m.login.commitDone = nil
		}
		m.mu.Unlock()
	}()
	targetAccountID := op.targetAccountID
	var record codexAccountRecord
	if targetAccountID != "" {
		target, targetFound := m.catalog.record(targetAccountID)
		if !targetFound || !codexObservationMatchesAccount(target.Snapshot, observation) {
			return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "Sign in with the same Codex account to replace its credentials.", nil), nil
		}
		credential, credentialErr := readOpaqueCredential(filepath.Join(home, codexCredentialFilename))
		if credentialErr != nil {
			return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The verified Codex account could not be saved.", nil), nil
		}
		m.mu.Lock()
		active := m.active
		m.mu.Unlock()
		if active.AccountID == targetAccountID {
			if descriptorErr := m.catalog.updateVerifiedDescriptor(targetAccountID, observation); descriptorErr != nil {
				return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The verified Codex account could not be saved.", nil), nil
			}
			if _, activateErr := m.activateFromCredentialLocked(m.ctx, targetAccountID, active.Revision, filepath.Join(home, codexCredentialFilename), nil); activateErr != nil {
				return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The account was verified but could not be activated.", nil), nil
			}
			record, _ = m.catalog.record(targetAccountID)
		} else {
			var replaceErr error
			record, replaceErr = m.catalog.replaceCredential(targetAccountID, credential, observation)
			if replaceErr != nil {
				return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The verified Codex account could not be saved.", nil), nil
			}
		}
		_ = os.RemoveAll(pendingDir)
		m.clearReauthenticationRequired(targetAccountID)
		m.catalog.updateSnapshot(targetAccountID, func(snapshot *domain.CodexAccountSnapshot) {
			snapshot.Authentication = accountAuthenticationObservation(m.now(), observation.Authentication)
			snapshot.AuthMethod = observation.Method
			snapshot.AccountEmail = observation.Email
			snapshot.Label = accountLabel(snapshot.ID, observation.Method, observation.Email)
		})
	} else {
		var err error
		record, err = m.catalog.commitPending(pendingDir, observation)
		if err != nil {
			return m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The verified Codex account could not be saved.", nil), nil
		}
		m.catalog.updateSnapshot(record.Snapshot.ID, func(s *domain.CodexAccountSnapshot) {
			s.Authentication = accountAuthenticationObservation(m.now(), observation.Authentication)
			s.AuthMethod = observation.Method
			s.AccountEmail = observation.Email
			s.Label = accountLabel(s.ID, observation.Method, observation.Email)
		})
		m.mu.Lock()
		activateFirst := m.active.AccountID == "" && m.globalAuth.State == domain.AgentAuthenticationUnauthorized
		m.mu.Unlock()
		if activateFirst {
			m.mu.Lock()
			expectedRevision := m.active.Revision
			m.mu.Unlock()
			if err := m.activateLocked(m.ctx, record.Snapshot.ID, expectedRevision); err != nil {
				result := m.finishLogin(operationID, domain.CodexAccountLoginFailed, domain.CodexAccountLoginReasonFailed, "The account was saved but could not be activated.", &record.Snapshot)
				if m.terminal != nil && terminalHandle != "" {
					_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminalHandle)
				}
				return result, nil
			}
		}
	}
	latest, _ := m.catalog.record(record.Snapshot.ID)
	snapshot := latest.Snapshot
	snapshot.Active = snapshot.ID == m.activeAccountID()
	reason := "Codex account added."
	if targetAccountID != "" {
		reason = "Codex account signed in."
	}
	result := m.finishLogin(operationID, domain.CodexAccountLoginCompleted, domain.CodexAccountLoginReasonCompleted, reason, &snapshot)
	if m.terminal != nil && terminalHandle != "" {
		_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminalHandle)
	}
	return result, nil
}

func (m *codexAccountManager) finishLoginUnverified(id, reason string) domain.CodexAccountLoginOperation {
	return m.finishLogin(id, domain.CodexAccountLoginUnverified, domain.CodexAccountLoginReasonUnverified, reason, nil)
}
func (m *codexAccountManager) finishLogin(id string, status domain.CodexAccountLoginStatus, code, reason string, account *domain.CodexAccountSnapshot) domain.CodexAccountLoginOperation {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.login == nil || m.login.snapshot.OperationID != id {
		return domain.CodexAccountLoginOperation{}
	}
	if m.login.closing && status != domain.CodexAccountLoginCancelled && status != domain.CodexAccountLoginExpired {
		return m.login.snapshot
	}
	if terminalLoginStatus(m.login.snapshot.Status) && m.login.snapshot.Status != status {
		return m.login.snapshot
	}
	m.login.snapshot.Status, m.login.snapshot.ReasonCode, m.login.snapshot.Reason, m.login.snapshot.Account = status, code, reason, account
	result := m.login.snapshot
	go m.publish()
	return result
}

func (m *codexAccountManager) cancelLogin(ctx context.Context, operationID string) (domain.CodexAccountLoginOperation, error) {
	for {
		m.mu.Lock()
		op := m.login
		if op == nil || op.snapshot.OperationID != operationID {
			m.mu.Unlock()
			return domain.CodexAccountLoginOperation{}, apierr.NotFound("CODEX_ACCOUNT_LOGIN_NOT_FOUND", "Codex account login operation not found")
		}
		if terminalLoginStatus(op.snapshot.Status) {
			result := op.snapshot
			m.mu.Unlock()
			return result, nil
		}
		if op.committing && op.commitDone != nil {
			done := op.commitDone
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return domain.CodexAccountLoginOperation{}, ctx.Err()
			}
		}
		if op.closing {
			result := op.snapshot
			m.mu.Unlock()
			return result, nil
		}
		op.closing = true
		handle, pending := op.terminalHandle, op.pendingDir
		m.mu.Unlock()
		if handle != "" && m.terminal != nil {
			if err := m.terminal.CloseShellTerminal(ctx, handle); err != nil {
				m.mu.Lock()
				if m.login != nil && m.login.snapshot.OperationID == operationID {
					m.login.closing = false
					m.login.snapshot.Status = domain.CodexAccountLoginUnverified
					m.login.snapshot.ReasonCode = domain.CodexAccountLoginReasonUnverified
					m.login.snapshot.Reason = "Codex login terminal could not be closed."
				}
				m.mu.Unlock()
				m.publish()
				return domain.CodexAccountLoginOperation{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex login terminal could not be closed")
			}
		}
		_ = os.RemoveAll(pending)
		return m.finishLogin(operationID, domain.CodexAccountLoginCancelled, domain.CodexAccountLoginReasonCancelled, "Codex account login was cancelled.", nil), nil
	}
}

func (m *codexAccountManager) expireLogin(ctx context.Context, id string, at time.Time) {
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.ctx.Done():
		return
	}
	for {
		m.mu.Lock()
		op := m.login
		if op == nil || op.snapshot.OperationID != id || terminalLoginStatus(op.snapshot.Status) || op.closing {
			m.mu.Unlock()
			return
		}
		if op.committing && op.commitDone != nil {
			done := op.commitDone
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-m.ctx.Done():
				return
			}
		}
		op.closing = true
		pending, handle := op.pendingDir, op.terminalHandle
		m.mu.Unlock()
		if pending == "" {
			return
		}
		if handle != "" && m.terminal != nil {
			_ = m.terminal.CloseShellTerminal(ctx, handle)
		}
		_ = os.RemoveAll(pending)
		m.finishLogin(id, domain.CodexAccountLoginExpired, domain.CodexAccountLoginReasonExpired, "Codex account login expired.", nil)
		return
	}
}

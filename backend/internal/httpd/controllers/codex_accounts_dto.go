package controllers

import (
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

func newCodexAccountsResponse(input agentsvc.CodexAccounts) CodexAccountsResponse {
	accounts := make([]CodexAccountResponse, len(input.Accounts))
	for i := range input.Accounts {
		accounts[i] = newCodexAccountResponse(input.Accounts[i])
	}
	response := CodexAccountsResponse{
		ActiveAccountID: input.ActiveAccountID, AccountRevision: input.AccountRevision,
		Accounts: accounts, Capabilities: newCodexCapabilitiesResponse(input.Capabilities),
	}
	if input.UnmanagedGlobalAccount != nil {
		response.UnmanagedGlobalAccount = &CodexUnmanagedGlobalAccountResponse{
			Label: input.UnmanagedGlobalAccount.Label, AuthMethod: string(input.UnmanagedGlobalAccount.AuthMethod),
			AccountEmail: input.UnmanagedGlobalAccount.AccountEmail,
			ReasonCode:   input.UnmanagedGlobalAccount.ReasonCode, Reason: input.UnmanagedGlobalAccount.Reason,
		}
	}
	if input.ActiveLogin != nil {
		response.ActiveLogin = &CodexActiveLoginResponse{
			OperationID: input.ActiveLogin.OperationID, AccountID: input.ActiveLogin.AccountID,
			Status: string(input.ActiveLogin.Status), ReasonCode: input.ActiveLogin.ReasonCode,
			Reason: input.ActiveLogin.Reason, ExpiresAt: input.ActiveLogin.ExpiresAt,
			ShellTerminal: CodexAccountLoginTerminalResponse{
				HandleID: input.ActiveLogin.ShellTerminal.HandleID, Title: input.ActiveLogin.ShellTerminal.Title,
				CreatedAt: input.ActiveLogin.ShellTerminal.CreatedAt,
			},
		}
	}
	if input.CurrentSwitch != nil {
		switchResponse := newCodexSwitchResponse(*input.CurrentSwitch)
		response.CurrentSwitch = &switchResponse
	}
	return response
}

func newCodexAccountResponse(input domain.CodexAccountSnapshot) CodexAccountResponse {
	response := CodexAccountResponse{
		ID: input.ID, Label: input.Label, Status: string(input.Status), ReasonCode: input.ReasonCode, Reason: input.Reason,
		Active: input.Active, AuthMethod: string(input.AuthMethod), AccountEmail: input.AccountEmail, CreatedAt: input.CreatedAt,
		Authentication: CodexAuthenticationResponse{
			State: string(input.Authentication.State), Freshness: string(input.Authentication.Freshness),
			CheckedAt: input.Authentication.CheckedAt, AttemptedAt: input.Authentication.AttemptedAt,
			ReasonCode: input.Authentication.ReasonCode, Reason: input.Authentication.Reason,
		},
		Capacity: newCodexCapacityResponse(input.Capacity),
	}
	if input.UsageSummary != nil {
		response.UsageSummary = &CodexAccountUsageSummaryResponse{
			LatestDayTokens: input.UsageSummary.LatestDayTokens, LatestDayStartDate: input.UsageSummary.LatestDayStartDate,
			LifetimeTokens: input.UsageSummary.LifetimeTokens, PeakDailyTokens: input.UsageSummary.PeakDailyTokens,
			LongestRunningTurnSeconds: input.UsageSummary.LongestRunningTurnSeconds,
			CurrentStreakDays:         input.UsageSummary.CurrentStreakDays, LongestStreakDays: input.UsageSummary.LongestStreakDays,
			ObservedAt: input.UsageSummary.ObservedAt,
		}
	}
	return response
}

func newCodexCapacityResponse(input domain.CodexCapacitySnapshot) CodexAccountCapacityResponse {
	additional := make([]CodexCapacityBucketResponse, len(input.AdditionalBuckets))
	for i := range input.AdditionalBuckets {
		additional[i] = newCodexCapacityBucketResponse(input.AdditionalBuckets[i])
	}
	response := CodexAccountCapacityResponse{
		State: string(input.State), Freshness: string(input.Freshness), Plan: input.Plan,
		UsedPercent: input.UsedPercent, RemainingPercent: input.RemainingPercent,
		ResetsAt: input.ResetsAt, ObservedAt: input.ObservedAt, CheckedAt: input.CheckedAt, AttemptedAt: input.AttemptedAt,
		ReasonCode: input.ReasonCode, Reason: input.Reason, AdditionalBuckets: additional,
	}
	if input.Overall != nil {
		overall := newCodexCapacityBucketResponse(*input.Overall)
		response.Overall = &overall
	}
	if input.ResetCredits != nil {
		response.ResetCredits = &CodexResetCreditsSummaryResponse{
			AvailableCount: input.ResetCredits.AvailableCount, NearestExpiresAt: input.ResetCredits.NearestExpiresAt,
		}
	}
	return response
}

func newCodexCapacityBucketResponse(input domain.CodexCapacityBucket) CodexCapacityBucketResponse {
	response := CodexCapacityBucketResponse{DisplayName: input.DisplayName, Reached: string(input.Reached)}
	if input.Primary != nil {
		response.Primary = &CodexCapacityWindowResponse{
			UsedPercent: input.Primary.UsedPercent, WindowDurationMinutes: input.Primary.WindowDurationMinutes, ResetsAt: input.Primary.ResetsAt,
		}
	}
	if input.Secondary != nil {
		response.Secondary = &CodexCapacityWindowResponse{
			UsedPercent: input.Secondary.UsedPercent, WindowDurationMinutes: input.Secondary.WindowDurationMinutes, ResetsAt: input.Secondary.ResetsAt,
		}
	}
	return response
}

func newCodexCapabilitiesResponse(input domain.CodexAccountCapabilities) CodexAccountCapabilitiesResponse {
	return CodexAccountCapabilitiesResponse{
		NativeLogin:        newCodexCapabilityResponse(input.NativeLogin),
		ResetCreditConsume: newCodexCapabilityResponse(input.ResetCreditConsume),
		GlobalSwitch:       newCodexCapabilityResponse(input.GlobalSwitch),
	}
}

func newCodexCapabilityResponse(input domain.CodexCapabilityObservation) CodexCapabilityObservationResponse {
	return CodexCapabilityObservationResponse{State: string(input.State), ReasonCode: input.ReasonCode, Reason: input.Reason}
}

func newCodexLoginResponse(input domain.CodexAccountLoginOperation) CodexAccountLoginResponse {
	response := CodexAccountLoginResponse{
		OperationID: input.OperationID, AccountID: input.AccountID, Status: string(input.Status),
		ReasonCode: input.ReasonCode, Reason: input.Reason, ExpiresAt: input.ExpiresAt,
	}
	if input.Account != nil {
		account := newCodexAccountResponse(*input.Account)
		response.Account = &account
	}
	return response
}

func newCodexSwitchResponse(input domain.CodexAccountSwitch) CodexAccountSwitchResponse {
	sessions := make([]CodexAccountSwitchSessionResponse, len(input.Sessions))
	for i := range input.Sessions {
		session := input.Sessions[i]
		sessions[i] = CodexAccountSwitchSessionResponse{
			SessionID: string(session.SessionID), InterfaceMode: string(session.InterfaceMode), WasRunning: session.WasRunning,
			StopState: session.StopState, RestartState: session.RestartState, ErrorCode: redactedCodexSwitchSessionErrorCode(session.ErrorCode),
			StoppedAt: session.StoppedAt, RestartedAt: session.RestartedAt,
		}
	}
	return CodexAccountSwitchResponse{
		ID: input.ID, SourceAccountID: input.SourceAccountID, TargetAccountID: input.TargetAccountID,
		Phase: CodexAccountSwitchPhase(input.Phase), FailureCode: input.FailureCode, Sessions: sessions,
		CanRecover: input.CanRecover, CredentialsCommittedAt: input.CredentialsCommittedAt,
		CreatedAt: input.CreatedAt, UpdatedAt: input.UpdatedAt, CompletedAt: input.CompletedAt,
	}
}

func redactedCodexSwitchSessionErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if prefix, _, found := strings.Cut(code, ":"); found {
		return prefix
	}
	return code
}

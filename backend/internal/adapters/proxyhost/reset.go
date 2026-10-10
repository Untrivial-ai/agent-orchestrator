package proxyhost

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// resetAnswers names a provider's verdict; a claim already honoured counts as spent.
var resetAnswers = map[string]string{
	"reset": domain.ProviderResetDone, "already_redeemed": domain.ProviderResetDone, "already_used": domain.ProviderResetDone,
	"nothing_to_reset": domain.ProviderResetNothing, "not_limited": domain.ProviderResetNothing,
	"no_credit": domain.ProviderResetNone, "ineligible": domain.ProviderResetNone,
	"cooldown": domain.ProviderResetWait, "unavailable": domain.ProviderResetFailed,
}

// AccountAction resumes an account, renews its sign-in, or spends one reset.
func (c *Client) AccountAction(ctx context.Context, a domain.ProviderAccount, action, requestID string) (outcome string, err error) {
	switch {
	case action == ports.AccountActionResume:
		err = c.call(ctx, http.MethodPost, "/ao/account-resume", on(a), nil)
	case action == ports.AccountActionRefresh:
		err = c.call(ctx, http.MethodPost, "/ao/account-refresh", on(a), nil)
	case action == ports.AccountActionReset && !a.APIKey():
		// Detached from the caller: once a claim is sent, its answer must be read.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if outcome, err = c.reset(ctx, a, requestID); outcome == domain.ProviderResetDone {
			// The limit is gone at the provider; stop holding the account back here.
			_ = c.call(ctx, http.MethodPost, "/ao/account-resume", on(a), nil)
		}
	default:
		return "", ports.ErrProviderAccountActionUnavailable
	}
	if status := code(err); status == http.StatusBadRequest || status == http.StatusNotFound {
		err = ports.ErrProviderAccountActionUnavailable
	}
	return outcome, err
}

// reset spends one reset; for Claude only a grant the provider said it would accept.
func (c *Client) reset(ctx context.Context, a domain.ProviderAccount, requestID string) (string, error) {
	if a.Provider != "claude" {
		return c.spend(ctx, a, codexAPI+"/rate-limit-reset-credits/consume", "code", map[string]string{"redeem_request_id": requestID})
	}
	organization := text(c.read(ctx, a, claudeProfileURL, quick), "organization", "uuid")
	status := at(c.read(ctx, a, claudeGrantsURL, quick), "cedar_ember")
	now, atLimit, next := time.Now(), at(status, "at_limit") == true, text(status, "next_grant_id")
	switch {
	case organization == "" || status == nil:
		return domain.ProviderResetFailed, nil
	case at(status, "eligible") != true:
		return domain.ProviderResetNone, nil
	case after(text(status, "cooldown_until"), now):
		return domain.ProviderResetWait, nil
	}
	grants, _ := at(status, "grants").([]any)
	grantID, left := "", 0.0
	for _, grant := range grants {
		count, _ := number(grant, "resets_left")
		left += count
		// The grant the provider names next is claimed, else the first by id.
		if id := text(grant, "id"); spendable(grant, atLimit, now) && (grantID == "" || id == next || id < grantID && grantID != next) {
			grantID = id
		}
	}
	switch {
	case grantID == "" && left > 0 && !atLimit:
		return domain.ProviderResetNothing, nil
	case grantID == "":
		return domain.ProviderResetNone, nil
	}
	claim := map[string]string{"program": "cedar_ember", "grant_id": grantID, "request_id": requestID}
	return c.spend(ctx, a, claudeAPI+"/api/organizations/"+url.PathEscape(strings.ToLower(organization))+"/reset_rate_limits", "result", claim)
}

// spend sends one claim; an answer that does not rule it out leaves it unknown.
func (c *Client) spend(ctx context.Context, a domain.ProviderAccount, address, key string, claim map[string]string) (string, error) {
	status, answer, err := c.provider(ctx, a, http.MethodPost, address, claim)
	claude := a.Provider == "claude"
	switch {
	case code(err) == http.StatusBadRequest || code(err) == http.StatusNotFound:
		return "", err
	case err != nil:
		return domain.ProviderResetUnknown, nil //nolint:nilerr // an unconfirmed claim is unknown, not failed
	case status == http.StatusTooManyRequests:
		return domain.ProviderResetWait, nil
	case claude && (status == http.StatusUnauthorized || status == http.StatusForbidden):
		return domain.ProviderResetFailed, nil
	case status >= 500 || claude && status > 299:
		return domain.ProviderResetUnknown, nil
	case status > 299:
		return domain.ProviderResetFailed, nil
	}
	return cmp.Or(resetAnswers[text(answer, key)], domain.ProviderResetUnknown), nil
}

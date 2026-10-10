package host

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const accountHeader, providerHeader = "X-AO-Auth-ID", "X-AO-Provider"

// exactSelector picks the account the boundary named for the request, and never another.
type exactSelector struct{}

func (exactSelector) Pick(_ context.Context, provider, _ string, options executor.Options, candidates []*auth.Auth) (*auth.Auth, error) {
	id, want := options.Headers.Get(accountHeader), options.Headers.Get(providerHeader)
	for _, candidate := range candidates {
		if id != "" && (provider == want || provider == "mixed") && candidate != nil && candidate.ID == id && candidate.Provider == want && !candidate.Disabled && candidate.Status != auth.StatusDisabled {
			if options.Metadata != nil {
				options.Metadata[executor.PinnedAuthMetadataKey] = id
			}
			return candidate, nil
		}
	}
	return nil, &auth.Error{Code: "account_unavailable", Message: "The session account is unavailable. Sign in or choose another account.", HTTPStatus: 503}
}

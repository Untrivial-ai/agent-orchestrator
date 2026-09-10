package agentcreds

import (
	"context"
	"errors"
)

// ValidateLocal runs the whole local story: decide the provider, resolve the
// credential that provider will use, probe it, and fall back to the provider
// CLI when the credential is chain-sourced and therefore unreadable.
//
// It is the single call the daemon needs, and it upholds the additive rule at
// every branch — anything it cannot determine comes back Unknown.
func (v *Validator) ValidateLocal(ctx context.Context, reportedProvider string, opts ResolveOptions) Result {
	provider, ok := ResolveProvider(reportedProvider, opts)
	if !ok {
		// An apiProvider this build does not recognize. Probing anything now
		// would mean guessing which host should receive the credential.
		return Result{
			State: StateUnknown, CheckedAt: v.now(),
			Detail: "the configured API provider is not one this build can validate",
		}
	}

	cred, found := ResolveLocal(ctx, provider, opts)
	if !found {
		// Nothing readable. For Bedrock and Vertex that is the expected case
		// rather than an error: the credential almost certainly exists, in a
		// chain only the provider's own CLI can resolve.
		switch provider {
		case ProviderBedrock:
			return v.ValidateBedrockViaCLI(ctx, opts.env("AWS_REGION"))
		case ProviderVertex:
			project := firstNonEmpty(opts.env("ANTHROPIC_VERTEX_PROJECT_ID"), opts.env("GOOGLE_CLOUD_PROJECT"))
			region := firstNonEmpty(opts.env("CLOUD_ML_REGION"), opts.env("GOOGLE_CLOUD_REGION"), "us-east5")
			return v.ValidateVertexViaCLI(ctx, project, region)
		default:
			return Result{
				State: StateUnknown, Provider: provider, CheckedAt: v.now(),
				Detail: "no credential could be resolved for this provider",
			}
		}
	}

	result := v.Validate(ctx, cred)

	// A rejection that came from AO sending a malformed request is our bug,
	// not the user's revoked credential. Downgrade it rather than telling a
	// working user to sign in again.
	if result.State == StateInvalid && IsResolverBug(result.Detail) {
		return Result{
			State: StateUnknown, Provider: provider, Source: cred.Source,
			Fingerprint: cred.Fingerprint(), CheckedAt: result.CheckedAt,
			Detail: "the credential probe was malformed, so nothing can be concluded",
			Err:    result.Err,
		}
	}

	// A signed Bedrock or Vertex probe that could not even be built — a
	// malformed key file, a missing region — is worth one CLI attempt before
	// giving up, since the CLI needs none of what we were missing.
	if result.State == StateUnknown && result.Err != nil && !errors.Is(result.Err, ErrInvalidCredential) {
		switch provider {
		case ProviderBedrock:
			if cliResult := v.ValidateBedrockViaCLI(ctx, cred.Region); cliResult.State != StateUnknown {
				return cliResult
			}
		case ProviderVertex:
			if cliResult := v.ValidateVertexViaCLI(ctx, cred.Project, cred.Region); cliResult.State != StateUnknown {
				return cliResult
			}
		}
	}
	return result
}

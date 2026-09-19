package agentcreds

import (
	"context"
	"errors"
	"time"
)

// ValidateLocal runs the whole local story: decide the provider, resolve the
// credential that provider will use, probe it, and fall back to the provider
// CLI when the credential is chain-sourced and therefore unreadable.
//
// It is the single call the daemon needs, and it upholds the additive rule at
// every branch — anything it cannot determine comes back Unknown.
func (v *Validator) ValidateLocal(ctx context.Context, reportedProvider string, opts ResolveOptions) Result {
	if err := ctx.Err(); err != nil {
		return Result{State: StateUnknown, CheckedAt: time.Now(), Detail: "credential validation was canceled", Err: err}
	}
	provider, ok := ResolveProvider(reportedProvider, opts)
	if !ok {
		// An apiProvider this build does not recognize. Probing anything now
		// would mean guessing which host should receive the credential.
		return Result{
			State: StateUnknown, CheckedAt: time.Now(),
			Detail: "the configured API provider is not one this build can validate",
		}
	}
	cred, found := ResolveLocal(ctx, provider, opts)
	return v.ValidateResolvedLocal(ctx, provider, cred, found, opts)
}

// ValidateResolvedLocal validates a provider and credential that the caller
// already resolved from the same local environment.
func (v *Validator) ValidateResolvedLocal(
	ctx context.Context,
	provider Provider,
	cred Credential,
	found bool,
	opts ResolveOptions,
) Result {
	if provider == ProviderFoundry {
		return Result{
			State: StateUnknown, Provider: provider, Models: configuredFoundryModels(opts), CheckedAt: time.Now(),
			Detail: "Azure AI Foundry deployments were read from Claude configuration; invocation permission was not verified",
		}
	}

	if !found {
		// Nothing readable. For Bedrock and Vertex that is the expected case
		// rather than an error: the credential almost certainly exists, in a
		// chain only the provider's own CLI can resolve.
		switch provider {
		case ProviderBedrock:
			return v.validateBedrockViaCLI(ctx, opts.env("AWS_REGION"), opts.commandInvocation())
		case ProviderVertex:
			project := firstNonEmpty(opts.env("ANTHROPIC_VERTEX_PROJECT_ID"), opts.env("GOOGLE_CLOUD_PROJECT"))
			region := firstNonEmpty(opts.env("CLOUD_ML_REGION"), opts.env("GOOGLE_CLOUD_REGION"), "us-east5")
			return v.validateVertexViaCLI(ctx, project, region, opts.env("ANTHROPIC_VERTEX_BASE_URL"), opts.commandInvocation())
		default:
			return Result{
				State: StateUnknown, Provider: provider, CheckedAt: time.Now(),
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

	// A Vertex probe that could not be built is worth one gcloud attempt before
	// giving up, since gcloud can resolve credentials AO cannot read directly.
	if result.State == StateUnknown && result.Err != nil && !errors.Is(result.Err, ErrInvalidCredential) {
		if provider == ProviderVertex {
			if cliResult := v.validateVertexViaCLI(ctx, cred.Project, cred.Region, cred.BaseURL, opts.commandInvocation()); cliResult.State != StateUnknown {
				return cliResult
			}
		}
	}
	return result
}

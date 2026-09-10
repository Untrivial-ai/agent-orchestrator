// Package agentcreds validates coding-agent credentials against the provider
// that will actually be asked to honor them.
//
// The problem it exists to solve is that every local check answers "is a
// credential configured?" and reports it as "is the user authenticated?".
// Those are different questions. Validity is a server-side fact: a credential
// can be revoked, downgraded, or rate-limited with no change on disk, so only
// a network call can observe it.
//
// The package is shared by the desktop daemon and Cloud, which resolve
// credentials in opposite directions. Cloud is handed a secret explicitly and
// its hard problem is custody; the daemon discovers a secret locally and its
// hard problem is resolution — deciding which of several sources wins. Only
// the second half, the probe, is common to both, so that is what lives here:
// resolution is the caller's business, classification is ours.
//
// Three rules hold throughout.
//
// A verdict is three-state. "Invalid" requires the provider to have rejected a
// well-formed credential; everything else that goes wrong — a timeout, a
// malformed request, an endpoint that does not exist — is Unknown. A false
// negative locks out a working user and is strictly worse than the false
// positive of staying quiet.
//
// The header follows the credential's Kind and is never guessed. A token sent
// under the wrong header is rejected for the wrong reason, and the rejection
// is indistinguishable from a revoked credential.
//
// Every probe hits a model-listing endpoint, never a generic identity
// endpoint. For the cloud providers a valid credential is not the same as
// Claude access: working AWS credentials with no Bedrock entitlement
// authenticate perfectly and then fail at the first inference call.
package agentcreds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidCredential reports that the provider rejected the credential. It
// is returned only on a definite rejection, never on a failure to ask.
var ErrInvalidCredential = errors.New("agent credential is invalid or expired")

// State is the three-state result of a validation attempt.
type State string

const (
	// StateValid means the provider accepted the credential.
	StateValid State = "valid"
	// StateInvalid means the provider rejected it. This is the only state that
	// may be reported to a user as "you need to sign in again".
	StateInvalid State = "invalid"
	// StateUnknown means the question could not be answered. It is the safe
	// default and must never block anything.
	StateUnknown State = "unknown"
)

// Kind identifies a credential class, which selects the auth header. It is
// carried with the secret precisely so the header is never inferred.
type Kind string

const (
	// KindAPIKey is an Anthropic console key, sent as x-api-key.
	KindAPIKey Kind = "api_key"
	// KindOAuthToken is a claude.ai login or `claude setup-token` credential,
	// sent as a bearer token. Both are the same sk-ant-oat01- class.
	KindOAuthToken Kind = "oauth_token"
	// KindAuthToken is ANTHROPIC_AUTH_TOKEN, sent as a bearer token.
	KindAuthToken Kind = "auth_token"
	// KindAzureAPIKey is an Azure AI Foundry key, sent as api-key.
	KindAzureAPIKey Kind = "azure_api_key"
	// KindAWSSigV4 is a static AWS access key pair, which signs the request
	// rather than being sent verbatim.
	KindAWSSigV4 Kind = "aws_sigv4"
	// KindGoogleAccessToken is a short-lived Google OAuth access token.
	KindGoogleAccessToken Kind = "google_access_token"
	// KindGoogleServiceAccount is a service-account JSON key, which must be
	// exchanged for an access token before it can be sent.
	KindGoogleServiceAccount Kind = "google_service_account"
)

// Credential is a secret plus the metadata needed to send it correctly.
type Credential struct {
	// Kind selects the auth header or signing scheme.
	Kind Kind
	// Secret is the credential itself. It is never logged and never stored.
	Secret string
	// Source names where it came from, e.g. "ANTHROPIC_API_KEY" or "keychain".
	// It is safe to log and is the field that identifies a shadowing env var.
	Source string
	// Provider is the API surface that should be asked to honor it.
	Provider Provider
	// Region is required by Bedrock and Vertex; ignored elsewhere.
	Region string
	// Project is required by Vertex; ignored elsewhere.
	Project string
	// Resource is the Azure AI Foundry resource name; ignored elsewhere.
	Resource string
	// BaseURL overrides the provider's default endpoint. It is how a gateway
	// is validated, and how tests point a probe at a local server.
	BaseURL string
}

// Fingerprint returns sha256(secret)[:12], or "" when there is no secret.
// It is a cache key, never a credential: it is safe to log, and it changes
// when the underlying secret changes, which is what makes it usable to
// invalidate a cached verdict.
func (c Credential) Fingerprint() string { return Fingerprint(c.Secret) }

// Fingerprint returns sha256(secret)[:12], or "" for an empty secret.
func Fingerprint(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:12]
}

// Model is one model a provider reported, with the capabilities that differ
// per model and therefore cannot be hardcoded.
type Model struct {
	// ID is the provider's own identifier, used verbatim when launching.
	ID string
	// DisplayName is the provider's human label, when it supplies one.
	DisplayName string
	// Efforts are the reasoning levels this model accepts, in the provider's
	// order. Empty means the model takes no effort setting at all — which is a
	// real answer, not a missing one: Sonnet 4.5 and Haiku 4.5 support none,
	// Opus 4.6 omits xhigh, and the 5 family accepts all five. A hardcoded list
	// would be wrong for most of the catalog within a release.
	Efforts []string
}

// Result is a validation outcome together with what can be said about it.
type Result struct {
	// State is the three-state verdict.
	State State
	// Provider is the API surface that was asked.
	Provider Provider
	// Source echoes the credential's Source, for diagnostics.
	Source string
	// Fingerprint identifies the credential without revealing it.
	Fingerprint string
	// Models lists the Claude model IDs the provider reported, in that
	// provider's own ID format. Model IDs do not translate between providers,
	// so this is also the only correct source for a per-provider model picker.
	Models []Model
	// Detail is a human-readable explanation, safe to surface. It never
	// contains the credential.
	Detail string
	// CheckedAt is when the verdict was produced.
	CheckedAt time.Time
	// Err is the transport or protocol error behind an unknown verdict.
	Err error
}

// Valid reports whether the provider accepted the credential.
func (r Result) Valid() bool { return r.State == StateValid }

// anthropicAPIVersion is required on every first-party request.
const anthropicAPIVersion = "2023-06-01"

// DefaultTimeout bounds a single probe. Measured round-trips are ~350ms; this
// covers a bad network without stalling anything that waits on it.
const DefaultTimeout = 3 * time.Second

// maxBodyBytes caps how much of a provider response is read. Model lists are
// small; anything larger is a misrouted request, not a catalog.
const maxBodyBytes = 256 << 10

// Validator performs credential probes. The zero value is not usable; call
// New.
type Validator struct {
	client   *http.Client
	now      func() time.Time
	execCmd  commandRunner
	endpoint endpointOverrides
}

// Option configures a Validator.
type Option func(*Validator)

// WithHTTPClient sets the HTTP client used for probes.
func WithHTTPClient(client *http.Client) Option {
	return func(v *Validator) {
		if client != nil {
			v.client = client
		}
	}
}

// WithClock replaces the clock, for tests.
func WithClock(now func() time.Time) Option {
	return func(v *Validator) {
		if now != nil {
			v.now = now
		}
	}
}

// New builds a Validator.
func New(opts ...Option) *Validator {
	v := &Validator{
		client:  &http.Client{Timeout: DefaultTimeout},
		now:     time.Now,
		execCmd: execCommand,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// Validate probes the credential against its provider and classifies the
// response. It never returns an error for a rejected credential — that is a
// Result with StateInvalid. A returned error means the question could not be
// asked, and always accompanies StateUnknown.
func (v *Validator) Validate(ctx context.Context, cred Credential) Result {
	result := Result{
		Provider:    cred.Provider,
		Source:      cred.Source,
		Fingerprint: cred.Fingerprint(),
		CheckedAt:   v.now(),
	}
	provider := cred.Provider
	if provider == "" {
		provider = ProviderFirstParty
	}
	spec, err := v.requestFor(ctx, provider, cred)
	if err != nil {
		result.State = StateUnknown
		result.Err = err
		result.Detail = err.Error()
		return result
	}
	return v.probe(ctx, result, spec)
}

// requestSpec is one prepared, already-authenticated HTTP request plus the
// rule for reading Claude models out of its response body.
type requestSpec struct {
	request     *http.Request
	parseModels func([]byte) ([]Model, error)
	// requireModels means a 200 with no Claude models is not a pass. It
	// applies to the cloud providers, where authenticating successfully says
	// nothing about whether the account may call Claude.
	requireModels bool
	label         string
}

// probe issues the request and classifies the response.
//
// The classification is the heart of the package, and two of its arms are
// deliberately counter-intuitive. 429 is a pass: a rate-limited request was
// authenticated before it was throttled, so the credential is good. And any
// status that is neither an explicit rejection nor an explicit success is
// Unknown rather than a failure — a 404 from a gateway that does not implement
// model listing says nothing at all about the credential.
func (v *Validator) probe(ctx context.Context, result Result, spec requestSpec) Result {
	response, err := v.client.Do(spec.request)
	if err != nil {
		result.State = StateUnknown
		result.Err = err
		result.Detail = fmt.Sprintf("could not reach %s: %v", spec.label, err)
		return result
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if readErr != nil {
		result.State = StateUnknown
		result.Err = readErr
		result.Detail = fmt.Sprintf("could not read %s response: %v", spec.label, readErr)
		return result
	}

	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		result.State = StateInvalid
		result.Err = ErrInvalidCredential
		result.Detail = rejectionDetail(spec.label, body)
		return result
	case http.StatusOK, http.StatusTooManyRequests:
		// Fall through to the entitlement check below.
	default:
		result.State = StateUnknown
		result.Detail = fmt.Sprintf("%s returned HTTP %d", spec.label, response.StatusCode)
		return result
	}

	if response.StatusCode == http.StatusTooManyRequests {
		// Throttled before a body was produced, but the request authenticated.
		result.State = StateValid
		result.Detail = fmt.Sprintf("%s accepted the credential (rate limited)", spec.label)
		return result
	}

	if spec.parseModels != nil {
		models, parseErr := spec.parseModels(body)
		if parseErr != nil {
			if spec.requireModels {
				// The catalog IS the verdict here, so failing to read it means
				// entitlement cannot be confirmed.
				result.State = StateUnknown
				result.Err = parseErr
				result.Detail = fmt.Sprintf("could not read the %s model list: %v", spec.label, parseErr)
				return result
			}
			// Elsewhere the catalog is a bonus. The provider accepted the
			// credential, which is the question that was asked; a body we
			// cannot parse — an empty one, or a gateway's own envelope — does
			// not retract that.
			models = nil
		}
		result.Models = models
		if spec.requireModels && len(models) == 0 {
			// Authenticated, but not entitled to Claude. Greenlighting this
			// account means it fails on its first turn instead of here.
			result.State = StateUnknown
			result.Detail = fmt.Sprintf(
				"%s accepted the credential but reported no Claude models; the account may lack Claude access", spec.label)
			return result
		}
	}
	result.State = StateValid
	result.Detail = fmt.Sprintf("%s accepted the credential", spec.label)
	return result
}

// rejectionDetail extracts the provider's own explanation for a rejection.
//
// It is worth the effort because Anthropic returns a distinct message per
// credential class — "API key is invalid.", "OAuth access token is invalid.",
// "Invalid bearer token" — which is what makes an actionable error possible
// instead of a generic "auth failed".
func rejectionDetail(label string, body []byte) string {
	if message := providerErrorMessage(body); message != "" {
		return fmt.Sprintf("%s rejected the credential: %s", label, message)
	}
	return fmt.Sprintf("%s rejected the credential", label)
}

// IsResolverBug reports whether a rejection was caused by AO sending a
// malformed request rather than by a bad credential.
//
// This is the guard on the second invariant. "x-api-key header is required"
// means no credential was sent at all, and "Invalid bearer token" against an
// empty header means the same thing — both are our bug. Reporting either as
// "your credential was rejected" tells a user with perfectly good credentials
// to go re-authenticate, which is the one failure mode worse than the one this
// package exists to fix.
func IsResolverBug(detail string) bool {
	lowered := strings.ToLower(detail)
	for _, phrase := range []string{
		"header is required",
		"missing api key",
		"no credentials",
		"could not be parsed",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

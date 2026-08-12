package ports

import "context"

// FreshNativeSessionIDMode describes who chooses a new provider conversation's
// native identity.
type FreshNativeSessionIDMode string

const (
	// FreshNativeSessionIDProviderAssigned means the provider chooses the id and
	// AO learns it from the provider's lifecycle hooks.
	FreshNativeSessionIDProviderAssigned FreshNativeSessionIDMode = "provider_assigned"
	// FreshNativeSessionIDCallerAssigned means the adapter accepts
	// LaunchConfig.NativeSessionID for a fresh conversation.
	FreshNativeSessionIDCallerAssigned FreshNativeSessionIDMode = "caller_assigned"
)

// ContinuationCapabilities is the static fresh-conversation behavior AO needs
// before it activates an agent as part of a session switch. Runtime evidence
// such as whether one particular native session still exists is reported by
// AgentNativeSessionProber instead.
type ContinuationCapabilities struct {
	FreshNativeSessionID FreshNativeSessionIDMode
}

// AgentContinuationCapabilityProvider is implemented by adapters whose
// fresh native-session behavior has been verified. Its absence is
// intentionally treated as unsupported rather than assuming a provider
// accepts caller-selected native ids.
type AgentContinuationCapabilityProvider interface {
	ContinuationCapabilities() ContinuationCapabilities
}

// AgentFreshNativeSessionIDProvider keeps provider-specific id formats out of
// switch orchestration. AO persists the returned id and passes it back through
// LaunchConfig.NativeSessionID.
type AgentFreshNativeSessionIDProvider interface {
	NewNativeSessionID() string
}

// NativeSessionRef identifies provider-owned local session state without
// conflating it with the stable AO session id. ConfigDir is the exact native
// configuration/state root used by that invocation (for example CODEX_HOME).
type NativeSessionRef struct {
	NativeSessionID string
	ConfigDir       string
}

// AgentNativeSessionConfigProvider resolves the provider state root used for
// a launch. AO stores the returned path with the native-session binding so a
// later probe does not accidentally inspect another account/config home.
type AgentNativeSessionConfigProvider interface {
	NativeSessionConfigDir(ctx context.Context, env map[string]string) (string, error)
}

// NativeSessionAvailability is a tri-state result. Unknown is materially
// different from unavailable: some providers can resume remote conversations
// even when AO cannot find authoritative local state.
type NativeSessionAvailability string

const (
	// NativeSessionAvailabilityAvailable means authoritative backing state exists.
	NativeSessionAvailabilityAvailable NativeSessionAvailability = "available"
	// NativeSessionAvailabilityUnavailable means authoritative state is absent.
	NativeSessionAvailabilityUnavailable NativeSessionAvailability = "unavailable"
	// NativeSessionAvailabilityUnknown means the adapter cannot prove either case.
	NativeSessionAvailabilityUnknown NativeSessionAvailability = "unknown"
)

// AgentNativeSessionProber is an optional adapter capability for checking
// whether a stored provider-native resume handle still has usable backing
// state. A probe must return Unknown when its evidence is not authoritative.
type AgentNativeSessionProber interface {
	ProbeNativeSession(ctx context.Context, ref NativeSessionRef) (NativeSessionAvailability, error)
}

// AgentTranscriptLocator is an optional adapter capability for finding the
// provider-owned transcript associated with a native session. ok=false means
// no readable transcript was found; it does not by itself prove the native
// conversation cannot be resumed.
type AgentTranscriptLocator interface {
	LocateTranscript(ctx context.Context, ref NativeSessionRef) (path string, ok bool, err error)
}

// AgentTranscriptReader is an optional adapter capability for reading the
// provider's native transcript for a session and returning normalized
// user/assistant turns. It is intentionally separate from Agent: most
// harnesses do not record a parseable transcript, and keeping the capability
// optional lets the service degrade to "no transcript" instead of forcing
// every adapter to implement a stub. ok=false means no transcript is
// available for this session.
type AgentTranscriptReader interface {
	Transcript(ctx context.Context, session SessionRef) (messages []TranscriptMessage, ok bool, err error)
}

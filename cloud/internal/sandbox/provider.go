package sandbox

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// ErrNotFound indicates that a provider environment does not exist. It is the
// only error the reconciler accepts as proof that an environment is gone; every
// other failure leaves observed reality untouched.
var ErrNotFound = errors.New("sandbox environment not found")

// ErrAtCapacity indicates that a provider has no capacity for a new sandbox.
var ErrAtCapacity = errors.New("sandbox provider at capacity")

// ID uniquely identifies a provider sandbox.
type ID string

// Spec describes a sandbox to create.
type Spec struct {
	Name              string
	SessionID         string
	OrgID             string
	ResourceProfile   domain.ResourceProfile
	Shape             string
	RootFS            string
	Ingress           string
	Environment       map[string]string
	Labels            map[string]string
	AutoDeleteMinutes int
	// AutoPauseSeconds is disabled when zero.
	AutoPauseSeconds int
}

// Environment is the provider-neutral view of a sandbox. State is always one of
// the AO vocabulary values the reconciler switches on, never a provider string.
type Environment struct {
	ID       ID
	Name     string
	State    string
	Target   string
	Resource domain.ResourceProfile
}

// WorkerBootstrap contains the worker executable and launch environment.
type WorkerBootstrap struct {
	Binary            []byte
	Destination       string
	HelperBinary      []byte
	HelperDestination string
	User              string
	Environment       map[string]string
}

// Bootstrapper installs and starts an AO worker in an existing sandbox.
// Providers implement it when they expose an authenticated exec/file API,
// which lets the reconciler repair a live sandbox instead of replacing it.
type Bootstrapper interface {
	BootstrapWorker(context.Context, ID, WorkerBootstrap) error
}

// Recreator re-establishes compute with a fresh worker launch.
type Recreator interface {
	Recreate(context.Context, ID, Spec) (Environment, error)
}

// Provider manages the lifecycle of cloud sandbox environments.
type Provider interface {
	Create(context.Context, Spec) (Environment, error)
	Get(context.Context, ID) (Environment, error)
	FindBySession(context.Context, string) (Environment, bool, error)
	Start(context.Context, ID) error
	Stop(context.Context, ID) error
	Pause(context.Context, ID) error
	Resume(context.Context, ID) error
	Delete(context.Context, ID) error
}

// Provider-neutral environment states. Every provider maps its own vocabulary
// onto these; anything unrecognized must become StateProvisioning, never
// StateRunning, because reporting a sandbox as running before its worker has
// checked in suppresses the startup deadline.
const (
	StateProvisioning = "provisioning"
	StateRunning      = "running"
	StateStopped      = "stopped"
	StatePaused       = "paused"
	StateDeleting     = "deleting"
	StateDeleted      = "deleted"
)

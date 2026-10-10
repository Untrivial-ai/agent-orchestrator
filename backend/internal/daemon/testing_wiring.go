package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/testingdesktop/cua"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/testingevidence"
	localtarget "github.com/aoagents/agent-orchestrator/backend/internal/adapters/testingtarget/local"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// testingProviders is the single composition point for slices A, D and E.
// The local recipe resolves its checkout at URL intake. Construction starts no
// target, desktop driver or worker. Cancellation and saved evidence use durable
// state independently of providers.
type testingProviders struct {
	PullRequests ports.TestingPullRequestIntake
	Target       ports.TestingTargetEnvironment
	Desktop      ports.TestingDesktopControl
	Workers      testingsvc.WorkerLauncher
	Recipes      map[string]testingsvc.Recipe
	Close        func(context.Context) error
	Log          *slog.Logger
}

// Provider-specific recording and policy types are translated here; the
// service only sees the provider-neutral desktop port and its extensions.
type testingDesktopAdapter interface {
	ports.TestingDesktopControl
	DeliveryMode() cua.DeliveryMode
	StartRecording(context.Context, domain.TestTargetIdentity, string) (cua.RecordingResult, error)
	StopRecording(context.Context, domain.TestTargetIdentity) (cua.RecordingResult, error)
	Release(context.Context, domain.TestTargetIdentity) error
	Close(context.Context) error
}

type testingDesktopBridge struct {
	resolve func() (testingDesktopAdapter, error)
	close   func(context.Context) error
	mode    cua.DeliveryMode
}

// Native provider validation belongs to desktop use, not ordinary daemon startup.
// Close fences future use without constructing a provider that was never used.
func newTestingDesktopBridge(cfg cua.Config, factory func(cua.Config) (testingDesktopAdapter, error)) testingDesktopBridge {
	var once sync.Once
	var desktop testingDesktopAdapter
	var constructionErr error
	return testingDesktopBridge{
		mode: cfg.DeliveryMode,
		resolve: func() (testingDesktopAdapter, error) {
			once.Do(func() {
				desktop, constructionErr = factory(cfg)
				if constructionErr != nil {
					constructionErr = fmt.Errorf("configure testing desktop: %w", constructionErr)
				}
			})
			return desktop, constructionErr
		},
		close: func(ctx context.Context) error {
			once.Do(func() { constructionErr = errors.New("testing desktop closed before first use") })
			if desktop == nil {
				return nil
			}
			return desktop.Close(ctx)
		},
	}
}

func (d testingDesktopBridge) BindWindow(ctx context.Context, target domain.TestTargetIdentity) (domain.TestTargetIdentity, error) {
	if err := ctx.Err(); err != nil {
		return domain.TestTargetIdentity{}, err
	}
	desktop, err := d.resolve()
	if err != nil {
		return domain.TestTargetIdentity{}, err
	}
	return desktop.BindWindow(ctx, target)
}

func (d testingDesktopBridge) Screenshot(ctx context.Context, target domain.TestTargetIdentity) (domain.TestScreenshot, error) {
	desktop, err := d.resolve()
	if err != nil {
		return domain.TestScreenshot{}, err
	}
	return desktop.Screenshot(ctx, target)
}

func (d testingDesktopBridge) Release(ctx context.Context, target domain.TestTargetIdentity) error {
	desktop, err := d.resolve()
	if err != nil {
		return err
	}
	return desktop.Release(ctx, target)
}

func (d testingDesktopBridge) Close(ctx context.Context) error { return d.close(ctx) }

func testingInputError(err error) error {
	if errors.Is(err, cua.ErrRefused) {
		return errors.Join(ports.ErrTestingInputRefused, err)
	}
	return err
}
func (d testingDesktopBridge) Click(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestClickRequest) (domain.TestActionResult, error) {
	desktop, err := d.resolve()
	if err != nil {
		return domain.TestActionResult{}, err
	}
	result, err := desktop.Click(ctx, target, frame, request)
	return result, testingInputError(err)
}
func (d testingDesktopBridge) Type(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestTypeRequest) (domain.TestActionResult, error) {
	desktop, err := d.resolve()
	if err != nil {
		return domain.TestActionResult{}, err
	}
	result, err := desktop.Type(ctx, target, frame, request)
	return result, testingInputError(err)
}
func (d testingDesktopBridge) Key(ctx context.Context, target domain.TestTargetIdentity, frame domain.TestDesktopFrame, request domain.TestKeyRequest) (domain.TestActionResult, error) {
	desktop, err := d.resolve()
	if err != nil {
		return domain.TestActionResult{}, err
	}
	result, err := desktop.Key(ctx, target, frame, request)
	return result, testingInputError(err)
}

func (d testingDesktopBridge) DeliveryMode() string {
	return string(d.mode)
}
func (d testingDesktopBridge) InputDeliveryMode(string) string {
	return d.DeliveryMode()
}
func (d testingDesktopBridge) StartRecording(ctx context.Context, target domain.TestTargetIdentity, dir string) (ports.TestingRecordingResult, error) {
	desktop, err := d.resolve()
	if err != nil {
		return ports.TestingRecordingResult{}, err
	}
	result, err := desktop.StartRecording(ctx, target, dir)
	return recordingResult(result), err
}
func (d testingDesktopBridge) StopRecording(ctx context.Context, target domain.TestTargetIdentity) (ports.TestingRecordingResult, error) {
	desktop, err := d.resolve()
	if err != nil {
		return ports.TestingRecordingResult{}, err
	}
	result, err := desktop.StopRecording(ctx, target)
	return recordingResult(result), err
}
func recordingResult(result cua.RecordingResult) ports.TestingRecordingResult {
	return ports.TestingRecordingResult{Path: result.Path, MIMEType: result.MIMEType, Width: result.Width, Height: result.Height,
		Duration: result.Duration, StartedAt: result.StartedAt, StoppedAt: result.StoppedAt, RecorderPID: result.RecorderPID,
		Gap: result.Gap, StagingPath: result.StagingPath, StagingCleanup: result.StagingCleanup}
}

func configuredTestingProviders(cfg config.Config) (testingProviders, error) {
	return testingProvidersFromEnv(cfg, os.Getenv, localtarget.New(), func(cfg cua.Config) (testingDesktopAdapter, error) { return cua.New(cfg) })
}

func testingProvidersFromEnv(cfg config.Config, getenv func(string) string, target ports.TestingTargetEnvironment, makeDesktop func(cua.Config) (testingDesktopAdapter, error)) (testingProviders, error) {
	mode := cua.DeliveryMode(getenv("AO_TESTING_DESKTOP_DELIVERY"))
	if mode == "" {
		mode = cua.Foreground
	}
	if mode != cua.Foreground {
		return testingProviders{}, fmt.Errorf("AO_TESTING_DESKTOP_DELIVERY must be foreground; background input is unsupported")
	}
	checkout := getenv("AO_TESTING_TARGET_CHECKOUT")
	desktop := newTestingDesktopBridge(cua.Config{DataDir: cfg.DataDir, DeliveryMode: mode}, makeDesktop)
	providers := testingProviders{PullRequests: localtarget.NewPullRequestIntake(), Target: target, Desktop: desktop, Close: desktop.Close,
		Recipes: map[string]testingsvc.Recipe{"local-ao": {ID: "local-ao", CheckoutPath: checkout, Snapshot: "isolated local AO checkout", DeliveryMode: string(mode), VisualMarker: getenv("AO_TESTING_REAL_PROVIDERS") != "1", RealProviders: getenv("AO_TESTING_REAL_PROVIDERS") == "1"}}}
	return providers, nil
}

func newTestingService(cfg config.Config, store testingsvc.Store, providers testingProviders) *testingsvc.Service {
	home, _ := os.UserHomeDir()
	return testingsvc.New(testingsvc.Deps{PullRequests: providers.PullRequests, Store: store, Target: providers.Target, Desktop: providers.Desktop, Workers: providers.Workers, Recipes: providers.Recipes, Evidence: testingevidence.New(cfg.DataDir, store), EvidenceRoot: filepath.Join(cfg.DataDir, "testing"), TargetStateRoot: filepath.Join(home, ".ao", "dev", "agentic-target"), Log: providers.Log, CloseDesktop: providers.Close})
}

// wireTestingService binds both sides before startup recovery can restore workers.
func wireTestingService(cfg config.Config, store testingsvc.Store, manager *sessionmanager.Manager, providers testingProviders) *testingsvc.Service {
	providers.Workers = manager
	svc := newTestingService(cfg, store, providers)
	manager.SetTestingProfileResolver(svc)
	return svc
}

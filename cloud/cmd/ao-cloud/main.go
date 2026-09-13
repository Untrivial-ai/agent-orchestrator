package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/auth"
	"github.com/aoagents/agent-orchestrator/cloud/internal/config"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/httpapi"
	"github.com/aoagents/agent-orchestrator/cloud/internal/idlepause"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/prstatus"
	"github.com/aoagents/agent-orchestrator/cloud/internal/reconcile"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	coderprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/createos"
	dockerprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/docker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandboxresolve"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// readSSHPubKeys loads the operator SSH keys authorized on every sandbox. They
// are a debugging affordance, not part of the worker's trust path.
func readSSHPubKeys(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sandbox SSH public keys %s: %w", path, err)
	}
	var keys []string
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			keys = append(keys, trimmed)
		}
	}
	return keys, nil
}

// provisioningDefaults is the plan every new session in this deployment is
// stamped with. It is resolved once at startup so a request never reads
// configuration, and so a misconfigured deployment fails at boot rather than on
// a user's first session.
func provisioningDefaults(cfg config.Config) sandbox.ProvisioningDefaults {
	return sandbox.ProvisioningDefaults{
		Provider: cfg.SandboxProvider,
		Release:  cfg.Release,
		NodeOps: sandbox.NodeOpsConfig{
			BaseURL:          cfg.NodeOpsBaseURL,
			APIKey:           cfg.NodeOpsAPIKey,
			DefaultShape:     cfg.NodeOpsDefaultShape,
			DefaultRootFS:    cfg.NodeOpsDefaultRootFS,
			RootFSByHarness:  cfg.NodeOpsRootFSByHarness,
			Ingress:          cfg.NodeOpsIngress,
			SSHKeyPath:       cfg.NodeOpsSSHKeyPath,
			WorkerTokenTTL:   cfg.NodeOpsWorkerTokenTTL,
			AutoPauseSeconds: cfg.NodeOpsAutoPauseSeconds,
		},
		Docker: sandbox.DockerConfig{
			Host:           cfg.DockerHost,
			WorkerImage:    cfg.DockerWorkerImage,
			Network:        cfg.DockerNetwork,
			Namespace:      cfg.DockerNamespace,
			WorkerTokenTTL: cfg.DockerWorkerTokenTTL,
		},
		Coder: sandbox.CoderConfig{
			BaseURL:        cfg.CoderURL,
			Owner:          cfg.CoderOwner,
			TemplateID:     cfg.CoderTemplateID,
			AgentName:      cfg.CoderAgentName,
			Parameters:     cfg.CoderParameters,
			DurableRoot:    cfg.CoderDurableRoot,
			WorkerTokenTTL: cfg.CoderWorkerTokenTTL,
		},
	}
}

// newSandboxReconciler builds the reconciler for a deployment that provisions
// sandboxes. It returns nil when this deployment does not: a control plane with
// no sandbox provider still serves the API, and the worker routes report 404
// rather than failing open.
func newSandboxReconciler(
	cfg config.Config,
	store *postgres.Store,
	logger *slog.Logger,
) (*reconcile.Reconciler, error) {
	// Build every provider this control plane offers, not just the default, so a
	// single CP can serve more than one provider and a client can pick per
	// session. AvailableSandboxProviders always contains the default, and is
	// exactly that default for a single-provider deployment.
	var (
		nodeOpsProvider    sandbox.Provider
		dockerProvider     sandbox.Provider
		coderProvider      sandbox.Provider
		workerBinary       []byte
		workerHelperBinary []byte
		buildsProvider     bool
		needsWorkerBinary  bool
	)
	for _, provider := range cfg.AvailableSandboxProviders {
		switch provider {
		case sandbox.ProviderNodeOps, sandbox.ProviderDocker, sandbox.ProviderCoder:
			buildsProvider = true
		}
		if provider == sandbox.ProviderNodeOps || provider == sandbox.ProviderCoder {
			needsWorkerBinary = true
		}
	}
	if !buildsProvider {
		return nil, nil
	}
	if needsWorkerBinary {
		// The worker binary is read once, at startup. Reading it per provision
		// would let a mid-flight deploy hand two sandboxes different builds.
		var err error
		workerBinary, err = os.ReadFile(cfg.WorkerBinaryPath)
		if err != nil {
			return nil, fmt.Errorf("read worker binary %s: %w", cfg.WorkerBinaryPath, err)
		}
		if len(workerBinary) == 0 {
			return nil, fmt.Errorf("worker binary %s is empty", cfg.WorkerBinaryPath)
		}
		workerHelperBinary, err = os.ReadFile(cfg.WorkerHelperBinaryPath)
		if err != nil {
			return nil, fmt.Errorf("read worker helper binary %s: %w", cfg.WorkerHelperBinaryPath, err)
		}
		if len(workerHelperBinary) == 0 {
			return nil, fmt.Errorf("worker helper binary %s is empty", cfg.WorkerHelperBinaryPath)
		}
	}
	for _, provider := range cfg.AvailableSandboxProviders {
		switch provider {
		case sandbox.ProviderNodeOps:
			sshPubKeys, err := readSSHPubKeys(cfg.NodeOpsSSHKeyPath)
			if err != nil {
				return nil, err
			}
			nodeOpsProvider = createos.New(createos.Config{
				BaseURL:      cfg.NodeOpsBaseURL,
				APIKey:       cfg.NodeOpsAPIKey,
				DefaultShape: cfg.NodeOpsDefaultShape,
				DefaultRoot:  cfg.NodeOpsDefaultRootFS,
				Region:       cfg.NodeOpsRegion,
				SSHPubKeys:   sshPubKeys,
			})
		case sandbox.ProviderDocker:
			provider, err := dockerprovider.New(dockerprovider.Config{
				Host:        cfg.DockerHost,
				WorkerImage: cfg.DockerWorkerImage,
				Network:     cfg.DockerNetwork,
				Namespace:   cfg.DockerNamespace,
			})
			if err != nil {
				return nil, err
			}
			dockerProvider = provider
		case sandbox.ProviderCoder:
			provider, err := coderprovider.New(coderprovider.Config{
				BaseURL:    cfg.CoderURL,
				Token:      cfg.CoderAPIToken,
				Owner:      cfg.CoderOwner,
				TemplateID: cfg.CoderTemplateID,
				AgentName:  cfg.CoderAgentName,
				Parameters: cfg.CoderParameters,
			})
			if err != nil {
				return nil, err
			}
			coderProvider = provider
		}
	}
	return reconcile.New(store, sandboxresolve.New(nodeOpsProvider, dockerProvider, coderProvider), reconcile.Options{
		PublicURL:              cfg.PublicURL,
		WorkerBinary:           workerBinary,
		WorkerHelperBinary:     workerHelperBinary,
		Interval:               cfg.ReconcileInterval,
		StartupTimeout:         cfg.SandboxStartupTimeout,
		HeartbeatTimeout:       cfg.WorkerHeartbeatTimeout,
		AllowAnonymousCheckout: cfg.AllowAnonymousCheckout,
		Logger:                 logger,
	}), nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("ao-cloud stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	if cfg.MigrateOnStartup {
		err := func() error {
			migrationContext, cancelMigration := context.WithTimeout(
				ctx,
				cfg.MigrationTimeout,
			)
			defer cancelMigration()
			return postgres.Migrate(migrationContext, cfg.MigrationDatabaseURL)
		}()
		if err != nil {
			return err
		}
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.Hosted() {
		if err := store.ValidateRuntimeRole(ctx); err != nil {
			return err
		}
	}

	var workosVerifier auth.WorkOSVerifier
	if cfg.WorkOSIssuer != "" {
		profiles, err := auth.NewWorkOSProfileResolver(cfg.WorkOSAPIKey, nil)
		if err != nil {
			return err
		}
		organizations, err := auth.NewWorkOSOrganizationResolver(cfg.WorkOSAPIKey, nil)
		if err != nil {
			return err
		}
		workosVerifier, err = auth.NewOIDCVerifier(
			ctx,
			cfg.WorkOSIssuer,
			cfg.WorkOSClientID,
			cfg.WorkOSJWKSURL,
			profiles,
			organizations,
		)
		if err != nil {
			return err
		}
	}
	var providerCipher *secrets.Cipher
	if len(cfg.ProviderSecretKey) > 0 {
		providerCipher, err = secrets.New(cfg.ProviderSecretKey)
		if err != nil {
			return err
		}
	}

	var githubService *githubapp.Service
	if cfg.GitHub.Enabled() {
		githubClient, err := githubapp.New(githubapp.Config{
			AppID:         cfg.GitHub.AppID,
			AppSlug:       cfg.GitHub.AppSlug,
			ClientID:      cfg.GitHub.ClientID,
			ClientSecret:  cfg.GitHub.ClientSecret,
			PrivateKeyPEM: cfg.GitHub.PrivateKeyPEM,
			PublicURL:     cfg.GitHub.PublicURL,
		}, nil)
		if err != nil {
			return err
		}
		githubService, err = githubapp.NewService(
			store,
			githubClient,
			cfg.GitHub.StateKey,
			cfg.ProviderSecretKey,
			cfg.GitHub.WebhookSecret,
			cfg.GitHub.InstallTTL,
			logger,
		)
		if err != nil {
			return err
		}
		go githubService.Run(ctx)
	}
	var checkoutBroker httpapi.CheckoutBroker
	if githubService != nil {
		checkoutBroker = githubService
	} else if cfg.RepositoryBrokerURL != "" {
		checkoutBroker, err = githubapp.NewRemoteCheckoutBroker(
			store,
			providerCipher,
			cfg.RepositoryBrokerURL,
			cfg.Environment,
			cfg.RepositoryBrokerToken,
			nil,
		)
		if err != nil {
			return err
		}
	}
	reconciler, err := newSandboxReconciler(cfg, store, logger)
	if err != nil {
		return err
	}
	// The scanner only has anything to do where sandboxes exist to pause.
	var idlePauseScanner *idlepause.Scanner
	if reconciler != nil {
		idlePauseScanner = idlepause.New(store, idlepause.Options{
			Interval:      cfg.IdlePauseInterval,
			IdleThreshold: cfg.IdlePauseThreshold,
			Logger:        logger,
		})
	}
	// The scanner only has anything to refresh where GitHub is configured to
	// resolve an installation for.
	var prStatusScanner *prstatus.Scanner
	if githubService != nil {
		prStatusScanner = prstatus.New(store, githubService, prstatus.Options{
			Interval: cfg.PRStatusPollInterval,
			Logger:   logger,
		})
	}
	// Worker tokens are only issued where sandboxes are provisioned. Leaving
	// this nil elsewhere is what makes the worker routes 404 instead of
	// accepting credentials no sandbox could have been given.
	var workerTokens httpapi.WorkerTokens
	if reconciler != nil {
		workerTokens = worker.NewTokenManager([]byte(cfg.WorkerSigningKey))
	}

	apiOptions := httpapi.Options{
		Store:                     store,
		WorkOS:                    workosVerifier,
		LocalAuthEnabled:          cfg.LocalAuthEnabled,
		LocalSessionTTL:           cfg.LocalSessionTTL,
		SandboxProvider:           cfg.SandboxProvider,
		AvailableSandboxProviders: cfg.AvailableSandboxProviders,
		Provisioning:              provisioningDefaults(cfg),
		WorkerTokens:              workerTokens,
		WorkerTokenTTL:            cfg.WorkerTokenTTL(),
		MaxSandboxes:              cfg.MaxSandboxesPerOrg,
		Environment:               cfg.Environment,
		Release:                   cfg.Release,
		Logger:                    logger,
		GitHub:                    githubService,
		CheckoutBroker:            checkoutBroker,
		BrokerAuthToken:           cfg.RepositoryBrokerToken,
		EnvironmentControlToken:   cfg.EnvironmentControlToken,
		SecretCipher:              providerCipher,
		WebhookMaxBody:            cfg.GitHub.WebhookMaxBody,
	}
	if cfg.Environment == "development" &&
		os.Getenv("AO_CLOUD_DEVELOPMENT_SKIP_CREDENTIAL_VALIDATION") == "true" {
		logger.Warn("coding-agent credential validation is disabled for development")
		apiOptions.CredentialValidator = developmentCredentialValidator{}
	}
	api := httpapi.New(apiOptions)
	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		logger.Info("ao-cloud listening", "config", cfg.String())
		result <- server.ListenAndServe()
	}()

	if reconciler != nil {
		go func() {
			logger.Info("sandbox reconciler started",
				"provider", cfg.SandboxProvider,
				"interval", cfg.ReconcileInterval,
				"startup_timeout", cfg.SandboxStartupTimeout,
				"heartbeat_timeout", cfg.WorkerHeartbeatTimeout,
			)
			if err := reconciler.Run(ctx); err != nil {
				logger.Error("sandbox reconciler stopped", "error", err)
			}
		}()
	}

	if idlePauseScanner != nil {
		go func() {
			logger.Info("idle-pause scanner started",
				"interval", cfg.IdlePauseInterval,
				"idle_threshold", cfg.IdlePauseThreshold,
			)
			if err := idlePauseScanner.Run(ctx); err != nil {
				logger.Error("idle-pause scanner stopped", "error", err)
			}
		}()
	}

	if prStatusScanner != nil {
		go func() {
			logger.Info("pull request status scanner started", "interval", cfg.PRStatusPollInterval)
			if err := prStatusScanner.Run(ctx); err != nil {
				logger.Error("pull request status scanner stopped", "error", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		api.SetDraining(true)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type developmentCredentialValidator struct{}

func (developmentCredentialValidator) Validate(
	context.Context,
	string,
	string,
	[]byte,
) error {
	return nil
}

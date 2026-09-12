package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

var releasePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)
var githubSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)

const workOSAPIBaseURL = "https://api.workos.com"

type Config struct {
	Environment             string
	HTTPAddress             string
	DatabaseURL             string
	MigrationDatabaseURL    string
	MigrateOnStartup        bool
	MigrationTimeout        time.Duration
	WorkOSIssuer            string
	WorkOSClientID          string
	WorkOSAPIKey            string
	WorkOSJWKSURL           string
	LocalAuthEnabled        bool
	LocalSessionTTL         time.Duration
	SandboxProvider         string
	AllowAnonymousCheckout  bool
	ProviderSecretKey       []byte
	Release                 string
	RepositoryBrokerURL     string
	RepositoryBrokerToken   string
	EnvironmentControlToken string

	// PublicURL is the origin a sandbox worker dials back to. A worker opens
	// no inbound port, so this is the only way it can reach the control plane.
	PublicURL string
	// WorkerSigningKey signs the short-lived worker tokens.
	WorkerSigningKey string
	// WorkerBinaryPath is the ao-worker executable uploaded into sandboxes.
	WorkerBinaryPath string
	// WorkerHelperBinaryPath is the AO CLI uploaded for harness activity hooks.
	WorkerHelperBinaryPath string
	// MaxSandboxesPerOrg caps how much provider capacity one organization can
	// hold at once.
	MaxSandboxesPerOrg int
	// ReconcileInterval is the sandbox reconcile tick.
	ReconcileInterval time.Duration
	// SandboxStartupTimeout is the budget from Create to the first heartbeat.
	SandboxStartupTimeout time.Duration
	// WorkerHeartbeatTimeout is how long a silent worker is tolerated.
	WorkerHeartbeatTimeout time.Duration
	// IdlePauseInterval is how often the idle-pause scanner runs.
	IdlePauseInterval time.Duration
	// IdlePauseThreshold is how long a session must be quiet, with no turn in
	// flight, before the control plane pauses its sandbox.
	IdlePauseThreshold time.Duration
	// PRStatusPollInterval is how often the pull-request status scanner
	// refreshes CI, review, and mergeability state from GitHub.
	PRStatusPollInterval time.Duration
	// TerminalStreamEnabled turns on the low-latency terminal path: workers
	// hold a persistent stream to the control plane and Postgres NOTIFY
	// replaces the input/output polling loops. Off means the polled
	// store-and-forward behavior, byte for byte.
	TerminalStreamEnabled bool

	NodeOpsBaseURL       string
	NodeOpsAPIKey        string
	NodeOpsDefaultShape  string
	NodeOpsDefaultRootFS string
	// NodeOpsRootFSByHarness maps a harness to a slimmer per-harness template
	// (AO_CLOUD_NODEOPS_ROOTFS_BY_HARNESS, JSON object). Optional; unmapped
	// harnesses use NodeOpsDefaultRootFS.
	NodeOpsRootFSByHarness  map[string]string
	NodeOpsIngress          string
	NodeOpsSSHKeyPath       string
	NodeOpsRegion           string
	NodeOpsWorkerTokenTTL   time.Duration
	NodeOpsAutoPauseSeconds int

	DockerHost           string
	DockerWorkerImage    string
	DockerNetwork        string
	DockerNamespace      string
	DockerWorkerTokenTTL time.Duration

	GitHub GitHubConfig
}

type GitHubConfig struct {
	AppID          int64
	AppSlug        string
	ClientID       string
	ClientSecret   string
	PrivateKeyPEM  string
	WebhookSecret  string
	StateKey       []byte
	PublicURL      string
	InstallTTL     time.Duration
	WebhookMaxBody int64
}

func (c GitHubConfig) Enabled() bool {
	return c.AppID != 0
}

// minWorkerSigningKeyLength is the shortest signing key accepted for worker
// tokens. Short keys make the HMAC forgeable, and a forged worker token is a
// grant to write onto someone else's session stream.
const minWorkerSigningKeyLength = 32

// Provider-side auto-pause is opt-in; the control plane tracks real activity.
const defaultNodeOpsAutoPauseSeconds = 0

// Give background sessions a full hour before their NodeOps VM is paused. A
// visible workspace terminal still holds its shorter interactive lease, but
// this default avoids turning an ordinary review break into a cold resume.
const defaultIdlePauseThreshold = time.Hour

const defaultIdlePauseInterval = 30 * time.Second

const defaultPRStatusPollInterval = 30 * time.Second

func Load() (Config, error) {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("AO_CLOUD_ENV")))
	hosted := environment == "staging" || environment == "production"
	defaultHTTPAddress := ":8080"
	if environment == "development" || environment == "test" {
		defaultHTTPAddress = "127.0.0.1:8080"
	}
	rootFSByHarnessEnv := map[string]string{}
	if raw := strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_ROOTFS_BY_HARNESS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rootFSByHarnessEnv); err != nil {
			return Config{}, fmt.Errorf("invalid AO_CLOUD_NODEOPS_ROOTFS_BY_HARNESS: %w", err)
		}
	}

	cfg := Config{
		Environment:            environment,
		HTTPAddress:            envOrDefault("AO_CLOUD_HTTP_ADDRESS", defaultHTTPAddress),
		DatabaseURL:            strings.TrimSpace(os.Getenv("AO_CLOUD_DATABASE_URL")),
		MigrationDatabaseURL:   strings.TrimSpace(os.Getenv("AO_CLOUD_MIGRATION_DATABASE_URL")),
		MigrateOnStartup:       boolEnv("AO_CLOUD_MIGRATE_ON_STARTUP", !hosted),
		MigrationTimeout:       durationEnv("AO_CLOUD_MIGRATION_TIMEOUT", 15*time.Minute),
		WorkOSIssuer:           strings.TrimSpace(os.Getenv("AO_CLOUD_WORKOS_ISSUER")),
		WorkOSClientID:         strings.TrimSpace(os.Getenv("AO_CLOUD_WORKOS_CLIENT_ID")),
		WorkOSAPIKey:           strings.TrimSpace(os.Getenv("AO_CLOUD_WORKOS_API_KEY")),
		WorkOSJWKSURL:          strings.TrimSpace(os.Getenv("AO_CLOUD_WORKOS_JWKS_URL")),
		LocalAuthEnabled:       boolEnv("AO_CLOUD_LOCAL_AUTH", false),
		LocalSessionTTL:        durationEnv("AO_CLOUD_LOCAL_SESSION_TTL", 24*time.Hour),
		AllowAnonymousCheckout: boolEnv("AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT", false),
		TerminalStreamEnabled:  boolEnv("AO_CLOUD_TERMINAL_STREAM", false),
		SandboxProvider: strings.ToLower(
			envOrDefault("AO_CLOUD_SANDBOX_PROVIDER", defaultSandboxProvider(hosted)),
		),
		Release: strings.TrimSpace(os.Getenv("AO_CLOUD_RELEASE")),
		RepositoryBrokerURL: strings.TrimRight(
			strings.TrimSpace(os.Getenv("AO_CLOUD_REPOSITORY_BROKER_URL")), "/",
		),
		RepositoryBrokerToken: strings.TrimSpace(
			os.Getenv("AO_CLOUD_REPOSITORY_BROKER_TOKEN"),
		),
		EnvironmentControlToken: strings.TrimSpace(
			os.Getenv("AO_CLOUD_ENV_CONTROL_TOKEN"),
		),

		PublicURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("AO_CLOUD_PUBLIC_URL")), "/"),
		WorkerSigningKey:       strings.TrimSpace(os.Getenv("AO_CLOUD_WORKER_SIGNING_KEY")),
		WorkerBinaryPath:       strings.TrimSpace(os.Getenv("AO_CLOUD_WORKER_BINARY_PATH")),
		WorkerHelperBinaryPath: strings.TrimSpace(os.Getenv("AO_CLOUD_WORKER_HELPER_BINARY_PATH")),
		MaxSandboxesPerOrg:     intEnvOrDefault("AO_CLOUD_MAX_ACTIVE_SANDBOXES_PER_ORG", 1000),
		ReconcileInterval:      durationEnv("AO_CLOUD_SANDBOX_RECONCILE_INTERVAL", 2*time.Second),
		SandboxStartupTimeout:  durationEnv("AO_CLOUD_SANDBOX_STARTUP_TIMEOUT", 3*time.Minute),
		WorkerHeartbeatTimeout: durationEnv("AO_CLOUD_WORKER_HEARTBEAT_TIMEOUT", time.Minute),
		IdlePauseInterval:      durationEnv("AO_CLOUD_IDLE_PAUSE_INTERVAL", defaultIdlePauseInterval),
		IdlePauseThreshold:     durationEnv("AO_CLOUD_IDLE_PAUSE_THRESHOLD", defaultIdlePauseThreshold),
		PRStatusPollInterval:   durationEnv("AO_CLOUD_PR_STATUS_POLL_INTERVAL", defaultPRStatusPollInterval),

		NodeOpsBaseURL:         strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_BASE_URL")),
		NodeOpsAPIKey:          strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_API_KEY")),
		NodeOpsDefaultShape:    strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_DEFAULT_SHAPE")),
		NodeOpsDefaultRootFS:   strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_DEFAULT_ROOTFS")),
		NodeOpsRootFSByHarness: rootFSByHarnessEnv,
		NodeOpsIngress:         strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_INGRESS")),
		NodeOpsSSHKeyPath:      strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_SSH_KEY_PATH")),
		NodeOpsRegion:          strings.TrimSpace(os.Getenv("AO_CLOUD_NODEOPS_REGION")),
		NodeOpsAutoPauseSeconds: intEnvOrDefault(
			"AO_CLOUD_NODEOPS_AUTO_PAUSE_SECONDS", defaultNodeOpsAutoPauseSeconds),
		NodeOpsWorkerTokenTTL: durationEnv(
			"AO_CLOUD_NODEOPS_WORKER_TOKEN_TTL", sandbox.DefaultWorkerTokenTTL,
		),

		DockerHost:        envOrDefault("AO_CLOUD_DOCKER_HOST", "unix:///var/run/docker.sock"),
		DockerWorkerImage: envOrDefault("AO_CLOUD_DOCKER_WORKER_IMAGE", "ao-cloud-worker:local"),
		DockerNetwork:     strings.TrimSpace(os.Getenv("AO_CLOUD_DOCKER_NETWORK")),
		DockerNamespace:   envOrDefault("AO_CLOUD_DOCKER_NAMESPACE", "ao-cloud-local"),
		DockerWorkerTokenTTL: durationEnv(
			"AO_CLOUD_DOCKER_WORKER_TOKEN_TTL", sandbox.DefaultWorkerTokenTTL,
		),

		GitHub: GitHubConfig{
			AppID:          int64Env("AO_CLOUD_GITHUB_APP_ID"),
			AppSlug:        strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_APP_SLUG")),
			ClientID:       strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_CLIENT_ID")),
			ClientSecret:   strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_CLIENT_SECRET")),
			PrivateKeyPEM:  strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_PRIVATE_KEY")),
			WebhookSecret:  strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_WEBHOOK_SECRET")),
			PublicURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("AO_CLOUD_PUBLIC_URL")), "/"),
			InstallTTL:     durationEnv("AO_CLOUD_GITHUB_INSTALL_TTL", 10*time.Minute),
			WebhookMaxBody: int64EnvOrDefault("AO_CLOUD_GITHUB_WEBHOOK_MAX_BYTES", 2<<20),
		},
	}
	stateKey := strings.TrimSpace(os.Getenv("AO_CLOUD_GITHUB_STATE_KEY"))
	if stateKey != "" {
		decoded, err := base64.StdEncoding.DecodeString(stateKey)
		if err != nil || len(decoded) != 32 {
			return Config{}, errors.New("AO_CLOUD_GITHUB_STATE_KEY must be base64-encoded 32 bytes")
		}
		cfg.GitHub.StateKey = decoded
	}
	providerSecretKey := strings.TrimSpace(
		os.Getenv("AO_CLOUD_PROVIDER_SECRET_KEY"),
	)
	if providerSecretKey != "" {
		decoded, err := base64.StdEncoding.DecodeString(providerSecretKey)
		if err != nil || len(decoded) != 32 {
			return Config{}, errors.New(
				"AO_CLOUD_PROVIDER_SECRET_KEY must be base64-encoded 32 bytes",
			)
		}
		cfg.ProviderSecretKey = decoded
	}
	if value := strings.TrimSpace(os.Getenv("AO_CLOUD_MIGRATION_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, errors.New("AO_CLOUD_MIGRATION_TIMEOUT must be a positive duration")
		}
		cfg.MigrationTimeout = timeout
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("AO_CLOUD_DATABASE_URL is required")
	}
	if cfg.MigrationDatabaseURL == "" {
		cfg.MigrationDatabaseURL = cfg.DatabaseURL
	}
	switch cfg.Environment {
	case "development", "test", "staging", "production":
	default:
		return Config{}, errors.New("AO_CLOUD_ENV must be development, test, staging, or production")
	}
	workosValues := []string{cfg.WorkOSIssuer, cfg.WorkOSClientID, cfg.WorkOSAPIKey}
	configuredWorkOSValues := 0
	for _, value := range workosValues {
		if value != "" {
			configuredWorkOSValues++
		}
	}
	if configuredWorkOSValues != 0 && configuredWorkOSValues != len(workosValues) {
		return Config{}, errors.New("AO_CLOUD_WORKOS_ISSUER, AO_CLOUD_WORKOS_CLIENT_ID, and AO_CLOUD_WORKOS_API_KEY must be set together")
	}
	if strings.TrimRight(cfg.WorkOSIssuer, "/") == workOSAPIBaseURL {
		cfg.WorkOSIssuer = workOSAPIBaseURL + "/user_management/" + cfg.WorkOSClientID
	}
	if cfg.WorkOSIssuer != "" {
		workOSIssuer := workOSAPIBaseURL + "/user_management/" + cfg.WorkOSClientID
		if strings.HasPrefix(cfg.WorkOSIssuer, workOSAPIBaseURL+"/user_management/") &&
			cfg.WorkOSIssuer != workOSIssuer {
			return Config{}, errors.New("AO_CLOUD_WORKOS_ISSUER must match AO_CLOUD_WORKOS_CLIENT_ID")
		}
		if cfg.WorkOSJWKSURL == "" {
			if cfg.WorkOSIssuer == workOSIssuer {
				cfg.WorkOSJWKSURL = workOSAPIBaseURL + "/sso/jwks/" + cfg.WorkOSClientID
			} else {
				cfg.WorkOSJWKSURL = strings.TrimRight(cfg.WorkOSIssuer, "/") + "/oauth2/jwks"
			}
		}
	}
	if cfg.WorkOSIssuer == "" && !cfg.LocalAuthEnabled {
		return Config{}, errors.New("configure WorkOS or enable AO_CLOUD_LOCAL_AUTH")
	}
	if cfg.LocalAuthEnabled && cfg.Hosted() {
		return Config{}, errors.New("AO_CLOUD_LOCAL_AUTH cannot be enabled in staging or production")
	}
	if cfg.LocalAuthEnabled && cfg.WorkOSIssuer != "" {
		return Config{}, errors.New("AO_CLOUD_LOCAL_AUTH cannot be combined with WorkOS")
	}
	if cfg.Hosted() && len(cfg.ProviderSecretKey) != 32 {
		return Config{}, errors.New(
			"AO_CLOUD_PROVIDER_SECRET_KEY is required in hosted environments",
		)
	}
	if cfg.LocalSessionTTL <= 0 {
		return Config{}, errors.New("AO_CLOUD_LOCAL_SESSION_TTL must be positive")
	}
	switch cfg.SandboxProvider {
	case "ecs", "daytona", "docker", "nodeops":
	default:
		return Config{}, errors.New("AO_CLOUD_SANDBOX_PROVIDER must be ecs, daytona, docker, or nodeops")
	}
	if cfg.Hosted() && cfg.SandboxProvider != "nodeops" {
		return Config{}, errors.New("AO_CLOUD_SANDBOX_PROVIDER must be nodeops in staging and production")
	}
	if cfg.SandboxProvider == "docker" {
		if err := (sandbox.DockerConfig{
			Host:           cfg.DockerHost,
			WorkerImage:    cfg.DockerWorkerImage,
			Network:        cfg.DockerNetwork,
			Namespace:      cfg.DockerNamespace,
			WorkerTokenTTL: cfg.DockerWorkerTokenTTL,
		}).Validate(); err != nil {
			return Config{}, err
		}
	}
	if cfg.SandboxProvider == "nodeops" || cfg.Hosted() {
		if err := (sandbox.NodeOpsConfig{
			BaseURL:          cfg.NodeOpsBaseURL,
			APIKey:           cfg.NodeOpsAPIKey,
			DefaultShape:     cfg.NodeOpsDefaultShape,
			DefaultRootFS:    cfg.NodeOpsDefaultRootFS,
			RootFSByHarness:  cfg.NodeOpsRootFSByHarness,
			Ingress:          cfg.NodeOpsIngress,
			SSHKeyPath:       cfg.NodeOpsSSHKeyPath,
			WorkerTokenTTL:   cfg.NodeOpsWorkerTokenTTL,
			AutoPauseSeconds: cfg.NodeOpsAutoPauseSeconds,
		}).Validate(); err != nil {
			return Config{}, err
		}
	}
	if cfg.SandboxProvider == "nodeops" || cfg.SandboxProvider == "docker" {
		// A worker can only dial home if it is told where home is, and can only
		// be trusted if its token is signed by a key strong enough to matter.
		if cfg.PublicURL == "" {
			return Config{}, fmt.Errorf(
				"AO_CLOUD_PUBLIC_URL is required when AO_CLOUD_SANDBOX_PROVIDER=%s",
				cfg.SandboxProvider,
			)
		}
		// A worker reads this origin out of its environment and dials it with
		// no user agent to fall back on, so a malformed value fails silently
		// inside the sandbox. Reject it here instead.
		workerHome, err := url.Parse(cfg.PublicURL)
		if err != nil || workerHome.Host == "" ||
			(workerHome.Scheme != "http" && workerHome.Scheme != "https") {
			return Config{}, errors.New("AO_CLOUD_PUBLIC_URL must be an absolute http or https origin")
		}
		if cfg.Hosted() && workerHome.Scheme != "https" {
			return Config{}, errors.New("AO_CLOUD_PUBLIC_URL must use HTTPS in hosted environments")
		}
		if len(cfg.WorkerSigningKey) < minWorkerSigningKeyLength {
			return Config{}, fmt.Errorf(
				"AO_CLOUD_WORKER_SIGNING_KEY must be at least %d characters when sandbox workers are enabled",
				minWorkerSigningKeyLength,
			)
		}
	}
	if cfg.SandboxProvider == "nodeops" {
		if cfg.WorkerBinaryPath == "" {
			return Config{}, errors.New("AO_CLOUD_WORKER_BINARY_PATH is required when AO_CLOUD_SANDBOX_PROVIDER=nodeops")
		}
		if cfg.WorkerHelperBinaryPath == "" {
			return Config{}, errors.New("AO_CLOUD_WORKER_HELPER_BINARY_PATH is required when AO_CLOUD_SANDBOX_PROVIDER=nodeops")
		}
	}
	if cfg.ReconcileInterval <= 0 {
		return Config{}, errors.New("AO_CLOUD_SANDBOX_RECONCILE_INTERVAL must be positive")
	}
	// The startup budget must outlast a cold provider boot. Set it too low and
	// every cycle replaces a sandbox that was still starting, which never
	// converges and bills for every discarded attempt.
	if cfg.SandboxStartupTimeout < 30*time.Second {
		return Config{}, errors.New("AO_CLOUD_SANDBOX_STARTUP_TIMEOUT must be at least 30s")
	}
	if cfg.WorkerHeartbeatTimeout < 30*time.Second {
		return Config{}, errors.New("AO_CLOUD_WORKER_HEARTBEAT_TIMEOUT must be at least 30s")
	}
	if cfg.IdlePauseInterval <= 0 {
		return Config{}, errors.New("AO_CLOUD_IDLE_PAUSE_INTERVAL must be positive")
	}
	if cfg.IdlePauseThreshold < time.Minute {
		return Config{}, errors.New("AO_CLOUD_IDLE_PAUSE_THRESHOLD must be at least 1m")
	}
	if cfg.PRStatusPollInterval <= 0 {
		return Config{}, errors.New("AO_CLOUD_PR_STATUS_POLL_INTERVAL must be positive")
	}
	if cfg.MaxSandboxesPerOrg < 1 {
		return Config{}, errors.New("AO_CLOUD_MAX_ACTIVE_SANDBOXES_PER_ORG must be at least 1")
	}
	if cfg.Release == "" {
		if cfg.Hosted() {
			return Config{}, errors.New("AO_CLOUD_RELEASE is required in staging and production")
		}
		cfg.Release = "dev"
	}
	if !releasePattern.MatchString(cfg.Release) {
		return Config{}, errors.New("AO_CLOUD_RELEASE must be a release tag or Git SHA")
	}
	githubValues := []bool{
		cfg.GitHub.AppID > 0,
		cfg.GitHub.AppSlug != "",
		cfg.GitHub.ClientID != "",
		cfg.GitHub.ClientSecret != "",
		cfg.GitHub.PrivateKeyPEM != "",
		cfg.GitHub.WebhookSecret != "",
		len(cfg.GitHub.StateKey) != 0,
	}
	configuredGitHubValues := 0
	for _, configured := range githubValues {
		if configured {
			configuredGitHubValues++
		}
	}
	if configuredGitHubValues != 0 && configuredGitHubValues != len(githubValues) {
		return Config{}, errors.New("all AO_CLOUD_GITHUB_* credentials must be set together")
	}
	// AO_CLOUD_PUBLIC_URL is no longer a GitHub-only setting: a sandbox worker
	// opens no inbound port and can only reach the control plane by dialing
	// this origin. It is therefore validated on its own, and merely required
	// by whichever features need it.
	if cfg.GitHub.Enabled() && cfg.GitHub.PublicURL == "" {
		return Config{}, errors.New("AO_CLOUD_PUBLIC_URL is required when the GitHub App is configured")
	}
	if cfg.GitHub.Enabled() && cfg.Environment != "production" {
		return Config{}, errors.New("GitHub App credentials may only be configured in production")
	}
	if cfg.GitHub.Enabled() {
		publicURL, err := url.Parse(cfg.GitHub.PublicURL)
		if err != nil || publicURL.Host == "" || publicURL.User != nil ||
			(publicURL.Path != "" && publicURL.Path != "/") ||
			publicURL.RawQuery != "" || publicURL.Fragment != "" ||
			(publicURL.Scheme != "http" && publicURL.Scheme != "https") {
			return Config{}, errors.New("AO_CLOUD_PUBLIC_URL must be an absolute origin without credentials, path, query, or fragment")
		}
		if cfg.Hosted() && publicURL.Scheme != "https" {
			return Config{}, errors.New("AO_CLOUD_PUBLIC_URL must use HTTPS in hosted environments")
		}
		if !githubSlugPattern.MatchString(cfg.GitHub.AppSlug) {
			return Config{}, errors.New("AO_CLOUD_GITHUB_APP_SLUG is invalid")
		}
		if cfg.GitHub.InstallTTL <= 0 || cfg.GitHub.InstallTTL > 30*time.Minute {
			return Config{}, errors.New("AO_CLOUD_GITHUB_INSTALL_TTL must be positive and no more than 30 minutes")
		}
		if cfg.GitHub.WebhookMaxBody < 1024 || cfg.GitHub.WebhookMaxBody > 10<<20 {
			return Config{}, errors.New("AO_CLOUD_GITHUB_WEBHOOK_MAX_BYTES must be between 1024 and 10485760")
		}
		if len(cfg.RepositoryBrokerToken) < 32 {
			return Config{}, errors.New("AO_CLOUD_REPOSITORY_BROKER_TOKEN must be at least 32 characters when GitHub is configured")
		}
	}
	brokerConfigured := cfg.RepositoryBrokerURL != "" ||
		cfg.EnvironmentControlToken != ""
	if cfg.Environment != "production" && brokerConfigured {
		if len(cfg.RepositoryBrokerToken) < 32 ||
			len(cfg.EnvironmentControlToken) < 32 {
			return Config{}, errors.New("repository broker and environment control tokens must be at least 32 characters")
		}
		brokerURL, err := url.Parse(cfg.RepositoryBrokerURL)
		if err != nil || brokerURL.Scheme != "https" ||
			brokerURL.Host == "" || brokerURL.User != nil ||
			(brokerURL.Path != "" && brokerURL.Path != "/") ||
			brokerURL.RawQuery != "" || brokerURL.Fragment != "" {
			return Config{}, errors.New("AO_CLOUD_REPOSITORY_BROKER_URL must be an HTTPS origin")
		}
	}
	if cfg.Environment == "staging" && !brokerConfigured {
		return Config{}, errors.New("repository broker configuration is required in staging")
	}
	return cfg, nil
}

func (c Config) Hosted() bool {
	return c.Environment == "staging" || c.Environment == "production"
}

func (c Config) WorkerTokenTTL() time.Duration {
	if c.SandboxProvider == sandbox.ProviderDocker {
		return c.DockerWorkerTokenTTL
	}
	return c.NodeOpsWorkerTokenTTL
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// defaultSandboxProvider picks the provider an unconfigured deployment gets.
// Hosted environments run on NodeOps, which is also the only provider they are
// allowed to run on; locally there is no NodeOps account, so the default is the
// provider a developer can actually reach.
func defaultSandboxProvider(hosted bool) string {
	if hosted {
		return sandbox.ProviderNodeOps
	}
	return sandbox.DefaultProvider
}

func intEnvOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func int64Env(key string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	return value
}

func int64EnvOrDefault(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func (c Config) String() string {
	authMode := "workos"
	if c.LocalAuthEnabled {
		authMode = "local"
	}
	return fmt.Sprintf("environment=%s address=%s auth=%s release=%s", c.Environment, c.HTTPAddress, authMode, c.Release)
}

// Package accountsmanager embeds the upstream CLIProxyAPI SDK behind AO's
// Codex session boundary. AO owns the process lifetime and the route
// capability; CLIProxyAPI owns provider authentication and request execution.
package accountsmanager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultRouteTokenEnv = ports.CodexProxyTokenEnv
	serverReadyTimeout   = 10 * time.Second
	serverPollInterval   = 20 * time.Millisecond
)

// Options configures the daemon-owned embedded proxy. NativeAccountRoot is
// optional; when set, AO's existing private Codex account homes are mirrored
// into the proxy's private auth directory without exposing credentials through
// the AO API.
type Options struct {
	// StateDir is the root for Accounts Manager files. DataDir is retained as
	// a compatibility fallback for callers and tests from before the state
	// directory was separated from SQLite data.
	StateDir          string
	DataDir           string
	LegacyDataDir     string
	NativeAccountRoot string
}

// Account is the redacted account view used by internal management surfaces.
// It intentionally contains no token, auth file path, or raw provider data.
type Account struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Email       string `json:"email,omitempty"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
}

// Service owns one loopback-only CLIProxyAPI instance for the AO daemon.
type Service struct {
	root              string
	authDir           string
	configPath        string
	baseURL           string
	nativeAccountRoot string

	proxy       *cliproxy.Service
	coreManager *coreauth.Manager
	routes      *routeState
	capability  *routeCapability
	switches    *codexAccountSwitchStore

	nativeMu      sync.Mutex
	nativeRefs    map[string]string            // AO/native account id -> proxy auth id
	nativeDigests map[string][sha256.Size]byte // AO/native account id -> source credential digest

	lifecycleMu sync.Mutex
	runCancel   context.CancelFunc
	runCtx      context.Context
	runDone     chan error
	closed      bool
}

// New prepares private state and a ready-to-run upstream service. It does not
// bind a socket; Start must be called by daemon lifecycle wiring.
func New(options Options) (*Service, error) {
	root := strings.TrimSpace(options.StateDir)
	if root == "" {
		root = strings.TrimSpace(options.DataDir)
	}
	if root == "" {
		return nil, fmt.Errorf("accounts manager state directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create accounts manager state directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- owner-only permissions are required for a private directory.
		return nil, fmt.Errorf("protect accounts manager state directory: %w", err)
	}
	managerRoot := filepath.Join(root, "accounts-manager")
	legacyRoot := filepath.Join(strings.TrimSpace(options.LegacyDataDir), "accounts-manager")
	if strings.TrimSpace(options.LegacyDataDir) != "" && filepath.Clean(legacyRoot) != filepath.Clean(managerRoot) {
		if _, newErr := os.Stat(managerRoot); errors.Is(newErr, os.ErrNotExist) {
			if _, oldErr := os.Stat(legacyRoot); oldErr == nil {
				if err := os.Rename(legacyRoot, managerRoot); err != nil {
					return nil, fmt.Errorf("migrate accounts manager state: %w", err)
				}
			}
		} else if newErr != nil {
			return nil, fmt.Errorf("inspect accounts manager state: %w", newErr)
		}
	}
	authDir := filepath.Join(managerRoot, "auth")
	if err := ensurePrivateDirectory(managerRoot); err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(authDir); err != nil {
		return nil, err
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("allocate accounts manager port: %w", err)
	}
	routingKey, err := randomBytes(32)
	if err != nil {
		return nil, fmt.Errorf("create accounts manager routing key: %w", err)
	}
	if err := writePrivateFile(filepath.Join(managerRoot, "routing.key"), routingKey); err != nil {
		return nil, err
	}

	configPath := filepath.Join(managerRoot, "config.yaml")
	cfg := &sdkconfig.Config{
		Host:          "127.0.0.1",
		Port:          port,
		AuthDir:       authDir,
		WebsocketAuth: true,
		RemoteManagement: sdkconfig.RemoteManagement{
			AllowRemote:            false,
			DisableControlPanel:    true,
			DisableAutoUpdatePanel: true,
		},
	}
	if err := writeConfig(configPath, cfg); err != nil {
		return nil, err
	}
	routes, err := newRouteState(filepath.Join(managerRoot, "routes.json"))
	if err != nil {
		return nil, err
	}
	switches, err := newCodexAccountSwitchStore(filepath.Join(managerRoot, "switch-state.json"))
	if err != nil {
		return nil, err
	}
	capability, err := newRouteCapability(routingKey)
	if err != nil {
		return nil, err
	}

	tokenStore := sdkauth.NewFileTokenStore()
	tokenStore.SetBaseDir(authDir)
	coreManager := coreauth.NewManager(tokenStore, nil, nil)
	service := &Service{
		root:              managerRoot,
		authDir:           authDir,
		configPath:        configPath,
		baseURL:           fmt.Sprintf("http://127.0.0.1:%d", port),
		nativeAccountRoot: strings.TrimSpace(options.NativeAccountRoot),
		coreManager:       coreManager,
		routes:            routes,
		capability:        capability,
		switches:          switches,
		nativeRefs:        make(map[string]string),
		nativeDigests:     make(map[string][sha256.Size]byte),
	}
	if err := service.syncNativeAccounts(); err != nil {
		return nil, err
	}

	sdkaccess.RegisterProvider(routeAccessProviderType, capability)
	built, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithRequestAccessManager(sdkaccess.NewManager()).
		WithCoreAuthManager(coreManager).
		WithWatcherFactory(disabledWatcherFactory).
		WithHooks(cliproxy.Hooks{OnAfterStart: func(*cliproxy.Service) {
			coreManager.SetSelector(newExactRouteSelector(capability, routes, coreManager.Selector()))
		}}).
		Build()
	if err != nil {
		sdkaccess.UnregisterProvider(routeAccessProviderType)
		return nil, fmt.Errorf("build embedded CLIProxyAPI: %w", err)
	}
	service.proxy = built
	return service, nil
}

// Start binds the loopback proxy and waits until its listener is accepting
// connections. It is safe to call once; a failed start must be discarded.
func (s *Service) Start(ctx context.Context) error {
	if s == nil || s.proxy == nil {
		return ports.ErrCodexProxyUnavailable
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return ports.ErrCodexProxyUnavailable
	}
	if s.runDone != nil {
		s.lifecycleMu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	s.runCancel = cancel
	s.runCtx = runCtx
	s.runDone = done
	s.lifecycleMu.Unlock()
	go func() {
		err := s.proxy.Run(runCtx)
		if errors.Is(err, context.Canceled) || runCtx.Err() != nil {
			err = nil
		}
		done <- err
	}()
	if err := s.waitReady(ctx, done); err != nil {
		cancel()
		s.lifecycleMu.Lock()
		if s.runDone == done {
			s.runCancel = nil
			s.runCtx = nil
			s.runDone = nil
		}
		s.lifecycleMu.Unlock()
		sdkaccess.UnregisterProvider(routeAccessProviderType)
		return err
	}
	return nil
}

// Close stops the embedded proxy and removes its process-global access
// provider registration. It never removes credentials or route state.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	cancel, done := s.runCancel, s.runDone
	s.runCtx = nil
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if done != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case err = <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	sdkaccess.UnregisterProvider(routeAccessProviderType)
	return err
}

func (s *Service) waitReady(ctx context.Context, done <-chan error) error {
	deadline := time.NewTimer(serverReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(serverPollInterval)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", strings.TrimPrefix(s.baseURL, "http://"))
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-done:
			if err == nil {
				return ports.ErrCodexProxyUnavailable
			}
			return fmt.Errorf("start embedded CLIProxyAPI: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wait for embedded CLIProxyAPI: %w", ports.ErrCodexProxyUnavailable)
		case <-ticker.C:
		}
	}
}

// RouteForSession returns the stable connection material for a Codex process.
// Existing sessions keep their persisted account pin; an unavailable pin is a
// hard error and is never silently replaced with another account.
func (s *Service) RouteForSession(ctx context.Context, sessionID string) (ports.AgentProviderRoute, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentProviderRoute{}, err
	}
	if s == nil || s.coreManager == nil || s.capability == nil {
		return ports.AgentProviderRoute{}, ports.ErrCodexProxyUnavailable
	}
	if err := s.refreshAccounts(ctx); err != nil {
		return ports.AgentProviderRoute{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ports.AgentProviderRoute{}, fmt.Errorf("route Codex session: session id is required")
	}
	accountID, pinned := s.routes.accountForSession(sessionID)
	if pinned {
		if _, ok := s.resolveUsableAccount(accountID); !ok {
			return ports.AgentProviderRoute{}, fmt.Errorf("route Codex session %s: %w", sessionID, ports.ErrCodexProxyAccountUnavailable)
		}
	} else {
		if activeID, active := s.routes.activeAccountID(); active {
			if _, ok := s.resolveUsableAccount(activeID); !ok {
				return ports.AgentProviderRoute{}, fmt.Errorf("route Codex session %s: %w", sessionID, ports.ErrCodexProxyAccountUnavailable)
			}
			accountID = activeID
		} else {
			account, ok := s.defaultUsableAccount()
			if !ok {
				return ports.AgentProviderRoute{}, ports.ErrCodexProxyNoAccounts
			}
			accountID = account.ID
		}
		if err := s.routes.setAccountForSession(sessionID, accountID); err != nil {
			return ports.AgentProviderRoute{}, fmt.Errorf("persist Codex session account: %w", err)
		}
	}
	token, err := s.capability.Mint(sessionID)
	if err != nil {
		return ports.AgentProviderRoute{}, err
	}
	return ports.AgentProviderRoute{
		BaseURL:      s.baseURL,
		ProviderName: ports.CodexProxyProviderName,
		Token:        token,
		TokenEnv:     defaultRouteTokenEnv,
	}, nil
}

// SwitchAllSessionsAccount selects the default account for future Codex
// sessions. Existing session pins are intentionally preserved.
func (s *Service) SwitchAllSessionsAccount(ctx context.Context, accountRef string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.coreManager == nil {
		return "", ports.ErrCodexProxyUnavailable
	}
	if err := s.refreshAccounts(ctx); err != nil {
		return "", err
	}
	account, ok := s.resolveUsableAccount(accountRef)
	if !ok {
		return "", ports.ErrCodexProxyAccountUnavailable
	}
	if err := s.routes.setAccountForAllSessions(account.ID); err != nil {
		return "", err
	}
	return s.externalAccountID(account.ID), nil
}

// SwitchSessionAccount updates the persisted account pin used by future
// requests from a running Codex process. The process and route token remain
// unchanged.
func (s *Service) SwitchSessionAccount(ctx context.Context, sessionID, accountRef string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.coreManager == nil {
		return "", ports.ErrCodexProxyUnavailable
	}
	if err := s.refreshAccounts(ctx); err != nil {
		return "", err
	}
	account, ok := s.resolveUsableAccount(accountRef)
	if !ok {
		return "", ports.ErrCodexProxyAccountUnavailable
	}
	if err := s.routes.setAccountForSession(sessionID, account.ID); err != nil {
		return "", err
	}
	return s.externalAccountID(account.ID), nil
}

// Accounts returns a redacted snapshot for an AO management surface.
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	if err := s.refreshAccounts(ctx); err != nil {
		return nil, err
	}
	accounts := make([]Account, 0)
	for _, auth := range s.coreManager.List() {
		if auth == nil || !strings.EqualFold(auth.Provider, "codex") {
			continue
		}
		accounts = append(accounts, Account{
			ID:          s.externalAccountID(auth.ID),
			Label:       auth.Label,
			Email:       accountEmail(auth),
			Provider:    auth.Provider,
			Status:      string(auth.Status),
			Disabled:    auth.Disabled,
			Unavailable: auth.Unavailable,
		})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return accounts, nil
}

// CreateCodexAccountSwitch persists a Codex account-switch journal record.
func (s *Service) CreateCodexAccountSwitch(ctx context.Context, record domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	if s == nil || s.switches == nil {
		return domain.CodexAccountSwitch{}, false, ports.ErrCodexProxyUnavailable
	}
	return s.switches.CreateCodexAccountSwitch(ctx, record)
}

// GetCodexAccountSwitch returns a Codex account-switch journal record by ID.
func (s *Service) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	if s == nil || s.switches == nil {
		return domain.CodexAccountSwitch{}, false, ports.ErrCodexProxyUnavailable
	}
	return s.switches.GetCodexAccountSwitch(ctx, id)
}

// GetCodexAccountSwitchByIdempotency returns a switch record by idempotency key.
func (s *Service) GetCodexAccountSwitchByIdempotency(ctx context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	if s == nil || s.switches == nil {
		return domain.CodexAccountSwitch{}, false, ports.ErrCodexProxyUnavailable
	}
	return s.switches.GetCodexAccountSwitchByIdempotency(ctx, key)
}

// GetActiveCodexAccountSwitch returns the current nonterminal switch record.
func (s *Service) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	if s == nil || s.switches == nil {
		return domain.CodexAccountSwitch{}, false, ports.ErrCodexProxyUnavailable
	}
	return s.switches.GetActiveCodexAccountSwitch(ctx)
}

// UpdateCodexAccountSwitch advances a Codex account-switch journal record.
func (s *Service) UpdateCodexAccountSwitch(ctx context.Context, record domain.CodexAccountSwitch, expected domain.CodexAccountSwitchPhase) (bool, error) {
	if s == nil || s.switches == nil {
		return false, ports.ErrCodexProxyUnavailable
	}
	return s.switches.UpdateCodexAccountSwitch(ctx, record, expected)
}

func (s *Service) refreshAccounts(ctx context.Context) error {
	if err := s.syncNativeAccounts(); err != nil {
		return err
	}
	if err := s.coreManager.Load(ctx); err != nil {
		return fmt.Errorf("load Codex accounts into proxy: %w", err)
	}
	return nil
}

func (s *Service) defaultUsableAccount() (*coreauth.Auth, bool) {
	accounts := s.coreManager.List()
	sort.SliceStable(accounts, func(i, j int) bool {
		if !accounts[i].CreatedAt.Equal(accounts[j].CreatedAt) {
			return accounts[i].CreatedAt.Before(accounts[j].CreatedAt)
		}
		return accounts[i].ID < accounts[j].ID
	})
	for _, account := range accounts {
		if exactRouteAuthUsable(account, "", time.Now()) {
			return account, true
		}
	}
	return nil, false
}

func (s *Service) resolveUsableAccount(ref string) (*coreauth.Auth, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, false
	}
	if auth, ok := s.coreManager.GetByID(ref); ok && exactRouteAuthUsable(auth, "", time.Now()) {
		return auth, true
	}
	s.nativeMu.Lock()
	proxyID := s.nativeRefs[ref]
	s.nativeMu.Unlock()
	if proxyID == "" {
		return nil, false
	}
	auth, ok := s.coreManager.GetByID(proxyID)
	return auth, ok && exactRouteAuthUsable(auth, "", time.Now())
}

func (s *Service) externalAccountID(proxyID string) string {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	for nativeID, currentProxyID := range s.nativeRefs {
		if currentProxyID == proxyID {
			return nativeID
		}
	}
	return proxyID
}

func (s *Service) syncNativeAccounts() error {
	root := strings.TrimSpace(s.nativeAccountRoot)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return fmt.Errorf("read native Codex accounts: %w", err)
	}
	seen := make(map[string]string)
	digests := make(map[string][sha256.Size]byte)
	for _, entry := range entries {
		if !entry.IsDir() || !safePathComponent(entry.Name()) {
			continue
		}
		source := filepath.Join(root, entry.Name(), "credential-home", "auth.json")
		raw, readErr := os.ReadFile(source)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read native Codex credential: %w", readErr)
		}
		proxyCredential, ok := proxyCodexCredential(raw)
		if !ok {
			continue
		}
		name := "ao-native-" + entry.Name() + ".json"
		destination := filepath.Join(s.authDir, name)
		digest := sha256.Sum256(raw)
		s.nativeMu.Lock()
		previousDigest, hadPreviousDigest := s.nativeDigests[entry.Name()]
		s.nativeMu.Unlock()
		if _, readErr := os.Stat(destination); errors.Is(readErr, os.ErrNotExist) || !hadPreviousDigest || previousDigest != digest {
			if err := os.WriteFile(destination, proxyCredential, 0o600); err != nil {
				return fmt.Errorf("mirror native Codex credential: %w", err)
			}
		} else if readErr != nil {
			return fmt.Errorf("inspect mirrored native Codex credential: %w", readErr)
		}
		if err := os.Chmod(destination, 0o600); err != nil {
			return fmt.Errorf("protect mirrored native Codex credential: %w", err)
		}
		seen[entry.Name()] = name
		digests[entry.Name()] = digest
	}
	s.nativeMu.Lock()
	previous := s.nativeRefs
	s.nativeRefs = make(map[string]string, len(seen))
	s.nativeDigests = make(map[string][sha256.Size]byte, len(digests))
	for nativeID, proxyID := range seen {
		s.nativeRefs[nativeID] = proxyID
		s.nativeDigests[nativeID] = digests[nativeID]
	}
	s.nativeMu.Unlock()
	for nativeID, name := range previous {
		if _, ok := seen[nativeID]; !ok {
			_ = os.Remove(filepath.Join(s.authDir, name))
		}
	}
	return nil
}

// proxyCodexCredential converts AO's native Codex auth.json shape into the
// CLIProxyAPI file format. The conversion is intentionally write-only into
// AO's private proxy directory: it never mutates the native account home or
// exposes any credential through an AO API response.
func proxyCodexCredential(raw []byte) ([]byte, bool) {
	var document struct {
		Type         string `json:"type"`
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`
		Tokens       *struct {
			AccountID    string `json:"account_id"`
			AccessToken  string `json:"access_token"`
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, false
	}
	if strings.EqualFold(strings.TrimSpace(document.Type), "codex") {
		return raw, true
	}
	credential := map[string]string{"type": "codex"}
	if key := strings.TrimSpace(document.OpenAIAPIKey); key != "" {
		// CLIProxyAPI's Codex executor uses access_token for both OAuth and
		// native API-key credentials.
		credential["access_token"] = key
	} else if document.Tokens != nil {
		for key, value := range map[string]string{
			"account_id":    document.Tokens.AccountID,
			"access_token":  document.Tokens.AccessToken,
			"id_token":      document.Tokens.IDToken,
			"refresh_token": document.Tokens.RefreshToken,
		} {
			if value = strings.TrimSpace(value); value != "" {
				credential[key] = value
			}
		}
	}
	if credential["access_token"] == "" && credential["refresh_token"] == "" && credential["id_token"] == "" {
		return nil, false
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// disabledWatcherFactory intentionally disables CLIProxyAPI's broad file
// watcher. AO owns the account source and synchronizes it before each route
// read or mutation, so a second asynchronous watcher would both duplicate that
// work and let an upstream watcher race the proxy server's startup lifecycle.
func disabledWatcherFactory(string, string, func(*sdkconfig.Config)) (*cliproxy.WatcherWrapper, error) {
	return &cliproxy.WatcherWrapper{}, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private accounts manager directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- owner-only permissions are required for a private directory.
		return fmt.Errorf("protect private accounts manager directory: %w", err)
	}
	return nil
}

func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write private accounts manager file: %w", err)
	}
	return nil
}

func writeConfig(path string, cfg *sdkconfig.Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode accounts manager config: %w", err)
	}
	if err := writePrivateFile(path, raw); err != nil {
		return err
	}
	return nil
}

func randomBytes(size int) ([]byte, error) {
	if size <= 0 {
		return nil, errors.New("secret size must be positive")
	}
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return nil, err
	}
	return data, nil
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("loopback listener did not return a TCP address")
	}
	return address.Port, nil
}

func safePathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func accountEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["email"]) != "" {
		return strings.TrimSpace(auth.Attributes["email"])
	}
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(email)
		}
	}
	return ""
}

var _ ports.CodexRouteProvider = (*Service)(nil)
var _ ports.CodexAccountSwitchStore = (*Service)(nil)

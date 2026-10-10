package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	proxyapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Build writes the SDK's configuration under root and puts the boundary in front of the SDK.
func Build(root string, port int, controlKey, inferenceKey string, routes *Routes, activity *Activity) (*cliproxy.Service, error) {
	if !filepath.IsAbs(root) || port < 1 || port > 65535 || len(controlKey) < 32 || len(inferenceKey) < 32 || controlKey == inferenceKey {
		return nil, errors.New("invalid proxy host configuration")
	}
	path, authDir := filepath.Join(root, "config.yaml"), filepath.Join(root, "auth")
	err := os.MkdirAll(authDir, 0700)
	if err == nil {
		err = os.Chmod(authDir, 0700)
	}
	if err != nil {
		return nil, err
	}
	// A saved file is loaded strictly: the lenient load answers a bad file with an empty one, which would lose its API keys.
	cfg, err := config.LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = config.LoadConfigOptional(path, true)
	}
	if err != nil {
		return nil, err
	}
	// The SDK accepts its local management password only when a secret is configured.
	secret, err := bcrypt.GenerateFromPassword([]byte(controlKey), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.AuthDir, cfg.APIKeys = "127.0.0.1", port, authDir, []string{inferenceKey}
	cfg.CommercialMode, cfg.MaxRetryCredentials, cfg.WebsocketAuth = true, 1, true
	// AO names the exact account for every request; the SDK must not choose one itself.
	cfg.Routing.Strategy, cfg.Routing.SessionAffinity = "fill-first", false
	cfg.RemoteManagement.DisableControlPanel, cfg.RemoteManagement.DisableAutoUpdatePanel = true, true
	cfg.RemoteManagement.SecretKey = string(secret)
	data, err := yaml.Marshal(cfg)
	if err == nil {
		err = writePrivate(path, data)
	}
	if err != nil {
		return nil, err
	}
	b := Boundary{Routes: routes, ControlKey: controlKey, InferenceKey: inferenceKey, Logins: newLogins(authDir), Activity: activity}
	coreusage.RegisterNamedPlugin("ao-activity", activity)
	// The SDK runs an added middleware after its logger, which gives a request the id that bind goes by.
	return cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).WithLocalManagementPassword(controlKey).
		WithPostAuthHook(tagLogin).WithServerOptions(proxyapi.WithEngineConfigurator(func(e *gin.Engine) { e.Use(b.Middleware) }), proxyapi.WithMiddleware(activity.bind), proxyapi.WithRouterConfigurator(b.Configure)).Build()
}
func Run(ctx context.Context, root string, port int, controlKey, inferenceKey string) error {
	activity := OpenActivity(filepath.Join(root, "run", "activity.json"))
	service, err := Build(root, port, controlKey, inferenceKey, OpenRoutes(filepath.Join(root, "run", "routes.json")), activity)
	if err != nil {
		return err
	}
	// Saved every half minute when something changed, and once when the service stops.
	defer activity.save()
	go func() {
		for range time.Tick(30 * time.Second) {
			activity.save()
		}
	}()
	return service.Run(ctx)
}
func tagLogin(ctx context.Context, a *coreauth.Auth) error {
	if info := coreauth.GetRequestInfo(ctx); info != nil && info.Headers.Get("X-AO-Login-ID") != "" && len(info.Headers.Get("X-AO-Login-ID")) <= 128 {
		if a.Metadata == nil {
			a.Metadata = map[string]any{}
		}
		a.Metadata["ao_login_id"] = info.Headers.Get("X-AO-Login-ID")
	}
	return nil
}

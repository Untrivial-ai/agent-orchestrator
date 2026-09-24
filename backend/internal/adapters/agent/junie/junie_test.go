package junie

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recordingRuntimeFileBuilder struct {
	files    RuntimeFiles
	err      error
	requests []RuntimeFileRequest
}

func (b *recordingRuntimeFileBuilder) Prepare(ctx context.Context, request RuntimeFileRequest) (RuntimeFiles, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeFiles{}, err
	}
	b.requests = append(b.requests, request)
	return b.files, b.err
}

func TestManifestDescribesUnregisteredJunieAdapter(t *testing.T) {
	manifest := New().Manifest()
	if manifest.ID != "junie" || manifest.Name != "Junie" {
		t.Fatalf("Manifest() = %#v, want Junie identity", manifest)
	}
	if !reflect.DeepEqual(manifest.Capabilities, []adapters.Capability{adapters.CapabilityAgent}) {
		t.Fatalf("Manifest().Capabilities = %#v, want agent only", manifest.Capabilities)
	}
}

func TestGetConfigSpecDeclaresModelAndEffort(t *testing.T) {
	spec, err := New().GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ports.ConfigSpec{Fields: []ports.ConfigField{
		{Key: "model", Type: ports.ConfigFieldString, Description: "Model override passed to `junie --model`."},
		{Key: "effort", Type: ports.ConfigFieldEnum, Description: "Junie reasoning effort.", Enum: []string{"low", "medium", "high"}},
	}}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("GetConfigSpec() = %#v, want %#v", spec, want)
	}
}

func TestGetLaunchCommandBuildsExpectedArgv(t *testing.T) {
	tests := []struct {
		name        string
		cfg         ports.LaunchConfig
		files       RuntimeFiles
		want        []string
		wantErrText string
	}{
		{
			name: "worker includes prompt model effort guidelines and brave",
			cfg: ports.LaunchConfig{
				Config:          ports.AgentConfig{Model: "  sonnet  ", Effort: " high "},
				DataDir:         "/ao-data",
				SessionID:       "ao-session",
				Kind:            domain.KindWorker,
				Permissions:     ports.PermissionModeBypassPermissions,
				Prompt:          "-review this",
				NativeSessionID: "caller-selected-id",
				SystemPrompt:    "standing instructions",
			},
			files: RuntimeFiles{ConfigPath: "/runtime/config.json", GuidelinesPath: "/runtime/guidelines.md"},
			want:  []string{"junie", "--skip-update-check", "--config-location", "/runtime/config.json", "--guidelines-filename", "/runtime/guidelines.md", "--model", "sonnet", "--effort", "high", "--brave", "--prompt", "-review this"},
		},
		{
			name: "orchestrator preserves explicit task prompt and omits blank model",
			cfg: ports.LaunchConfig{
				Config:      ports.AgentConfig{Model: " \t ", Effort: "low"},
				DataDir:     "/ao-data",
				SessionID:   "orchestrator",
				Kind:        domain.KindOrchestrator,
				Permissions: ports.PermissionModeAuto,
				Prompt:      "coordinate the work",
			},
			files: RuntimeFiles{ConfigPath: "/runtime/config.json"},
			want:  []string{"junie", "--skip-update-check", "--config-location", "/runtime/config.json", "--effort", "low", "--prompt", "coordinate the work"},
		},
		{
			name: "empty worker prompt and medium effort",
			cfg: ports.LaunchConfig{
				Config:      ports.AgentConfig{Effort: "medium"},
				DataDir:     "/ao-data",
				SessionID:   "worker",
				Kind:        domain.KindWorker,
				Permissions: ports.PermissionModeAcceptEdits,
			},
			files: RuntimeFiles{ConfigPath: "/runtime/config.json"},
			want:  []string{"junie", "--skip-update-check", "--config-location", "/runtime/config.json", "--effort", "medium"},
		},
		{
			name: "invalid effort",
			cfg: ports.LaunchConfig{
				Config:    ports.AgentConfig{Effort: "ultra"},
				DataDir:   "/ao-data",
				SessionID: "worker",
			},
			files:       RuntimeFiles{ConfigPath: "/runtime/config.json"},
			wantErrText: "invalid effort",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := &recordingRuntimeFileBuilder{files: tt.files}
			plugin := &Plugin{resolvedBinary: "junie", runtimeFiles: builder}
			got, err := plugin.GetLaunchCommand(context.Background(), tt.cfg)
			if tt.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("GetLaunchCommand() error = %v, want containing %q", err, tt.wantErrText)
				}
				if !errors.Is(err, ports.ErrUnsupportedEffort) {
					t.Fatalf("GetLaunchCommand() error = %v, want ErrUnsupportedEffort", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("GetLaunchCommand()\n got: %#v\nwant: %#v", got, tt.want)
			}
			if containsArg(got, "--config-default-locations") {
				t.Fatalf("GetLaunchCommand() = %#v, must not disable Junie's normal config locations", got)
			}
			wantRequest := RuntimeFileRequest{
				DataDir:          tt.cfg.DataDir,
				SessionID:        tt.cfg.SessionID,
				SystemPrompt:     tt.cfg.SystemPrompt,
				SystemPromptFile: tt.cfg.SystemPromptFile,
			}
			if !reflect.DeepEqual(builder.requests, []RuntimeFileRequest{wantRequest}) {
				t.Fatalf("Prepare requests = %#v, want %#v", builder.requests, []RuntimeFileRequest{wantRequest})
			}
		})
	}
}

func TestGetLaunchCommandPermissionModes(t *testing.T) {
	for _, tt := range []struct {
		name       string
		permission ports.PermissionMode
		wantBrave  bool
	}{
		{name: "default", permission: ports.PermissionModeDefault},
		{name: "accept edits", permission: ports.PermissionModeAcceptEdits},
		{name: "auto", permission: ports.PermissionModeAuto},
		{name: "bypass permissions", permission: ports.PermissionModeBypassPermissions, wantBrave: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plugin := newTestPlugin(&recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}})
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				DataDir: "/ao-data", SessionID: "session", Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := containsArg(cmd, "--brave"); got != tt.wantBrave {
				t.Fatalf("command = %#v, --brave present = %v, want %v", cmd, got, tt.wantBrave)
			}
		})
	}
}

func TestGetLaunchCommandRejectsUnsupportedToolRestrictions(t *testing.T) {
	for _, tt := range []struct {
		name       string
		allowed    []string
		disallowed []string
	}{
		{name: "allowed", allowed: []string{"read"}},
		{name: "disallowed", disallowed: []string{"write"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			builder := &recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}}
			plugin := newTestPlugin(builder)
			_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				DataDir: "/ao-data", SessionID: "session", AllowedTools: tt.allowed, DisallowedTools: tt.disallowed,
			})
			if err == nil || !strings.Contains(err.Error(), "tool restrictions") {
				t.Fatalf("GetLaunchCommand() error = %v, want unsupported tool restrictions", err)
			}
			if len(builder.requests) != 0 {
				t.Fatalf("Prepare called before rejecting restrictions: %#v", builder.requests)
			}
		})
	}
}

func TestGetLaunchCommandPropagatesPreparationErrors(t *testing.T) {
	wantErr := errors.New("prepare failed")
	plugin := newTestPlugin(&recordingRuntimeFileBuilder{err: wantErr})
	_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: "/ao-data", SessionID: "session"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetLaunchCommand() error = %v, want %v", err, wantErr)
	}
}

func TestGetLaunchCommandRejectsMissingRuntimeIdentity(t *testing.T) {
	for _, cfg := range []ports.LaunchConfig{
		{SessionID: "session"},
		{DataDir: t.TempDir()},
	} {
		plugin := &Plugin{resolvedBinary: "junie", runtimeFiles: NewRuntimeFileBuilder()}
		if _, err := plugin.GetLaunchCommand(context.Background(), cfg); err == nil {
			t.Fatalf("GetLaunchCommand(%#v) succeeded", cfg)
		}
	}
}

func TestGetPromptDeliveryStrategyAlwaysUsesCommand(t *testing.T) {
	plugin := New()
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		if got != ports.PromptDeliveryInCommand {
			t.Fatalf("GetPromptDeliveryStrategy(%q) = %q, want %q", kind, got, ports.PromptDeliveryInCommand)
		}
	}
}

func TestGetRestoreCommandReappliesConfiguration(t *testing.T) {
	builder := &recordingRuntimeFileBuilder{files: RuntimeFiles{
		ConfigPath: "/runtime/config.json", GuidelinesPath: "/runtime/guidelines.md",
	}}
	plugin := newTestPlugin(builder)
	cfg := ports.RestoreConfig{
		Config:      ports.AgentConfig{Model: " model-x ", Effort: " medium "},
		DataDir:     "/ao-data",
		Permissions: ports.PermissionModeBypassPermissions,
		Session: ports.SessionRef{
			ID:       "ao-session",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native session id"},
		},
		Prompt:           "-continue",
		SystemPrompt:     "standing instructions",
		SystemPromptFile: "/ao/system.md",
	}
	got, ok, err := plugin.GetRestoreCommand(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("GetRestoreCommand() ok = false, want true")
	}
	want := []string{"junie", "--skip-update-check", "--config-location", "/runtime/config.json", "--guidelines-filename", "/runtime/guidelines.md", "--model", "model-x", "--effort", "medium", "--brave", "--resume", "--session-id", "native session id", "--prompt", "-continue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRestoreCommand()\n got: %#v\nwant: %#v", got, want)
	}
	wantRequest := RuntimeFileRequest{
		DataDir: "/ao-data", SessionID: "ao-session", SystemPrompt: "standing instructions", SystemPromptFile: "/ao/system.md",
	}
	if !reflect.DeepEqual(builder.requests, []RuntimeFileRequest{wantRequest}) {
		t.Fatalf("Prepare requests = %#v, want %#v", builder.requests, []RuntimeFileRequest{wantRequest})
	}
}

func TestGetRestoreCommandRequiresValidHookDerivedNativeID(t *testing.T) {
	invalid := []string{"", "   ", " native", "native ", "-native", "line\nbreak", strings.Repeat("x", 257), strings.Repeat("界", 86)}
	for _, nativeID := range invalid {
		t.Run(nativeID, func(t *testing.T) {
			builder := &recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}}
			plugin := newTestPlugin(builder)
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				DataDir: "/ao-data",
				Session: ports.SessionRef{ID: "ao-session", Metadata: map[string]string{
					ports.MetadataKeyAgentSessionID: nativeID,
				}},
			})
			if nativeID == "" {
				if err != nil || ok || cmd != nil {
					t.Fatalf("missing native id result = (%#v, %v, %v), want (nil, false, nil)", cmd, ok, err)
				}
			} else if err == nil || ok {
				t.Fatalf("invalid native id result = (%#v, %v, %v), want error", cmd, ok, err)
			}
			if len(builder.requests) != 0 {
				t.Fatalf("Prepare called for invalid native id: %#v", builder.requests)
			}
		})
	}
}

func TestGetRestoreCommandAcceptsBroadSafeNativeIDs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		nativeID string
	}{
		{name: "punctuation interior spaces and unicode", nativeID: "native/session id:日本語"},
		{name: "maximum byte length unicode", nativeID: strings.Repeat("界", 85) + "a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nativeID := tt.nativeID
			plugin := newTestPlugin(&recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}})
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				DataDir: "/ao-data",
				Session: ports.SessionRef{ID: "ao-session", Metadata: map[string]string{
					ports.MetadataKeyAgentSessionID: nativeID,
				}},
			})
			if err != nil || !ok {
				t.Fatalf("GetRestoreCommand() = (%#v, %v, %v), want valid restore", cmd, ok, err)
			}
			if !reflect.DeepEqual(cmd[len(cmd)-3:], []string{"--resume", "--session-id", nativeID}) {
				t.Fatalf("GetRestoreCommand() = %#v, want exact native id", cmd)
			}
		})
	}
}

func TestGetRestoreCommandRejectsMissingRuntimeIdentity(t *testing.T) {
	for _, cfg := range []ports.RestoreConfig{
		{DataDir: t.TempDir()},
		{Session: ports.SessionRef{ID: "ao-session"}},
	} {
		cfg.Session.Metadata = map[string]string{ports.MetadataKeyAgentSessionID: "native-id"}
		plugin := &Plugin{resolvedBinary: "junie", runtimeFiles: NewRuntimeFileBuilder()}
		if _, ok, err := plugin.GetRestoreCommand(context.Background(), cfg); err == nil || ok {
			t.Fatalf("GetRestoreCommand(%#v) = ok %v, err %v; want error", cfg, ok, err)
		}
	}
}

func TestGetRestoreCommandRejectsUnsupportedToolRestrictionsAndInvalidEffort(t *testing.T) {
	for _, cfg := range []ports.RestoreConfig{
		{AllowedTools: []string{"read"}},
		{DisallowedTools: []string{"write"}},
		{Config: ports.AgentConfig{Effort: "ultra"}},
	} {
		cfg.DataDir = "/ao-data"
		cfg.Session = ports.SessionRef{ID: "ao-session", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-id"}}
		plugin := newTestPlugin(&recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}})
		if _, ok, err := plugin.GetRestoreCommand(context.Background(), cfg); err == nil || ok {
			t.Fatalf("GetRestoreCommand(%#v) = ok %v, err %v; want error", cfg, ok, err)
		}
	}
}

func TestGetRestoreCommandPropagatesPreparationErrors(t *testing.T) {
	wantErr := errors.New("prepare failed")
	plugin := newTestPlugin(&recordingRuntimeFileBuilder{err: wantErr})
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		DataDir: "/ao-data",
		Session: ports.SessionRef{ID: "ao-session", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-id"}},
	})
	if ok || !errors.Is(err, wantErr) {
		t.Fatalf("GetRestoreCommand() = ok %v, err %v; want false, %v", ok, err, wantErr)
	}
}

func TestJunieOperationsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plugin := newTestPlugin(&recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}})
	if _, err := plugin.GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetLaunchCommand() error = %v, want context canceled", err)
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand() error = %v, want context canceled", err)
	}
	if _, err := plugin.GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec() error = %v, want context canceled", err)
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy() error = %v, want context canceled", err)
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo() error = %v, want context canceled", err)
	}
}

func TestSessionInfoReturnsStandardHookMetadata(t *testing.T) {
	plugin := New()
	got, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{Metadata: map[string]string{
		ports.MetadataKeyAgentSessionID: "native-id",
		ports.MetadataKeyTitle:          "Title",
		ports.MetadataKeySummary:        "Summary",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := ports.SessionInfo{AgentSessionID: "native-id", Title: "Title", Summary: "Summary"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("SessionInfo() = (%#v, %v), want (%#v, true)", got, ok, want)
	}
}

func TestJunieBinarySpecIncludesOfficialInstallLocations(t *testing.T) {
	if !reflect.DeepEqual(junieBinarySpec.Names, []string{"junie"}) {
		t.Fatalf("Names = %#v", junieBinarySpec.Names)
	}
	if !reflect.DeepEqual(junieBinarySpec.WinNames, []string{"junie.bat", "junie.exe", "junie.cmd", "junie"}) {
		t.Fatalf("WinNames = %#v", junieBinarySpec.WinNames)
	}
	if !containsParts(junieBinarySpec.UnixHomePaths, []string{".local", "bin", "junie"}) {
		t.Fatalf("UnixHomePaths = %#v, want ~/.local/bin/junie", junieBinarySpec.UnixHomePaths)
	}
	wantWindows := binaryutil.WinPath{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "junie.bat"}}
	if !containsWindowsPath(junieBinarySpec.WinPaths, wantWindows) {
		t.Fatalf("WinPaths = %#v, want %#v", junieBinarySpec.WinPaths, wantWindows)
	}
}

func newTestPlugin(builder RuntimeFileBuilder) *Plugin {
	return &Plugin{resolvedBinary: "junie", runtimeFiles: builder}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsParts(paths [][]string, want []string) bool {
	for _, path := range paths {
		if reflect.DeepEqual(path, want) {
			return true
		}
	}
	return false
}

func containsWindowsPath(paths []binaryutil.WinPath, want binaryutil.WinPath) bool {
	for _, path := range paths {
		if reflect.DeepEqual(path, want) {
			return true
		}
	}
	return false
}

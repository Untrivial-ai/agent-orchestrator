package modelcatalog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// openCodeLayer is one OpenCode config source that may set the root model.
// certain is false when AO cannot know whether the layer applies to the
// session it will launch; if such a layer is the one that wins, the default is
// left unresolved rather than guessed. unknown marks a layer whose contents AO
// cannot see at all: any model below it may be overridden at launch.
type openCodeLayer struct {
	model   string
	certain bool
	unknown bool
}

// openCodeManagedConfigDirs lists the system directories OpenCode reads
// managed (administrator) config from. Managed config outranks every user
// layer. A package variable so tests can point it at a temp dir.
var openCodeManagedConfigDirs = func() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/opencode"}
	case "windows":
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			return []string{filepath.Join(programData, "opencode")}
		}
		return nil
	default:
		return []string{"/etc/opencode"}
	}
}

// resolveOpenCodeModel returns the root model OpenCode will run for a session
// launched from an AO worktree of workingDir's repository, or "" when that
// cannot be determined.
//
// Both majors layer config lowest to highest as: global, OPENCODE_CONFIG,
// project opencode.json(c) from the outermost directory inward, then
// .opencode/opencode.json(c) the same way, then OPENCODE_CONFIG_CONTENT and
// managed config. v1 stops the project search at the git root; v2 continues
// to the filesystem root.
//
// Only layers that are the same for every launched session are trusted:
//   - project files inside the repository are never read, because the launch
//     worktree's content is chosen per session (see repoMayHoldConfig); if the
//     repository may hold one, or there is no git root to bound the search,
//     the project layers are unknown;
//   - v2 also reads the launch worktree's ancestors above its git root (for
//     example <AO_DATA_DIR>/opencode.json), which discovery cannot see, so for
//     v2 the project layers are always unknown;
//   - the user's OPENCODE_CONFIG is replaced by AO's own file at TUI launch
//     but kept for ACP sessions, and OPENCODE_CONFIG_DIR's contents are not
//     modeled, so both are uncertain.
//
// Remote (organization) config is the lowest layer and is not visible here; it
// can only matter when no visible layer sets a model, which already resolves
// to "".
//
// https://dev.opencode.ai/docs/config/ (v1), https://opencode.ai/v2/docs/config (v2)
func resolveOpenCodeModel(major int, home, workingDir string, env map[string]string) string {
	var layers []openCodeLayer
	if configHome := xdgDir(home, env, "XDG_CONFIG_HOME", ".config"); configHome != "" {
		layers = append(layers, openCodeDirLayer(filepath.Join(configHome, "opencode"), true, "config.json", "opencode.json", "opencode.jsonc"))
	}
	if custom := envValue(env, "OPENCODE_CONFIG"); custom != "" {
		layers = append(layers, openCodeFileLayer(custom, false))
	}
	if openCodeProjectConfigUnknown(major, workingDir) {
		layers = append(layers, openCodeLayer{unknown: true})
	}
	if configDir := envValue(env, "OPENCODE_CONFIG_DIR"); configDir != "" {
		layers = append(layers, openCodeDirLayer(configDir, false, "opencode.json", "opencode.jsonc"))
	}
	if content := envValue(env, "OPENCODE_CONFIG_CONTENT"); content != "" {
		layers = append(layers, openCodeLayer{model: strings.TrimSpace(parseJSONCModelKey([]byte(content))), certain: true})
	}
	for _, dir := range openCodeManagedConfigDirs() {
		layers = append(layers, openCodeDirLayer(dir, true, "opencode.json", "opencode.jsonc"))
	}

	for i := len(layers) - 1; i >= 0; i-- {
		if layers[i].unknown {
			return ""
		}
		if layers[i].model == "" {
			continue
		}
		if !layers[i].certain {
			return ""
		}
		return layers[i].model
	}
	return ""
}

// openCodeProjectConfigUnknown reports whether project config may set the
// model for a launched session without AO being able to see it.
func openCodeProjectConfigUnknown(major int, workingDir string) bool {
	if major >= 2 {
		return true
	}
	if workingDir == "" {
		return false
	}
	if _, _, ok := gitTopLevel(workingDir); !ok {
		return true
	}
	return repoMayHoldConfig(workingDir, "opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc")
}

// openCodeDirLayer reads the named config files in one directory. The order in
// which OpenCode merges sibling .json and .jsonc files is not documented, so
// siblings that disagree make the layer uncertain.
func openCodeDirLayer(dir string, certain bool, names ...string) openCodeLayer {
	layer := openCodeLayer{certain: certain}
	for _, name := range names {
		model := openCodeFileLayer(filepath.Join(dir, name), certain).model
		if model == "" {
			continue
		}
		if layer.model != "" && !strings.EqualFold(layer.model, model) {
			layer.certain = false
		}
		layer.model = model
	}
	return layer
}

func openCodeFileLayer(path string, certain bool) openCodeLayer {
	raw, err := readModelConfig(path)
	if err != nil {
		return openCodeLayer{certain: certain}
	}
	return openCodeLayer{model: strings.TrimSpace(parseJSONCModelKey(raw)), certain: certain}
}

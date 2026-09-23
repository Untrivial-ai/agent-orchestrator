package goose

import (
	"context"
	"os"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ResolveBinary performs a fresh, identity-validated resolution for the
// plugin. It is the normal readiness/explicit-refresh path, not the cached
// startup presence shortcut.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	binary, err := ResolveGooseBinary(ctx)
	if err != nil {
		p.clearGooseBinary()
		return "", err
	}
	if err := ctx.Err(); err != nil {
		p.clearGooseBinary()
		return "", err
	}
	p.cacheGooseBinary(binary)
	return binary, nil
}

// ResolveBinaryPresence is the process-free startup check. A cached
// identity-confirmed path is considered installed; a name-only match is
// deliberately reported as identity-unknown so startup does not claim
// Pressly's goose.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cached := p.cachedGooseBinary(); cached != "" {
		return cached, nil
	}

	presenceSpec := gooseBinarySpec
	presenceSpec.ValidateIdentity = nil
	if _, err := binaryutil.ResolveBinary(ctx, presenceSpec); err != nil {
		return "", err
	}
	return "", ports.ErrAgentBinaryIdentityUnknown
}

func (p *Plugin) cacheGooseBinary(binary string) {
	info, err := os.Stat(binary)
	if err != nil {
		return
	}
	p.binaryMu.Lock()
	p.resolvedBinary = binary
	p.resolvedBinaryInfo = info
	p.binaryMu.Unlock()
}

func (p *Plugin) clearGooseBinary() {
	p.binaryMu.Lock()
	p.resolvedBinary = ""
	p.resolvedBinaryInfo = nil
	p.binaryMu.Unlock()
}

// cachedGooseBinary returns a launch cache entry. Entries written by the
// resolver are rechecked against their local file snapshot so replacing a
// cached path with another executable cannot make startup trust the old
// identity.
func (p *Plugin) cachedGooseBinary() string {
	p.binaryMu.Lock()
	cached := p.resolvedBinary
	info := p.resolvedBinaryInfo
	p.binaryMu.Unlock()
	if cached == "" {
		return ""
	}
	if info != nil && !cachedGooseBinaryStillValid(cached, info) {
		p.binaryMu.Lock()
		if p.resolvedBinary == cached {
			p.resolvedBinary = ""
			p.resolvedBinaryInfo = nil
		}
		p.binaryMu.Unlock()
		return ""
	}
	return cached
}

func cachedGooseBinaryStillValid(path string, cachedInfo os.FileInfo) bool {
	if runtime.GOOS == "windows" && !isNativelyLaunchableWindowsGoose(path) {
		return false
	}
	currentInfo, err := os.Stat(path)
	if err != nil || currentInfo.IsDir() || cachedInfo == nil || !hookutil.IsExecutableFile(path) {
		return false
	}
	return os.SameFile(cachedInfo, currentInfo) &&
		cachedInfo.Size() == currentInfo.Size() &&
		cachedInfo.Mode().Perm() == currentInfo.Mode().Perm() &&
		cachedInfo.ModTime().Equal(currentInfo.ModTime())
}

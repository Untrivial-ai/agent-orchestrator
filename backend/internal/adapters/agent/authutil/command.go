package authutil

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Dependencies keeps external effects instance-scoped. Zero fields use the
// local environment, regular filesystem, clock, and bounded command runner.
// Tests should supply Getenv and Run before invoking credential discovery.
type Dependencies struct {
	Getenv   func(string) string
	Lstat    func(string) (os.FileInfo, error)
	ReadFile func(string) ([]byte, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
	Now      func() time.Time
	GOOS     string
	Timeout  time.Duration
	// Cloud loaders are optional and must honor context cancellation. With no
	// loader installed, metadata/refresh-dependent chains remain unknown.
	LoadAWS       func(context.Context) (CloudCredential, error)
	LoadGoogleADC func(context.Context) (CloudCredential, error)
	LoadAzure     func(context.Context) (CloudCredential, error)
}

func (d Dependencies) getenv(name string) string {
	if d.Getenv != nil {
		return d.Getenv(name)
	}
	return os.Getenv(name)
}

func (d Dependencies) lstat(path string) (os.FileInfo, error) {
	if d.Lstat != nil {
		return d.Lstat(path)
	}
	return os.Lstat(path)
}

func (d Dependencies) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Dependencies) goos() string {
	if d.GOOS != "" {
		return d.GOOS
	}
	return runtime.GOOS
}

func (d Dependencies) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 3 * time.Second
}

// RunCommand executes a bounded argument vector without a shell. Output is
// sensitive and intended only for a schema-specific parser. Errors never wrap
// command errors, arguments, stdout, or stderr.
func RunCommand(ctx context.Context, d Dependencies, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	run := d.Run
	if run == nil {
		run = runBounded
	}
	out, err := run(probeCtx, name, args...)
	if probeCtx.Err() != nil {
		return nil, probeCtx.Err()
	}
	if err != nil {
		return nil, errors.New("credential command failed")
	}
	if len(out) > MaxFileSize {
		return nil, errors.New("credential command output exceeds limit")
	}
	return out, nil
}

func runBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, name, args...)
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if output.exceeded {
		return nil, errors.New("credential command output exceeds limit")
	}
	return output.data, err
}

type boundedOutput struct {
	data     []byte
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := MaxFileSize - len(b.data)
	if len(p) > remaining {
		b.exceeded = true
		b.data = append(b.data, p[:remaining]...)
	} else {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

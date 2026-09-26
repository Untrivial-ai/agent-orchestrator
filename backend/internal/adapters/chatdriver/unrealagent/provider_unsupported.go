//go:build !darwin && !linux

package unrealagent

import (
	"context"
	"fmt"
	"io"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// RunProvider keeps the internal command linkable on platforms not supported
// by Unreal Agent's process primitives.
func RunProvider(context.Context, string, io.Reader, io.Writer) error {
	return fmt.Errorf("%w: Unreal Agent supports macOS and Linux", ports.ErrChatDriverUnavailable)
}

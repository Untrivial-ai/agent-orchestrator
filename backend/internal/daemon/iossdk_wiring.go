package daemon

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/iossimulator"
)

// IOSDevice shares the toolchain boundary while keeping one simulator manager
// per AO session. The default manager remains for old clients that do not send
// a session id.
func IOSDevice() *controllers.IOSDeviceController {
	return &controllers.IOSDeviceController{Simulator: iossimulator.New(), Sessions: iossimulator.NewSessionRegistry(nil)}
}

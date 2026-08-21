package daemon

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

// iosDeviceController is the singleton IOSDeviceController wired into the
// daemon's API surface. It is constructed once at boot and never recreated.
// The controller delegates to iossdk.DetectXcode(), a pure Go function with
// no external runtime dependencies, so it is safe to construct at boot even
// when no Simulator is running.
var iosDeviceController = &controllers.IOSDeviceController{}

// IOSDevice returns the wired IOSDeviceController for use in APIDeps.
// This mirrors the mobile bridge pattern where Mobile *controllers.MobileController
// is a field in httpd.APIDeps.
func IOSDevice() *controllers.IOSDeviceController {
	return iosDeviceController
}

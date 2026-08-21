package controllers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/iossdk"
)

// IOSDeviceController exposes the /ios-device/toolchain surface: status,
// re-check, and (if B0 confirms viability) runtime/runtime image fetch.
//
// The controller does not require a running Simulator; it delegates to
// iossdk.DetectXcode(), a pure Go function that shells out to xcode-select -p.
// This means the controller is safe to construct at daemon boot and the
// renderer can always poll the status endpoint regardless of Simulator state.
type IOSDeviceController struct{}

// statusResponse builds the wire DTO from a detection result, folding the
// static guidance payload into flat fields when Xcode is absent.
func statusResponse(result *iossdk.DetectionResult) StatusResponse {
	res := StatusResponse{
		XcodeDetected:           result.XcodeDetected,
		CLTOnly:                 result.CLTOnly,
		SimctlAvailable:         result.XcodeDetected, // simctl ships with full Xcode
		DefaultRuntimeAvailable: result.XcodeDetected,
	}
	if !result.XcodeDetected {
		res.GuidanceAppStoreURL = iossdk.DefaultGuidance.AppStoreURL
		res.GuidanceDeveloperURL = iossdk.DefaultGuidance.DeveloperURL
		res.GuidanceWhyMissing = iossdk.DefaultGuidance.WhyMissing
	}
	return res
}

// Status returns the current Xcode / iOS Simulator toolchain status.
//
// On macOS: evaluates xcode-select -p and returns detection results.
// On Windows/Linux: all fields are false / empty, with a message that this
// feature is macOS-only.
func (c *IOSDeviceController) Status(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, statusResponse(iossdk.DetectXcode()))
}

// Recheck triggers a fresh Xcode detection and returns the updated status.
func (c *IOSDeviceController) Recheck(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, statusResponse(iossdk.DetectXcode()))
}

// fetchRuntimePayload is the body of POST /api/v1/ios-device/toolchain/fetch-runtime.
type fetchRuntimePayload struct {
	AcceptAppleID bool `json:"acceptAppleID"`
}

// FetchRuntime attempts to acquire the iOS Simulator runtime image.
// Currently this is a no-op because Apple does not permit redistribution of
// Xcode or its iOS runtime outside the Xcode IDE, and there is no
// non-interactive download API. This endpoint records intent and returns
// guidance.
func (c *IOSDeviceController) FetchRuntime(w http.ResponseWriter, r *http.Request) {
	var req fetchRuntimePayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}

	res := FetchRuntimeResponse{
		Success: false,
		Message: "Xcode cannot be auto-downloaded. Apple's redistribution license forbids it, and Xcode is a multi-GB App Store item behind an Apple ID. Install Xcode manually from https://developer.apple.com/xcode/ or download from your Apple Developer account.",
	}

	envelope.WriteJSON(w, http.StatusOK, res)
}

// Register mounts the /api/v1/ios-device/* routes on the supplied router.
// The router already has the /api/v1 prefix, so paths here omit it.
func (c *IOSDeviceController) Register(r chi.Router) {
	r.Get("/ios-device/toolchain/status", c.Status)
	r.Post("/ios-device/toolchain/recheck", c.Recheck)
	r.Post("/ios-device/toolchain/fetch-runtime", c.FetchRuntime)
}

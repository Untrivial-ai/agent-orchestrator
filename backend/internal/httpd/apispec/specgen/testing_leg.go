package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func testingLegOperations() []operation {
	errors := []respUnit{{http.StatusBadRequest, envelope.APIError{}}, {http.StatusForbidden, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusUnsupportedMediaType, envelope.APIError{}}, {http.StatusInternalServerError, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}}
	ops := make([]operation, 0, 10)
	ops = append(ops,
		operation{method: http.MethodPost, path: "/api/v1/testing/sessions/{sessionId}/legs/{leg}/start", id: "startTestingLeg", tag: "testing", summary: "Clean up the previous target and start a pinned revision in the same worker", pathParams: []any{controllers.TestingWorkerIDParam{}, controllers.TestingLegParam{}}, reqBody: controllers.TestingLegStartRequest{}, resps: append([]respUnit{{http.StatusCreated, controllers.TestingLegStartResponse{}}}, errors...)},
		operation{method: http.MethodPost, path: "/api/v1/testing/comparisons", id: "startTestingComparison", tag: "testing", summary: "Attach one worker to pinned base and head runs before launching either target", reqBody: controllers.StartTestingComparisonRequest{}, resps: append([]respUnit{{http.StatusCreated, controllers.TestingAttemptStartResponse{}}}, errors...)},
	)
	for _, tool := range []struct {
		name, id string
		input    any
	}{
		{"screenshot", "testingSessionScreenshot", controllers.TestingScreenshotCall{}},
		{"observe", "testingSessionObserve", controllers.TestingObserveCall{}},
		{"click", "testingSessionClick", controllers.TestingClickCall{}},
		{"type", "testingSessionType", controllers.TestingTypeCall{}},
		{"key", "testingSessionKey", controllers.TestingKeyCall{}},
		{"read_target_logs", "testingSessionReadTargetLogs", controllers.TestingLogsCall{}},
		{"target_daemon_query", "testingSessionTargetDaemonQuery", controllers.TestingQueryCall{}},
		{"submit_report", "testingSessionSubmitReport", controllers.TestingReportCall{}},
	} {
		ops = append(ops, operation{method: http.MethodPost, path: "/api/v1/testing/sessions/{sessionId}/tools/" + tool.name, id: tool.id, tag: "testing", summary: "Resolve the current owned target and call " + tool.name, pathParams: []any{controllers.TestingWorkerIDParam{}, controllers.TestingCapabilityHeader{}}, reqBody: tool.input, resps: append([]respUnit{{http.StatusOK, controllers.TestingToolResponse{}}}, errors...)})
	}
	return ops
}

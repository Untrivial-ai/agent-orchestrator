package codexappserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
)

// Does the driver still agree with the provider about the protocol?
//
// Every method AO sends or reads is named here. Two failure modes this catches
// that nothing else does:
//
//   - AO calls a method the installed provider no longer declares, which arrives
//     as a generic -32601 at runtime and looks like a broken session.
//   - AO reads a payload field that has been renamed, which unmarshals to a zero
//     value and looks like "the agent produced no output".
//
// The second is the one that actually bit: a thread-vs-turn `sandboxPolicy` shape
// mismatch cost an afternoon and would have been a compile error against
// generated types.

// driverMethods is every method this driver depends on, with the generated type
// whose fields it reads. Adding a method to the driver without adding it here is
// the mistake this table exists to make visible.
var driverMethods = []struct {
	method string
	// payload names the generated type carrying this method's params, empty when
	// the driver only sends the method and reads nothing back.
	payload any
}{
	// Outbound: session lifecycle.
	{codexproto.MethodInitialize, &codexproto.InitializeParams{}},
	{codexproto.MethodThreadStart, &codexproto.ThreadStartParams{}},
	{codexproto.MethodThreadResume, &codexproto.ThreadResumeParams{}},
	{codexproto.MethodThreadUnsubscribe, &codexproto.ThreadUnsubscribeParams{}},

	// Outbound: turns.
	{codexproto.MethodTurnStart, &codexproto.TurnStartParams{}},
	{codexproto.MethodTurnInterrupt, &codexproto.TurnInterruptParams{}},

	// Outbound: capabilities the driver advertises.
	{codexproto.MethodModelList, &codexproto.ModelListParams{}},
	{codexproto.MethodThreadCompactStart, &codexproto.ThreadCompactStartParams{}},
	// Takes no params; the generated table records Params: "".
	{codexproto.MethodAccountRateLimitsRead, nil},
	{codexproto.MethodSkillsList, &codexproto.SkillsListParams{}},
	// Also takes no params. The reload is the only recovery for a tool server that
	// failed to start, and the inventory read after it is what reports the outcome.
	{codexproto.MethodConfigMcpServerReload, nil},
	{codexproto.MethodMcpServerStatusList, &codexproto.ListMcpServerStatusParams{}},

	// Inbound: the notifications the timeline is built from.
	// NOT thread/started: AO reads the thread id from the thread/start RESPONSE, so
	// the notification carries nothing it does not already have. Listing it here
	// would make this table's claim — every method the driver depends on — false.
	{codexproto.MethodTurnStarted, nil},
	{codexproto.MethodTurnCompleted, nil},
	{codexproto.MethodItemStarted, nil},
	{codexproto.MethodItemCompleted, nil},
	{codexproto.MethodItemAgentMessageDelta, nil},
	{codexproto.MethodItemCommandExecutionOutputDelta, nil},
	{codexproto.MethodTurnDiffUpdated, nil},
	{codexproto.MethodThreadTokenUsageUpdated, nil},
	{codexproto.MethodAccountRateLimitsUpdated, nil},

	// Inbound: streamed reasoning. VERIFIED live for the summary pair; the raw text
	// delta was never observed and is read for a build that exposes it.
	{codexproto.MethodItemReasoningSummaryTextDelta, &codexproto.ReasoningSummaryTextDeltaNotification{}},
	{codexproto.MethodItemReasoningSummaryPartAdded, &codexproto.ReasoningSummaryPartAddedNotification{}},
	{codexproto.MethodItemReasoningTextDelta, &codexproto.ReasoningTextDeltaNotification{}},

	// Inbound: the plan. The turn-scoped notification is what 0.146.0 sends; the
	// item delta was never observed.
	{codexproto.MethodTurnPlanUpdated, &codexproto.TurnPlanUpdatedNotification{}},
	{codexproto.MethodItemPlanDelta, &codexproto.PlanDeltaNotification{}},

	// Inbound: per-item file changes. Neither was observed on 0.146.0, which sends
	// the whole changes array on item/started; both are read so a build that streams
	// a patch is not dropped.
	{codexproto.MethodItemFileChangePatchUpdated, &codexproto.FileChangePatchUpdatedNotification{}},
	{codexproto.MethodItemFileChangeOutputDelta, &codexproto.FileChangeOutputDeltaNotification{}},

	// Inbound: MCP. Server startup state is VERIFIED live; tool-call progress was
	// never observed even with an MCP server sending notifications/progress.
	{codexproto.MethodMcpServerStartupStatusUpdated, &codexproto.McpServerStatusUpdatedNotification{}},
	{codexproto.MethodItemMcpToolCallProgress, &codexproto.McpToolCallProgressNotification{}},

	// Inbound: what the agent typed into a running command's terminal. VERIFIED live.
	{codexproto.MethodItemCommandExecutionTerminalInteraction, &codexproto.TerminalInteractionNotification{}},

	// Inbound: approval decisions the provider made on the user's behalf. VERIFIED
	// live with approvalsReviewer=auto_review.
	{codexproto.MethodItemAutoApprovalReviewStarted, &codexproto.ItemGuardianApprovalReviewStartedNotification{}},
	{codexproto.MethodItemAutoApprovalReviewCompleted, &codexproto.ItemGuardianApprovalReviewCompletedNotification{}},

	// Inbound: the model was swapped, the account changed. Neither is provokable
	// from a test.
	{codexproto.MethodModelRerouted, &codexproto.ModelReroutedNotification{}},
	{codexproto.MethodAccountUpdated, &codexproto.AccountUpdatedNotification{}},

	// Inbound: thread lifecycle. Status and archive/unarchive are VERIFIED live;
	// thread/closed was never observed.
	{codexproto.MethodThreadStatusChanged, &codexproto.ThreadStatusChangedNotification{}},
	{codexproto.MethodThreadArchived, &codexproto.ThreadArchivedNotification{}},
	{codexproto.MethodThreadUnarchived, &codexproto.ThreadUnarchivedNotification{}},
	{codexproto.MethodThreadClosed, &codexproto.ThreadClosedNotification{}},

	// Inbound request: the provider asking for ChatGPT credentials AO does not hold.
	// Answered with a refusal and surfaced to the user, never fabricated.
	{codexproto.MethodAccountChatgptAuthTokensRefresh, &codexproto.ChatgptAuthTokensRefreshParams{}},
	// Inbound request: run a tool AO never declared. Refused explicitly.
	{codexproto.MethodItemToolCall, &codexproto.DynamicToolCallParams{}},

	// Inbound: approvals. All three kinds the provider declares, which is the
	// whole set for this build — there is no fourth to miss.
	{codexproto.MethodItemCommandExecutionRequestApproval, nil},
	{codexproto.MethodItemFileChangeRequestApproval, nil},
	{codexproto.MethodItemPermissionsRequestApproval, nil},
	{codexproto.MethodItemToolRequestUserInput, nil},
}

func TestEveryMethodTheDriverUsesIsDeclaredByTheProvider(t *testing.T) {
	for _, m := range driverMethods {
		if !codexproto.Declares(m.method) {
			t.Errorf("driver uses %q, which the generated protocol (%s) does not declare",
				m.method, codexproto.ProviderVersion)
		}
	}
}

// The three approval kinds are load-bearing: a fourth kind arriving unhandled
// would leave a turn waiting on a decision no client is ever offered, which
// presents as a session that has silently stopped.
func TestApprovalKindsAreExhaustive(t *testing.T) {
	var approvals []string
	for _, m := range codexproto.Methods {
		if strings.HasSuffix(m.Name, "/requestApproval") {
			approvals = append(approvals, m.Name)
		}
	}
	handled := map[string]bool{
		codexproto.MethodItemCommandExecutionRequestApproval: true,
		codexproto.MethodItemFileChangeRequestApproval:       true,
		codexproto.MethodItemPermissionsRequestApproval:      true,
	}
	for _, name := range approvals {
		if !handled[name] {
			t.Errorf("provider declares approval %q, which the driver does not handle: "+
				"a turn blocked on it would wait forever", name)
		}
	}
	if len(approvals) != len(handled) {
		t.Errorf("provider declares %d approval kinds, driver handles %d", len(approvals), len(handled))
	}
}

// The generated file must describe the provider on this machine. A stale
// checkout is how a driver starts sending methods that no longer exist.
//
// Skipped rather than failed when the provider is absent, so the suite still runs
// on a machine without codex installed; the live e2e covers the other direction.
func TestGeneratedProtocolMatchesTheInstalledProvider(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed; nothing to compare the generated protocol against")
	}

	dir := t.TempDir()
	out, err := exec.Command(bin, "app-server", "generate-json-schema", "--out", dir).CombinedOutput()
	if err != nil {
		t.Skipf("provider declined to emit its schema (%v): %s", err, out)
	}

	live := methodsFromSchema(t, dir)
	generated := map[string]bool{}
	for _, m := range codexproto.Methods {
		generated[m.Name] = true
	}

	var missing, extra []string
	for name := range live {
		if !generated[name] {
			missing = append(missing, name)
		}
	}
	for name := range generated {
		if !live[name] {
			extra = append(extra, name)
		}
	}

	// New provider methods are not a failure on their own — AO does not have to use
	// everything — but they are worth surfacing, because they are where the next
	// feature comes from.
	if len(missing) > 0 {
		t.Logf("installed provider declares %d method(s) the generated file lacks; "+
			"run `go generate ./internal/adapters/chatdriver/codexappserver/...` to pick them up: %v",
			len(missing), truncate(missing))
	}
	// A method AO's generated file has and the provider does not is the dangerous
	// direction: any call to it fails at runtime.
	if len(extra) > 0 {
		t.Errorf("generated protocol declares %d method(s) the installed provider does not: %v",
			len(extra), truncate(extra))
	}
}

func methodsFromSchema(t *testing.T, dir string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	for name := range methodDirectionsFromSchema(t, dir) {
		found[name] = true
	}
	return found
}

// methodDirectionsFromSchema maps each method the installed provider declares onto
// the direction that declares it. Direction is what says whether a client can
// initiate something or only observe it.
func methodDirectionsFromSchema(t *testing.T, dir string) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, file := range []string{
		"ClientRequest.json", "ClientNotification.json",
		"ServerRequest.json", "ServerNotification.json",
	} {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			continue
		}
		var doc struct {
			OneOf []struct {
				Properties struct {
					Method struct {
						Enum []string `json:"enum"`
					} `json:"method"`
				} `json:"properties"`
			} `json:"oneOf"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		direction := strings.TrimSuffix(file, ".json")
		for _, arm := range doc.OneOf {
			for _, name := range arm.Properties.Method.Enum {
				found[name] = direction
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("no methods parsed out of %s", dir)
	}
	return found
}

func truncate(names []string) []string {
	const limit = 12
	if len(names) <= limit {
		return names
	}
	return append(names[:limit:limit], "…")
}

// Voice is deferred, and this is what makes deferring safe rather than forgetful.
//
// The provider declares eight `thread/realtime/*` methods — audio deltas, SDP,
// live transcript — but every one is a ServerNotification. There is NO
// ClientRequest that starts a realtime session, so no client can reach any of
// them, and the supporting types (ThreadRealtimeStartTransport,
// RealtimeOutputModality, RealtimeVoicesList, ThreadRealtimeInitialItem) are
// referenced by nothing at all. The feature is half-landed upstream with its door
// not yet published.
//
// So AO handling none of it costs nothing today: there is no reachable behaviour
// to be missing. What WOULD cost something is discovering the entry point exists
// months after it shipped, which is what happens when "check again later" lives in
// someone's memory instead of in the suite.
//
// When this test fails, voice became buildable. The failure is the feature
// request: wire the start request, then the eight notifications (their generated
// types already exist), then the product work — capture, playback, transport, and
// a transcript surface.
func TestRealtimeStaysUnreachableUntilTheProviderPublishesAnEntryPoint(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed; nothing to check the realtime surface against")
	}

	dir := t.TempDir()
	if out, err := exec.Command(bin, "app-server", "generate-json-schema", "--out", dir).CombinedOutput(); err != nil {
		t.Skipf("provider declined to emit its schema (%v): %s", err, out)
	}

	var startable []string
	for method, direction := range methodDirectionsFromSchema(t, dir) {
		if !strings.Contains(method, "realtime") {
			continue
		}
		// A ClientRequest or ClientNotification is something AO can send. Either one
		// means a session can now be initiated.
		if strings.HasPrefix(direction, "Client") {
			startable = append(startable, method+" ("+direction+")")
		}
	}
	sort.Strings(startable)

	if len(startable) > 0 {
		t.Errorf("the provider now lets a client start a realtime session: %v\n"+
			"Voice is buildable — see this test's comment for what that entails. "+
			"Regenerate the protocol and decide whether to take it on; if not, "+
			"record the decision here so this stops reporting it.", startable)
	}
}

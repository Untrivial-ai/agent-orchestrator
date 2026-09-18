package agentauth

import "strings"

// plans is the code-reviewed authentication allowlist in stable Harness
// settings order. Commands must be added here, never supplied by clients.
var plans = []Plan{
	plan("opencode", ActionLogin, "Log in to OpenCode", []string{"opencode", "auth", "login"}, "Native provider chooser", "https://github.com/anomalyco/opencode"),
}

var planByAgentID = func() map[string]Plan {
	out := make(map[string]Plan, len(plans))
	for _, plan := range plans {
		out[plan.AgentID] = plan
	}
	return out
}()

func plan(agentID string, action Action, title string, command []string, guidance, docs string) Plan {
	return Plan{
		AgentID:          agentID,
		Action:           action,
		LaunchMode:       LaunchTerminal,
		DisplayCommand:   strings.Join(command, " "),
		Guidance:         guidance,
		DocumentationURL: docs,
		command:          command,
		title:            title,
	}
}

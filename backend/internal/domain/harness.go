package domain

// AgentHarness identifies which agent CLI/runtime a session drives.
type AgentHarness string

// Supported agent harnesses.
const (
	HarnessClaudeCode  AgentHarness = "claude-code"
	HarnessCodex       AgentHarness = "codex"
	HarnessAider       AgentHarness = "aider"
	HarnessOpenCode    AgentHarness = "opencode"
	HarnessOpenCodeV2  AgentHarness = "opencode-v2"
	HarnessGrok        AgentHarness = "grok"
	HarnessDroid       AgentHarness = "droid"
	HarnessAmp         AgentHarness = "amp"
	HarnessAgy         AgentHarness = "agy"
	HarnessCrush       AgentHarness = "crush"
	HarnessCursor      AgentHarness = "cursor"
	HarnessQwen        AgentHarness = "qwen"
	HarnessGemini      AgentHarness = "gemini"
	HarnessCopilot     AgentHarness = "copilot"
	HarnessGoose       AgentHarness = "goose"
	HarnessAuggie      AgentHarness = "auggie"
	HarnessContinue    AgentHarness = "continue"
	HarnessDevin       AgentHarness = "devin"
	HarnessCline       AgentHarness = "cline"
	HarnessKimi        AgentHarness = "kimi"
	HarnessMuse        AgentHarness = "muse"
	HarnessKiro        AgentHarness = "kiro"
	HarnessKilocode    AgentHarness = "kilocode"
	HarnessVibe        AgentHarness = "vibe"
	HarnessPi          AgentHarness = "pi"
	HarnessKimchi      AgentHarness = "kimchi"
	HarnessPrimeAgent  AgentHarness = "prime-agent"
	HarnessAutohand    AgentHarness = "autohand"
	HarnessOMP         AgentHarness = "omp"
	HarnessFX          AgentHarness = "fx"
	HarnessUnreal      AgentHarness = "unreal-agent"
	HarnessMiMoCode    AgentHarness = "mimo-code"
	HarnessDeepSeek    AgentHarness = "deepseek-harness"
	HarnessCommandCode AgentHarness = "command-code"
	// HarnessFake is retained for existing test fixtures and historical session
	// rows, but is not user-selectable.
	HarnessFake AgentHarness = "fake"
)

// AllHarnesses lists every supported harness. It is the canonical set used to
// validate user-supplied harness names (e.g. per-project role overrides).
var AllHarnesses = []AgentHarness{
	HarnessClaudeCode, HarnessCodex, HarnessAider, HarnessOpenCode, HarnessOpenCodeV2, HarnessGrok,
	HarnessDroid, HarnessAmp, HarnessAgy, HarnessCrush, HarnessCursor, HarnessQwen, HarnessGemini,
	HarnessCopilot, HarnessGoose, HarnessAuggie, HarnessContinue, HarnessDevin,
	HarnessCline, HarnessKimi, HarnessMuse, HarnessKiro, HarnessKilocode, HarnessVibe, HarnessPi,
	HarnessKimchi, HarnessPrimeAgent, HarnessAutohand,
	HarnessOMP, HarnessFX, HarnessUnreal, HarnessMiMoCode, HarnessDeepSeek, HarnessCommandCode,
}

// IsKnown reports whether h is one of the supported harnesses.
func (h AgentHarness) IsKnown() bool {
	for _, k := range AllHarnesses {
		if h == k {
			return true
		}
	}
	return false
}

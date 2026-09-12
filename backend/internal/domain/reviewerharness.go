package domain

// ReviewerHarness identifies a code-review agent. It is a separate vocabulary
// from AgentHarness on purpose: a reviewer-only tool (e.g. the Greptile CLI)
// must not become a valid worker, and a worker harness does not automatically
// become a valid reviewer. The two sets are maintained independently and only
// happen to share ids where the same tool serves both roles.
type ReviewerHarness string

// Supported reviewer harnesses. Add a reviewer-only tool here (and register its
// adapter) without widening the worker AgentHarness set.
const (
	ReviewerClaudeCode ReviewerHarness = "claude-code"
	ReviewerCodex      ReviewerHarness = "codex"
	ReviewerCopilot    ReviewerHarness = "copilot"
	ReviewerCursor     ReviewerHarness = "cursor"
	ReviewerKiloCode   ReviewerHarness = "kilocode"
	ReviewerKimchi     ReviewerHarness = "kimchi"
	ReviewerOpenCode   ReviewerHarness = "opencode"
	ReviewerKiro       ReviewerHarness = "kiro"
	ReviewerPi         ReviewerHarness = "pi"
	ReviewerAgy        ReviewerHarness = "agy"
	ReviewerDevin      ReviewerHarness = "devin"
	ReviewerDroid      ReviewerHarness = "droid"
	ReviewerKimi       ReviewerHarness = "kimi"
	ReviewerMuse       ReviewerHarness = "muse"
	ReviewerAmp        ReviewerHarness = "amp"
	ReviewerAider      ReviewerHarness = "aider"
	ReviewerGrok       ReviewerHarness = "grok"
	ReviewerCrush      ReviewerHarness = "crush"
	ReviewerAuggie     ReviewerHarness = "auggie"
	ReviewerCline      ReviewerHarness = "cline"
	ReviewerAutohand   ReviewerHarness = "autohand"
)

// AllReviewerHarnesses is the canonical set used to validate a configured
// reviewer harness.
var AllReviewerHarnesses = []ReviewerHarness{
	ReviewerClaudeCode,
	ReviewerCodex,
	ReviewerCopilot,
	ReviewerCursor,
	ReviewerKiloCode,
	ReviewerKimchi,
	ReviewerOpenCode,
	ReviewerKiro,
	ReviewerPi,
	ReviewerAgy,
	ReviewerDevin,
	ReviewerDroid,
	ReviewerKimi,
	ReviewerMuse,
	ReviewerAmp,
	ReviewerAider,
	ReviewerGrok,
	ReviewerCrush,
	ReviewerAuggie,
	ReviewerCline,
	ReviewerAutohand,
}

// IsKnown reports whether h is one of the supported reviewer harnesses.
func (h ReviewerHarness) IsKnown() bool {
	for _, k := range AllReviewerHarnesses {
		if h == k {
			return true
		}
	}
	return false
}

// Label returns the human-readable display name for this reviewer harness.
func (h ReviewerHarness) Label() string {
	switch h {
	case ReviewerClaudeCode:
		return "Claude Code"
	case ReviewerCodex:
		return "Codex"
	case ReviewerCopilot:
		return "GitHub Copilot"
	case ReviewerCursor:
		return "Cursor"
	case ReviewerKiloCode:
		return "Kilo Code"
	case ReviewerKimchi:
		return "Kimchi"
	case ReviewerOpenCode:
		return "OpenCode"
	case ReviewerKiro:
		return "Kiro"
	case ReviewerPi:
		return "Pi"
	case ReviewerQwen:
		return "Qwen"
	case ReviewerAgy:
		return "AGY"
	case ReviewerContinue:
		return "Continue"
	case ReviewerGoose:
		return "Goose"
	case ReviewerVibe:
		return "Vibe"
	case ReviewerDevin:
		return "Devin"
	case ReviewerDroid:
		return "Droid"
	case ReviewerKimi:
		return "Kimi"
	case ReviewerMuse:
		return "Muse"
	case ReviewerAmp:
		return "Amp"
	case ReviewerAider:
		return "Aider"
	case ReviewerGrok:
		return "Grok"
	case ReviewerCrush:
		return "Crush"
	case ReviewerAuggie:
		return "Auggie"
	case ReviewerCline:
		return "Cline"
	case ReviewerAutohand:
		return "Autohand"
	default:
		return string(h)
	}
}

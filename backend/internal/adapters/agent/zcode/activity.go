package zcode

import (
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeriveActivityState treats pre-submit and Stop callbacks as observations:
// later native hooks can veto submission or continue the same turn. Permission
// requests lack a stable in-flight tool boundary, so they mean waiting for input.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "pre-tool-use", "post-tool-use", "post-tool-use-failure":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityWaitingInput, true
	default:
		return "", false
	}
}

var terminalEscape = regexp.MustCompile(`\x1b(?:\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\][^\x07]*(?:\x07|\x1b\\))`)

// ContinuouslyDetectTerminalActivity reconciles blocked hooks and Stop vetoes
// against the actual current composer instead of assuming a hook is final.
func (*Plugin) ContinuouslyDetectTerminalActivity() bool { return true }

// DetectTerminalActivity uses the last native bordered composer. InputPane
// changes its placeholder while busy; InputActiveStatus adds an interrupt hint.
// Plain transcript mentions and absent/edited composers remain unknown.
func (*Plugin) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	plain := terminalEscape.ReplaceAllString(strings.ReplaceAll(output, "\r", ""), "")
	lines := strings.Split(plain, "\n")
	start := max(0, len(lines)-16)
	for i := len(lines) - 1; i >= start; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "╰") || !strings.HasSuffix(line, "╯") {
			continue
		}
		for j := i - 1; j >= start && i-j <= 10; j-- {
			header := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(header, "╭") || !strings.HasSuffix(header, "╮") {
				continue
			}
			if !strings.Contains(header, " Build ") && !strings.Contains(header, " Edit ") && !strings.Contains(header, " Plan ") && !strings.Contains(header, " Yolo ") && !strings.Contains(header, " Input ") && !strings.Contains(header, " 输入 ") {
				return "", false
			}
			for _, row := range lines[max(0, j-8):j] {
				if strings.Contains(row, "No available models.") || strings.Contains(row, "没有可用模型") {
					return domain.ActivityWaitingInput, true
				}
			}
			for _, row := range lines[i+1:] {
				if strings.Contains(row, "esc to interrupt") {
					return domain.ActivityActive, true
				}
			}
			for _, row := range lines[j+1 : i] {
				row = strings.TrimSpace(row)
				if !strings.HasPrefix(row, "│") || !strings.HasSuffix(row, "│") {
					continue
				}
				content := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(row, "│"), "│"))
				switch content {
				case "Type to queue input", "输入内容会排队":
					return domain.ActivityActive, true
				case "Type a prompt", "输入提示词":
					return domain.ActivityIdle, true
				}
			}
			return "", false
		}
		return "", false
	}
	return "", false
}

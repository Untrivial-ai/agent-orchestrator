package conpty

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const maxWindowsCommandLineUTF16 = 32767

func validateWindowsCommandLine(args []string) error {
	length := windowsCommandLineUTF16Len(args)
	if length <= maxWindowsCommandLineUTF16 {
		return nil
	}
	return fmt.Errorf("%w: escaped command line is %d UTF-16 code units; maximum is %d",
		ports.ErrRuntimeCommandLineTooLong, length, maxWindowsCommandLineUTF16)
}

func windowsCommandLineUTF16Len(args []string) int {
	return len(utf16.Encode([]rune(windowsCommandLine(args)))) + 1
}

func windowsCommandLine(args []string) string {
	var commandLine strings.Builder
	for i, arg := range args {
		if i > 0 {
			commandLine.WriteByte(' ')
		}
		appendWindowsEscapedArg(&commandLine, arg)
	}
	return commandLine.String()
}

func appendWindowsEscapedArg(dst *strings.Builder, arg string) {
	if arg == "" {
		dst.WriteString(`""`)
		return
	}

	needsBackslash := false
	hasSpace := false
	for i := 0; i < len(arg); i++ {
		switch arg[i] {
		case '"', '\\':
			needsBackslash = true
		case ' ', '\t':
			hasSpace = true
		}
	}

	if !needsBackslash && !hasSpace {
		dst.WriteString(arg)
		return
	}
	if !needsBackslash {
		dst.WriteByte('"')
		dst.WriteString(arg)
		dst.WriteByte('"')
		return
	}

	if hasSpace {
		dst.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(arg); i++ {
		char := arg[i]
		switch char {
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				dst.WriteByte('\\')
			}
			dst.WriteByte('\\')
		default:
			slashes = 0
		}
		dst.WriteByte(char)
	}
	if hasSpace {
		for ; slashes > 0; slashes-- {
			dst.WriteByte('\\')
		}
		dst.WriteByte('"')
	}
}

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUninstallHooksPreservesUnknownFieldsOnSurvivingUserHooksAndGroups(t *testing.T) {
	const seed = `{
  "hooks": {
    "%s": [
      {
        "groupConfig": {"mode": "serial", "limits": [1, 2]},
        "hooks": [
          {
            "type": "command",
            "command": "custom mixed-group hook",
            "timeout": 3,
            "async": true,
            "metadata": {"owner": "user", "labels": ["one", "two"]}
          },
          {
            "type": "command",
            "command": "%s",
            "timeout": 30
          }
        ]
      },
      {
        "matcher": "user-only",
        "groupConfig": {"mode": "parallel", "enabled": true},
        "hooks": [
          {
            "type": "command",
            "command": "custom untouched-group hook",
            "timeout": 7,
            "platform": {"windows": {"command": "custom.exe"}}
          }
        ]
      }
    ]
  }
}`
	const expected = `{
  "hooks": {
    "%s": [
      {
        "groupConfig": {"mode": "serial", "limits": [1, 2]},
        "hooks": [
          {
            "type": "command",
            "command": "custom mixed-group hook",
            "timeout": 3,
            "async": true,
            "metadata": {"owner": "user", "labels": ["one", "two"]}
          }
        ]
      },
      {
        "matcher": "user-only",
        "groupConfig": {"mode": "parallel", "enabled": true},
        "hooks": [
          {
            "type": "command",
            "command": "custom untouched-group hook",
            "timeout": 7,
            "platform": {"windows": {"command": "custom.exe"}}
          }
        ]
      }
    ]
  }
}`
	for _, tt := range []struct {
		event   string
		command string
	}{
		{event: "SessionStart", command: "ao hooks codex session-start"},
		{event: "UserPromptSubmit", command: "ao hooks codex user-prompt-submit"},
		{event: "PermissionRequest", command: "ao hooks codex permission-request"},
		{event: "Stop", command: "ao hooks codex stop"},
	} {
		t.Run(tt.event, func(t *testing.T) {
			workspace := t.TempDir()
			hooksPath := filepath.Join(workspace, codexHooksDirName, codexHooksFileName)
			if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hooksPath, []byte(fmt.Sprintf(seed, tt.event, tt.command)), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := (&Plugin{}).UninstallHooks(context.Background(), workspace); err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(hooksPath)
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal([]byte(fmt.Sprintf(expected, tt.event)), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("hooks after cleanup\nwant: %#v\n got: %#v", want, got)
			}
		})
	}
}

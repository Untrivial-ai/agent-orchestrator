package sqlite

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// Generation is checked separately in CI with the pinned npm run sqlc command.
// Keep this test offline so the normal storage suite also catches malformed
// YAML and missing boolean overrides before generated code masks the problem.
func TestSQLCBooleanOverrides(t *testing.T) {
	data, err := os.ReadFile("../../../sqlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		SQL []struct {
			Gen struct {
				Go struct {
					Out       string `yaml:"out"`
					Overrides []struct {
						Column string `yaml:"column"`
						GoType any    `yaml:"go_type"`
					} `yaml:"overrides"`
				} `yaml:"go"`
			} `yaml:"gen"`
		} `yaml:"sql"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse sqlc.yaml: %v", err)
	}
	for _, column := range []string{
		"agent_switch_failure_policy.enabled",
		"app_settings.cloud_offering",
	} {
		t.Run(column, func(t *testing.T) {
			matches := 0
			for _, query := range config.SQL {
				if query.Gen.Go.Out != "internal/storage/sqlite/gen" {
					continue
				}
				for _, override := range query.Gen.Go.Overrides {
					if override.Column != column {
						continue
					}
					matches++
					if goType, ok := override.GoType.(string); !ok || goType != "bool" {
						t.Errorf("go_type = %v, want bool", override.GoType)
					}
				}
			}
			if matches != 1 {
				t.Errorf("found %d overrides, want exactly one", matches)
			}
		})
	}
}

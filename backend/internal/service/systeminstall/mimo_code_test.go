package systeminstall

import (
	"reflect"
	"testing"
)

func TestMiMoCodeUsesOfficialNPMPackage(t *testing.T) {
	plan := newTestService("darwin", "npm").planAgent(TargetMiMoCode)
	if plan.Unsupported || plan.Method != "npm" {
		t.Fatalf("plan = %#v, want automatic npm install", plan)
	}
	want := []string{"npm", "install", "-g", "@mimo-ai/cli"}
	if !reflect.DeepEqual(plan.Command, want) {
		t.Fatalf("command = %#v, want %#v", plan.Command, want)
	}
	if plan.DocsURL != "https://github.com/XiaomiMiMo/MiMo-Code" {
		t.Fatalf("docs URL = %q", plan.DocsURL)
	}
}

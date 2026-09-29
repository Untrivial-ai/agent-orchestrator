package ports_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestMetadataKeyAgentSessionIDMatchesDomainJSONTag pins the hand-maintained
// invariant documented on ports.MetadataKeyAgentSessionID: a silent rename on
// either side would break session restore.
func TestMetadataKeyAgentSessionIDMatchesDomainJSONTag(t *testing.T) {
	field, ok := reflect.TypeOf(domain.SessionMetadata{}).FieldByName("AgentSessionID")
	if !ok {
		t.Fatalf("domain.SessionMetadata has no AgentSessionID field")
	}
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name != ports.MetadataKeyAgentSessionID {
		t.Fatalf("json tag %q != ports.MetadataKeyAgentSessionID %q", name, ports.MetadataKeyAgentSessionID)
	}
}

func TestMetadataKeyNativeTranscriptPathMatchesDomainJSONTag(t *testing.T) {
	field, ok := reflect.TypeOf(domain.SessionMetadata{}).FieldByName("NativeTranscriptPath")
	if !ok {
		t.Fatalf("domain.SessionMetadata has no NativeTranscriptPath field")
	}
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name != ports.MetadataKeyNativeTranscriptPath {
		t.Fatalf("json tag %q != ports.MetadataKeyNativeTranscriptPath %q", name, ports.MetadataKeyNativeTranscriptPath)
	}
}

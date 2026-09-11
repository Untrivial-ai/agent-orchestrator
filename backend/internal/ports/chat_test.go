package ports

import (
	"reflect"
	"testing"
)

func TestChatProviderFailureNormalizesProviderCopy(t *testing.T) {
	for _, tc := range []struct {
		name, title, detail                string
		wantTitle, wantDetail, wantMessage string
	}{
		{"title and detail", " Access denied ", " Choose a plan. ", "Access denied", "Choose a plan.", "Access denied\n\nChoose a plan."},
		{"detail only", "", "Connection closed", "Connection closed", "", "Connection closed"},
		{"duplicate detail", "Quota reached", " Quota reached ", "Quota reached", "", "Quota reached"},
		{"empty", "", "", "Provider error", "", "Provider error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := NewChatProviderFailure(tc.title, tc.detail, ChatProviderRecoveryReauthenticate)
			if failure.Title != tc.wantTitle || failure.Detail != tc.wantDetail || failure.Error() != tc.wantMessage {
				t.Fatalf("failure = %#v / %q", failure, failure.Error())
			}
			if failure.Recovery != ChatProviderRecoveryReauthenticate {
				t.Fatalf("recovery = %q", failure.Recovery)
			}
		})
	}
}

func TestMissingCapabilitiesForPermissions(t *testing.T) {
	caps := ChatCapabilities{
		ChatCapabilityStreaming: true,
		ChatCapabilityInterrupt: true,
		ChatCapabilityResume:    true,
	}

	if got := MissingCapabilitiesForPermissions(caps, PermissionModeDefault); !reflect.DeepEqual(
		got, []ChatCapability{ChatCapabilityApprovals},
	) {
		t.Fatalf("default missing = %v, want approvals", got)
	}
	if got := MissingCapabilitiesForPermissions(caps, PermissionModeBypassPermissions); len(got) != 0 {
		t.Fatalf("bypass missing = %v, want none", got)
	}

	delete(caps, ChatCapabilityInterrupt)
	if got := MissingCapabilitiesForPermissions(caps, PermissionModeBypassPermissions); !reflect.DeepEqual(
		got, []ChatCapability{ChatCapabilityInterrupt},
	) {
		t.Fatalf("bypass missing = %v, want interrupt", got)
	}
}

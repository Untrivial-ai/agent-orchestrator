package domain

import "testing"

func TestEffectiveAgentReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		installation AgentInstallationState
		auth         AgentAuthenticationState
		want         AgentEffectiveReadiness
	}{
		{"missing", AgentInstallationNotInstalled, AgentAuthenticationUnknown, AgentReadinessNotReady},
		{"installation unknown", AgentInstallationUnknown, AgentAuthenticationAuthorized, AgentReadinessUnknown},
		{"authorized", AgentInstallationInstalled, AgentAuthenticationAuthorized, AgentReadinessReady},
		{"auth not applicable", AgentInstallationInstalled, AgentAuthenticationNotApplicable, AgentReadinessReady},
		{"unauthorized", AgentInstallationInstalled, AgentAuthenticationUnauthorized, AgentReadinessNotReady},
		{"auth unknown", AgentInstallationInstalled, AgentAuthenticationUnknown, AgentReadinessUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := EffectiveAgentReadiness(tt.installation, tt.auth); got != tt.want {
				t.Fatalf("EffectiveAgentReadiness(%q, %q) = %q, want %q", tt.installation, tt.auth, got, tt.want)
			}
		})
	}
}

func TestAgentReadinessPurposeValidation(t *testing.T) {
	t.Parallel()
	if !AgentReadinessPurposeDisplay.Valid() || !AgentReadinessPurposeLaunch.Valid() {
		t.Fatal("documented readiness purposes must be valid")
	}
	if AgentReadinessPurpose("force").Valid() {
		t.Fatal("unknown readiness purpose must be invalid")
	}
}

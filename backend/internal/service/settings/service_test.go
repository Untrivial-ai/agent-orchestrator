package settings

import "testing"

// The cloud gate is the single most safety-critical expression in the offering:
// a false positive would surface cloud UI (and let a local-only build reach a
// control plane) without the user opting in. Pin the whole (forced x toggle x
// url) matrix so no future edit can flip a default open.
func TestOfferingCloudEnabled(t *testing.T) {
	const url = "https://cp.example.com"
	cases := []struct {
		name   string
		forced bool // AO_CLOUD_OFFERING env override
		toggle bool // persisted app_settings.cloud_offering
		cpURL  string
		want   bool
	}{
		{"default off (baked url, no toggle, no force)", false, false, url, false},
		{"toggle on with url", false, true, url, true},
		{"env force on with url", true, false, url, true},
		{"force and toggle on", true, true, url, true},
		{"toggle on but no control plane", false, true, "", false},
		{"force on but no control plane", true, false, "", false},
		{"all off, no url", false, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offering := Offering{CloudForced: tc.forced, CloudControlPlaneURL: tc.cpURL}
			got := offering.CloudEnabled(Snapshot{CloudOffering: tc.toggle})
			if got != tc.want {
				t.Errorf("CloudEnabled(forced=%v, toggle=%v, url=%q) = %v, want %v",
					tc.forced, tc.toggle, tc.cpURL, got, tc.want)
			}
		})
	}
}

// The baked control-plane URL must never enable cloud on its own: a stock local
// install has the URL set but neither the toggle nor the env override, and must
// resolve closed. This is the exact upgrade path for existing local users.
func TestOfferingBakedURLDoesNotEnableCloud(t *testing.T) {
	offering := Offering{
		CloudForced:          false,
		CloudControlPlaneURL: "https://staging-api.aoagents.dev",
	}
	if offering.CloudEnabled(Snapshot{CloudOffering: false}) {
		t.Fatal("cloud enabled with only the baked URL set; a local-only install must stay off")
	}
}

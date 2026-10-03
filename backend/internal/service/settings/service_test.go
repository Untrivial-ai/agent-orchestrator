package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type hibernationSettingsStore struct{ err error }

func (s *hibernationSettingsStore) GetAppSettings(context.Context) (Snapshot, error) {
	return Snapshot{DefaultSessionMode: domain.SessionModeChat}, s.err
}
func (*hibernationSettingsStore) SetDefaultSessionMode(context.Context, domain.SessionMode, time.Time) error {
	return nil
}
func (*hibernationSettingsStore) SetCloudOffering(context.Context, bool, time.Time) error { return nil }

func TestChatHibernationGateResetsOnDaemonBoot(t *testing.T) {
	ctx := context.Background()
	store := &hibernationSettingsStore{}
	first := New(store, nil, Offering{}, nil)
	if first.ChatHibernationEnabled() {
		t.Fatal("new daemon enabled chat hibernation")
	}
	snapshot, err := first.SetChatHibernationEnabled(ctx, true)
	if err != nil || !snapshot.ChatHibernationEnabled || !first.ChatHibernationEnabled() {
		t.Fatalf("enable chat hibernation: snapshot=%+v err=%v", snapshot, err)
	}
	second := New(store, nil, Offering{}, nil)
	snapshot, err = second.Get(ctx)
	if err != nil || snapshot.ChatHibernationEnabled || second.ChatHibernationEnabled() {
		t.Fatalf("restarted daemon retained chat hibernation: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestDisablingChatHibernationSurvivesSettingsReadFailure(t *testing.T) {
	ctx := context.Background()
	store := &hibernationSettingsStore{}
	svc := New(store, nil, Offering{}, nil)
	if _, err := svc.SetChatHibernationEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("database unavailable")
	if _, err := svc.SetChatHibernationEnabled(ctx, false); err == nil || svc.ChatHibernationEnabled() {
		t.Fatalf("disable after read error = %v, enabled=%v", err, svc.ChatHibernationEnabled())
	}
}

func TestOfferingFromConfigCarriesTrackerIntake(t *testing.T) {
	for _, on := range []bool{true, false} {
		got := OfferingFromConfig(config.Config{TrackerIntake: on})
		if got.TrackerIntakeEnabled != on {
			t.Errorf("OfferingFromConfig(TrackerIntake=%v).TrackerIntakeEnabled = %v, want %v", on, got.TrackerIntakeEnabled, on)
		}
	}
}

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

package authutil

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGenericPasswordFixedSelectorsAndMacOSOnly(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			calls := 0
			deps := Dependencies{GOOS: goos, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name != "/usr/bin/security" || !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "goose", "-a", "secrets", "-w"}) {
					t.Fatalf("keychain command = %q %q", name, args)
				}
				return []byte("fixture-secret\n"), nil
			}}
			got, err := GenericPassword(context.Background(), deps, "goose", "secrets")
			if goos == "darwin" {
				if err != nil || string(got) != "fixture-secret" || calls != 1 {
					t.Fatalf("mac keychain = %q, %v; calls %d", got, err, calls)
				}
			} else if err == nil || calls != 0 || len(got) != 0 {
				t.Fatalf("non-mac keychain executed: calls %d, err %v", calls, err)
			}
		})
	}
}

func TestGenericPasswordRejectsMissingSelectorsAndSanitizesFailures(t *testing.T) {
	deps := Dependencies{GOOS: "darwin", Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("fixture-secret") }}
	for _, selectors := range [][2]string{{"", "secrets"}, {"goose", ""}, {"goose", "secrets"}} {
		got, err := GenericPassword(context.Background(), deps, selectors[0], selectors[1])
		if len(got) != 0 || err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("unsafe keychain failure: %v", err)
		}
	}
}

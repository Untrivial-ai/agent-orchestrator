package authutil

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunCommandPreservesArgumentsWithoutShell(t *testing.T) {
	deps := Dependencies{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "native-agent" || !reflect.DeepEqual(args, []string{"status", "literal;$(not-a-command)"}) {
			t.Fatalf("command = %q %q", name, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return []byte("native-output"), nil
	}}
	got, err := RunCommand(context.Background(), deps, "native-agent", "status", "literal;$(not-a-command)")
	if err != nil || string(got) != "native-output" {
		t.Fatalf("command = %q, %v", got, err)
	}
}

func TestRunCommandTimeoutCancellationAndSecretFreeErrors(t *testing.T) {
	deps := Dependencies{Timeout: 5 * time.Millisecond, Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return []byte("fixture-secret"), errors.New("fixture-secret")
	}}
	if got, err := RunCommand(context.Background(), deps, "native-agent"); len(got) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunCommand(ctx, deps, "native-agent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	deps.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("fixture-secret"), errors.New("fixture-secret")
	}
	if got, err := RunCommand(context.Background(), deps, "native-agent"); len(got) != 0 || err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("failure = %q, %v", got, err)
	}
	deps.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Repeat("x", MaxFileSize+1)), nil
	}
	if got, err := RunCommand(context.Background(), deps, "native-agent"); len(got) != 0 || err == nil {
		t.Fatal("unbounded output accepted")
	}
}

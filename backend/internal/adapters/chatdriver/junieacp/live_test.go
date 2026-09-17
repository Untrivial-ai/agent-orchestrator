package junieacp

import (
	"os"
	"testing"
)

func TestLiveJunieACPConformance(t *testing.T) {
	if os.Getenv("AO_LIVE_JUNIE") != "1" {
		t.Skip("set AO_LIVE_JUNIE=1 with authenticated Junie ACP")
	}
	t.Fatal("Junie ACP build 3196.4 has not passed AO's load/permission/cancel/replay matrix; registration is blocked")
}

package mobilebridge

import (
	"context"
	"errors"
	"testing"
)

const statusJSON = `{
  "Self": {"DNSName": "prasads-macbook-pro.tail057d04.ts.net."},
  "CertDomains": ["prasads-macbook-pro.tail057d04.ts.net"],
  "MagicDNSSuffix": "tail057d04.ts.net"
}`

func fixedRunner(out string, err error) TailscaleRunner {
	return func(ctx context.Context, args ...string) ([]byte, error) { return []byte(out), err }
}

func TestQueryTailscaleParsesNameAndCerts(t *testing.T) {
	got := queryTailscale(context.Background(), fixedRunner(statusJSON, nil))
	if got.Name != "prasads-macbook-pro.tail057d04.ts.net" {
		t.Errorf("Name = %q, want the name with no trailing dot", got.Name)
	}
	if !got.CertsEnabled {
		t.Error("CertsEnabled = false, want true when CertDomains is non-empty")
	}
}

func TestQueryTailscaleCertsDisabled(t *testing.T) {
	const noCerts = `{"Self":{"DNSName":"host.tail1.ts.net."},"CertDomains":[]}`
	got := queryTailscale(context.Background(), fixedRunner(noCerts, nil))
	if got.Name != "host.tail1.ts.net" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.CertsEnabled {
		t.Error("CertsEnabled = true, want false for an empty CertDomains")
	}
}

// Every failure mode must yield the zero value rather than a partial result:
// the caller treats an empty Name as "secure pairing unavailable".
func TestQueryTailscaleFailuresYieldZero(t *testing.T) {
	cases := map[string]struct {
		out string
		err error
	}{
		"binary missing": {"", errors.New("exec: \"tailscale\": executable file not found in $PATH")},
		"non-zero exit":  {"", errors.New("exit status 1")},
		"malformed json": {"not json", nil},
		"empty DNSName":  {`{"Self":{"DNSName":""},"CertDomains":["x"]}`, nil},
		"missing Self":   {`{"CertDomains":["x"]}`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := queryTailscale(context.Background(), fixedRunner(tc.out, tc.err)); got.Name != "" {
				t.Errorf("Name = %q, want empty", got.Name)
			}
		})
	}
}

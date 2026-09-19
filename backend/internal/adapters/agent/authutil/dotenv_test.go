package authutil

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	got, err := ParseDotenv([]byte("# comment\nexport KEY=\"quoted # value\" # comment\nSINGLE='literal $KEY'\nBARE=value # ignored\nURL=https://example.test/#fragment\nEMPTY=\nKEY=\"last # value\"\nESCAPED=\"line\\nnext\"\n"))
	want := map[string]string{"KEY": "last # value", "SINGLE": "literal $KEY", "BARE": "value", "URL": "https://example.test/#fragment", "EMPTY": "", "ESCAPED": "line\nnext"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("dotenv = %#v, %v", got, err)
	}
}

func TestParseDotenvRejectsMalformedWithoutLeakingSecret(t *testing.T) {
	for _, fixture := range []string{"KEY=\"fixture-secret", "bad-name=fixture-secret", "KEY='fixture-secret' trailing", "fixture-secret", strings.Repeat("x", MaxFileSize+1)} {
		if _, err := ParseDotenv([]byte(fixture)); err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("unsafe/missing error = %v", err)
		}
	}
}

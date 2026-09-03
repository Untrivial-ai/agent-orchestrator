//go:build windows

package providersecret

import (
	"bytes"
	"testing"
)

func TestDPAPIRoundTripDoesNotStorePlaintext(t *testing.T) {
	plain := []byte("phase19b2-secret-sentinel")
	ciphertext, err := New().Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plain) {
		t.Fatal("DPAPI ciphertext contains plaintext")
	}
	got, err := New().Unprotect(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("DPAPI round trip mismatch")
	}
}

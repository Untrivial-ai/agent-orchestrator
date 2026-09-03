//go:build windows

package workeridentity

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cryptProtectUIForbidden = 0x1

func randomPassword() ([]byte, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(encoded, raw)
	return append(encoded, []byte("!aA1")...), nil
}

func protectForCurrentUser(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("worker identity: empty secret")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, cryptProtectUIForbidden, &out); err != nil {
		return nil, fmt.Errorf("worker identity: DPAPI protect: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotectForCurrentUser(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("worker identity: empty encrypted secret")
	}
	in := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, cryptProtectUIForbidden, &out); err != nil {
		return nil, fmt.Errorf("worker identity: DPAPI unprotect: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func writeProtectedSecret(path string, password []byte) error {
	ciphertext, err := protectForCurrentUser(password)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".worker-secret-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(ciphertext); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return secureHostOnlyFile(path)
}

func LoadPassword(cfg Config) ([]byte, error) {
	ciphertext, err := os.ReadFile(cfg.SecretPath)
	if err != nil {
		return nil, fmt.Errorf("worker identity: read encrypted account secret: %w", err)
	}
	return unprotectForCurrentUser(ciphertext)
}

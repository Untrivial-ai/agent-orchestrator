package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create secret AEAD: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// ProviderConnectionAssociatedData is the AES-GCM associated data that binds a
// provider-connection secret to its (scope, provider, label) tuple. The scope is
// an organization id for an org-scoped connection, or "user:<id>" for a
// user-scoped one. The HTTP edge that encrypts a secret and any service that
// later decrypts it MUST pass the identical string, so the construction lives
// here, shared by both, rather than being duplicated per call site.
func ProviderConnectionAssociatedData(scope, provider, label string) string {
	return scope + "|" + provider + "|" + label
}

func (c *Cipher) Encrypt(
	plaintext []byte,
	associatedData string,
) (encrypted, nonce []byte, err error) {
	if len(plaintext) == 0 {
		return nil, nil, errors.New("secret plaintext is empty")
	}
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate secret nonce: %w", err)
	}
	encrypted = c.aead.Seal(nil, nonce, plaintext, []byte(associatedData))
	return encrypted, nonce, nil
}

func (c *Cipher) Decrypt(
	encrypted, nonce []byte,
	associatedData string,
) ([]byte, error) {
	plaintext, err := c.aead.Open(nil, nonce, encrypted, []byte(associatedData))
	if err != nil {
		return nil, errors.New("decrypt secret: authentication failed")
	}
	return plaintext, nil
}

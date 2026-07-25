package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

var (
	ErrKeyNotSet     = errors.New("MENDEL_CREDENTIAL_KEY environment variable not set")
	ErrKeyWrongSize  = errors.New("MENDEL_CREDENTIAL_KEY must be 32 bytes (256 bits) base64-encoded")
	ErrCiphertextTooShort = errors.New("ciphertext too short")
)

// GetKey retrieves and validates the encryption key from environment.
// The key must be 32 bytes (256 bits) base64-encoded.
func GetKey() ([]byte, error) {
	encoded := os.Getenv("MENDEL_CREDENTIAL_KEY")
	if encoded == "" {
		return nil, ErrKeyNotSet
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrKeyWrongSize
	}

	if len(key) != 32 {
		return nil, ErrKeyWrongSize
	}

	return key, nil
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns ciphertext with prepended nonce.
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts ciphertext using AES-256-GCM.
// Expects ciphertext with prepended nonce.
func Decrypt(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrCiphertextTooShort
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// EncryptString is a convenience wrapper that encrypts a string.
func EncryptString(plaintext string, key []byte) ([]byte, error) {
	return Encrypt([]byte(plaintext), key)
}

// DecryptString is a convenience wrapper that decrypts to a string.
func DecryptString(ciphertext []byte, key []byte) (string, error) {
	plaintext, err := Decrypt(ciphertext, key)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// GenerateKey generates a random 32-byte key and returns it base64-encoded.
// Useful for initial setup.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

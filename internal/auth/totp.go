package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// GenerateTOTPSecret creates a new TOTP secret for issuer/accountName
// (shown in the authenticator app), returning the otpauth:// URL for a QR
// code during enrolment.
func GenerateTOTPSecret(issuer, accountName string) (secret string, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: accountName})
	if err != nil {
		return "", "", fmt.Errorf("auth: generate totp secret: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// ValidateTOTP checks a 6-digit code against secret at time now.
func ValidateTOTP(secret, code string, now time.Time) bool {
	valid, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period:    30,
		Skew:      1, // tolerate one 30s step of clock drift either way
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && valid
}

// SecretBox encrypts/decrypts TOTP secrets at rest with AES-256-GCM.
// There's no key-management infrastructure specified for this, so the key
// is a random 32 bytes generated on first use and stored on disk
// (0600) alongside the database — protected the same way the database
// itself is (filesystem permissions, DynamicUser). See NewSecretBox.
type SecretBox struct {
	gcm cipher.AEAD
}

// NewSecretBox builds a SecretBox from a 32-byte key.
func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: secret box key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: init GCM: %w", err)
	}
	return &SecretBox{gcm: gcm}, nil
}

// GenerateKey creates a new random 32-byte key, for first-run setup.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("auth: generate key: %w", err)
	}
	return key, nil
}

func (b *SecretBox) Seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("auth: generate nonce: %w", err)
	}
	return b.gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (b *SecretBox) Open(ciphertext []byte) (string, error) {
	n := b.gcm.NonceSize()
	if len(ciphertext) < n {
		return "", fmt.Errorf("auth: ciphertext too short")
	}
	nonce, data := ciphertext[:n], ciphertext[n:]
	plaintext, err := b.gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", fmt.Errorf("auth: decrypt: %w", err)
	}
	return string(plaintext), nil
}

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

const VMGuestCipherPrefix = "vm-guest:v1:"

// VMGuestCipher protects only VM guest credentials in the durable command.
// It deliberately has no plaintext fallback and uses separate encryption and
// intent keys derived from the mandatory replica-stable master key.
type VMGuestCipher struct {
	aead      cipher.AEAD
	intentKey []byte
}

func NewVMGuestCipher(master *AccessKeyCipher) (*VMGuestCipher, error) {
	key := master.DeriveSubkey("ani-governance/vm-guest/encryption/v1")
	intent := master.DeriveSubkey("ani-governance/vm-guest/intent/v1")
	if len(key) != 32 || len(intent) != 32 {
		return nil, errors.New("VM guest credential encryption unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &VMGuestCipher{aead: aead, intentKey: intent}, nil
}

func (c *VMGuestCipher) IntentDigest(credentials []byte) (string, error) {
	if c == nil || len(c.intentKey) != 32 || len(credentials) == 0 {
		return "", errors.New("VM guest credential intent unavailable")
	}
	mac := hmac.New(sha256.New, c.intentKey)
	mac.Write(credentials)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (c *VMGuestCipher) Seal(binding, credentials []byte) (string, error) {
	if c == nil || c.aead == nil || len(binding) == 0 || len(credentials) == 0 {
		return "", errors.New("VM guest credential encryption unavailable")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, credentials, binding)
	return VMGuestCipherPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *VMGuestCipher) Open(binding []byte, sealed string) ([]byte, error) {
	invalid := errors.New("VM guest credential envelope invalid")
	if c == nil || c.aead == nil || len(binding) == 0 || !strings.HasPrefix(sealed, VMGuestCipherPrefix) {
		return nil, invalid
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, VMGuestCipherPrefix))
	if err != nil || len(raw) < c.aead.NonceSize()+c.aead.Overhead() {
		return nil, invalid
	}
	plaintext, err := c.aead.Open(nil, raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():], binding)
	if err != nil || len(plaintext) == 0 {
		return nil, invalid
	}
	return plaintext, nil
}

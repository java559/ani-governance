package crypto

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestVMGuestCipherDurableBindingAndIntent(t *testing.T) {
	master, err := NewAccessKeyCipher(strings.Repeat("8a", 32))
	require.NoError(t, err)
	cipher, err := NewVMGuestCipher(master)
	require.NoError(t, err)
	secret := []byte(`{"console_password":"fixture-private-passphrase"}`)
	binding := []byte(`{"tenant":"t","actor":"u","operation":"o","resource":"r","spec_digest":"d"}`)
	first, err := cipher.Seal(binding, secret)
	require.NoError(t, err)
	second, err := cipher.Seal(binding, secret)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.NotContains(t, first, "fixture-private-passphrase")
	// A restarted process loading the same private master key can dispatch it.
	reopenedMaster, err := NewAccessKeyCipher(strings.Repeat("8a", 32))
	require.NoError(t, err)
	restarted, err := NewVMGuestCipher(reopenedMaster)
	require.NoError(t, err)
	plaintext, err := restarted.Open(binding, first)
	require.NoError(t, err)
	require.Equal(t, secret, plaintext)
	digest, err := cipher.IntentDigest(secret)
	require.NoError(t, err)
	replayDigest, err := restarted.IntentDigest(secret)
	require.NoError(t, err)
	require.Equal(t, digest, replayDigest)
	changed, err := cipher.IntentDigest(append(bytes.Clone(secret), 'x'))
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
	_, err = restarted.Open(append(bytes.Clone(binding), 'x'), first)
	require.Error(t, err, "tenant/op/resource/spec transplantation must fail")
	_, err = restarted.Open(binding, string(secret))
	require.Error(t, err, "plaintext must never be accepted as ciphertext")
	wrongMaster, err := NewAccessKeyCipher(strings.Repeat("8b", 32))
	require.NoError(t, err)
	wrong, err := NewVMGuestCipher(wrongMaster)
	require.NoError(t, err)
	_, err = wrong.Open(binding, first)
	require.Error(t, err)
	_, err = NewVMGuestCipher(nil)
	require.Error(t, err)
}

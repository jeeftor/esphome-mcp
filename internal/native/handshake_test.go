package native

import (
	"encoding/base64"
	"testing"

	"github.com/flynn/noise"
)

// TestNoiseHandshakeConfig verifies that the NNpsk0 handshake state can be
// initialized with the ESPHome prologue/PSK and produces a 32-byte ephemeral
// key in the first message (NNpsk0 message 1 = ephemeral key only, encrypted
// with PSK-mixed key but no payload).
func TestNoiseHandshakeConfig(t *testing.T) {
	psk := make([]byte, 32)
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Pattern:               noise.HandshakeNN,
		Initiator:             true,
		Prologue:              []byte("NoiseAPIInit\x00\x00"),
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		t.Fatalf("NewHandshakeState: %v", err)
	}
	msg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	// NNpsk0 first message: 32-byte ephemeral key + 16-byte AEAD tag (empty
	// payload encrypted with PSK-mixed key).
	if len(msg) != 48 {
		t.Errorf("first message length = %d, want 48", len(msg))
	}
}

// TestPSKBase64Roundtrip confirms a typical base64 ESPHome key decodes to 32
// bytes, matching what ListEntities expects.
func TestPSKBase64Roundtrip(t *testing.T) {
	raw := make([]byte, 32)
	raw[0] = 0xAB
	raw[31] = 0xCD
	enc := base64.StdEncoding.EncodeToString(raw)
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec) != 32 || dec[0] != 0xAB || dec[31] != 0xCD {
		t.Errorf("PSK roundtrip mismatch: %v", dec)
	}
}

package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultUpdatePublicKeyBase64 is the production ed25519 public key used to
// verify update artifact signatures published in latest.json.
//
// The matching private key is held by release operators and supplied via
// PCHAT_UPDATE_SIGNING_KEY when packaging. It must never be committed.
const DefaultUpdatePublicKeyBase64 = "TUzgC2Rl2YGqZoced9B/moz8pCx3wf8Sa0mACbVcAmc="

// ErrMissingSignature reports that an installable artifact has no publisher signature.
var ErrMissingSignature = errors.New("update artifact signature missing")

// ErrInvalidSignature reports a failed publisher signature check.
var ErrInvalidSignature = errors.New("update artifact signature invalid")

// ArtifactSignPayload is the canonical message signed for an update artifact.
// ArtifactSignPayload is the bytes signed by the release private key.
func ArtifactSignPayload(sha256Hex, version, kind string) []byte {
	return []byte("pchat-update-v1\n" +
		strings.ToLower(strings.TrimSpace(sha256Hex)) + "\n" +
		strings.TrimSpace(version) + "\n" +
		strings.ToLower(strings.TrimSpace(kind)) + "\n")
}

// SignArtifact returns a base64 ed25519 signature over ArtifactSignPayload.
// SignArtifact signs the canonical artifact payload.
func SignArtifact(privateKey ed25519.PrivateKey, sha256Hex, version, kind string) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid ed25519 private key size")
	}
	sig := ed25519.Sign(privateKey, ArtifactSignPayload(sha256Hex, version, kind))
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyArtifactSignature checks a base64 signature against the public key.
// VerifyArtifactSignature validates an artifact publisher signature.
func VerifyArtifactSignature(publicKey ed25519.PublicKey, sha256Hex, version, kind, signature string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid ed25519 public key size")
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return ErrMissingSignature
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w: decode: %v", ErrInvalidSignature, err)
	}
	if !ed25519.Verify(publicKey, ArtifactSignPayload(sha256Hex, version, kind), raw) {
		return ErrInvalidSignature
	}
	return nil
}

// ParsePublicKeyBase64 decodes a standard-base64 ed25519 public key.
// ParsePublicKeyBase64 decodes a base64-encoded public key.
func ParsePublicKeyBase64(raw string) (ed25519.PublicKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty public key")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key length %d, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKeyBase64 decodes a standard-base64 ed25519 private key.
// Both the 64-byte seed+public form from ed25519.GenerateKey and a 32-byte
// seed are accepted. The seed is expanded with ed25519.NewKeyFromSeed.
// ParsePrivateKeyBase64 decodes a base64-encoded private key.
func ParsePrivateKeyBase64(raw string) (ed25519.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty private key")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("private key length %d, want %d or %d", len(b), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func defaultPublicKey() ed25519.PublicKey {
	key, err := ParsePublicKeyBase64(DefaultUpdatePublicKeyBase64)
	if err != nil {
		panic("update: invalid DefaultUpdatePublicKeyBase64: " + err.Error())
	}
	return key
}

// signatureRequired reports whether downloads must carry a valid publisher
// signature. Set PCHAT_UPDATE_REQUIRE_SIGNATURE=0 only for local emergency
// bypasses; production builds keep the default (required).
func signatureRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PCHAT_UPDATE_REQUIRE_SIGNATURE"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

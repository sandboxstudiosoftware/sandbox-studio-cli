package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
)

// Login-token encryption for the SBS-branded login (security finding H3).
//
// The branded login hands the id_token to a loopback listener on one of a few
// fixed ports. Any local process could race to bind such a port, so the token is
// never sent in the clear: the CLI generates an ephemeral P-256 key pair per
// login, sends the PUBLIC key to the login page, and the token comes back
// encrypted to it. Only this process holds the private key, so owning the port is
// not enough to read the token.
//
// Scheme — MUST stay byte-compatible with the API Lambda
// (source/lambda/api/src/auth/cli-token-encryption.ts) and the login page
// (source/ui/src/pages/login/cli-login.ts); a shared fixed vector is asserted in
// all three test suites:
//
//	cli_key  = base64url(uncompressed P-256 public point, 65 bytes, 0x04||X||Y)
//	secret   = ECDH(ephemeral private, cli_key)                       (32 bytes)
//	key      = HKDF-SHA256(secret, salt=state, info="sbs-cli-login-v1") (32 bytes)
//	enc      = base64url( ephemeralPublic(65) || iv(12) || ciphertext || tag(16) )
//	cipher   = AES-256-GCM, AAD = state
const (
	hkdfInfo       = "sbs-cli-login-v1"
	pubKeyLen      = 65
	gcmIVLen       = 12
	gcmTagLen      = 16
	maxEncodedEncB = 16 * 1024 // upper bound on the encoded payload we will even decode
)

// loginStatePattern mirrors the server/UI constraint on the CLI-generated state.
var loginStatePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// loginKey is the per-login ephemeral key pair. The private half never leaves
// this process and is never written to disk.
type loginKey struct {
	priv *ecdh.PrivateKey
}

func newLoginKey() (*loginKey, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate login key: %w", err)
	}
	return &loginKey{priv: priv}, nil
}

// publicParam returns the value for the `cli_key` login URL parameter.
func (k *loginKey) publicParam() string {
	return base64.RawURLEncoding.EncodeToString(k.priv.PublicKey().Bytes())
}

// decrypt opens the `enc` payload delivered by the login page. It authenticates
// the ciphertext against `state`, so a payload from another login attempt (or a
// tampered one) is rejected.
func (k *loginKey) decrypt(state, enc string) (string, error) {
	if !loginStatePattern.MatchString(state) {
		return "", errors.New("invalid login state")
	}
	if len(enc) == 0 || len(enc) > maxEncodedEncB {
		return "", errors.New("invalid encrypted token size")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("decode encrypted token: %w", err)
	}
	if len(raw) < pubKeyLen+gcmIVLen+gcmTagLen+1 {
		return "", errors.New("encrypted token too short")
	}

	// NewPublicKey rejects points that are not on the curve.
	ephPub, err := ecdh.P256().NewPublicKey(raw[:pubKeyLen])
	if err != nil {
		return "", fmt.Errorf("invalid ephemeral key: %w", err)
	}
	secret, err := k.priv.ECDH(ephPub)
	if err != nil {
		return "", fmt.Errorf("key agreement: %w", err)
	}
	key, err := hkdf.Key(sha256.New, secret, []byte(state), hkdfInfo, 32)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := raw[pubKeyLen : pubKeyLen+gcmIVLen]
	plaintext, err := gcm.Open(nil, iv, raw[pubKeyLen+gcmIVLen:], []byte(state))
	if err != nil {
		// Do not distinguish causes (wrong key, wrong state, tampering).
		return "", errors.New("could not decrypt login token")
	}
	return string(plaintext), nil
}

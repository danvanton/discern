package discdb

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// PublicKey is the key the published index is signed with (decision D59).
// The TheDiscDB index workflow signs with its private half, the GitHub
// secret DISCDB_SIGNING_KEY. A new key needs a new Discern release.
var PublicKey = mustPublicKey("xFSFEASfNJu9zpPhAz19kL/Z4rdFd5O2jMG5KNGQWEY=")

// maxSignature bounds the downloaded signature file: one base64 line.
const maxSignature = 1 << 10

// Sign signs an index with the private key's seed, and returns the
// signature file's contents: the signature in base64, on one line.
func Sign(seed, index []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("a signing key is %d bytes, not %d", ed25519.SeedSize, len(seed))
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), index)
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n"), nil
}

// Verify checks a signature file's contents against the index and key. Any
// doubt (a malformed file, a wrong length, a signature that doesn't verify)
// is an error.
func Verify(key ed25519.PublicKey, index, sigFile []byte) error {
	if len(key) != ed25519.PublicKeySize {
		return errors.New("no key to check the disc index's signature with")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the disc index's signature isn't an Ed25519 signature")
	}
	if !ed25519.Verify(key, index, sig) {
		return errors.New("the disc index's signature doesn't match: it wasn't published by Discern's workflow")
	}
	return nil
}

// ParseSeed reads a private key's seed as kept in the GitHub secret: 32
// bytes in base64.
func ParseSeed(s string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("a signing key is %d bytes in base64", ed25519.SeedSize)
	}
	return seed, nil
}

func mustPublicKey(s string) ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("discdb: the built-in public key is malformed")
	}
	return ed25519.PublicKey(b)
}

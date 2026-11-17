package discdb

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// A signature made with a key verifies with its public half only, and
// only for the same bytes.
func TestSignAndVerify(t *testing.T) {
	index := []byte("an index")
	sig, err := Sign(testSeed, index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(sig, []byte("\n")) || bytes.Count(sig, []byte("\n")) != 1 {
		t.Errorf("signature file %q: want one line", sig)
	}
	if err := Verify(testKey(), index, sig); err != nil {
		t.Errorf("a good signature: %v", err)
	}
	if err := Verify(testKey(), []byte("another index"), sig); err == nil {
		t.Error("a signature verified for other bytes")
	}
	if err := Verify(PublicKey, index, sig); err == nil {
		t.Error("Discern's key verified the tests' signature")
	}
	if err := Verify(nil, index, sig); err == nil {
		t.Error("no key verified a signature")
	}
	if _, err := Sign([]byte("short"), index); err == nil {
		t.Error("a short seed signed")
	}
}

// The secret's seed is 32 bytes in base64; anything else is refused.
func TestParseSeed(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(testSeed)
	if seed, err := ParseSeed(" " + good + "\n"); err != nil || !bytes.Equal(seed, testSeed) {
		t.Errorf("a good seed: %v", err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short")),
		base64.StdEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize))} {
		if _, err := ParseSeed(bad); err == nil || !strings.Contains(err.Error(), "32 bytes") {
			t.Errorf("ParseSeed(%q): %v", bad, err)
		}
	}
}

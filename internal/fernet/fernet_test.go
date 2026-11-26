package fernet

import "testing"

// The vector from the Fernet specification.
const (
	specKey   = "cw_0x689RpI-jtRR7oE8h_eQsKImvJapLeSbXpwF4e4="
	specToken = "gAAAAAAdwJ6wAAECAwQFBgcICQoLDA0ODy021cpGVWKZ_eEwCGM4BLLF_5CV9dOPmrhuVUPgJobwOz7JcbmrR64jVmpU4IwqDA=="
)

func TestDecrypt(t *testing.T) {
	plain, err := Decrypt(specToken, specKey)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("Decrypt = %q, %v", plain, err)
	}
	if _, err := Decrypt(specToken[:len(specToken)-6]+"AAAAA=", specKey); err == nil {
		t.Error("a token with a changed signature was accepted")
	}
	if _, err := Decrypt(specToken, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err == nil {
		t.Error("the wrong key was accepted")
	}
	if _, err := Decrypt("not a token", specKey); err == nil {
		t.Error("garbage was accepted")
	}
}

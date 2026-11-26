// Package fernet decrypts Fernet tokens. The Discord bot token is kept in
// the repository as a Fernet ciphertext, with the key outside it; this is
// the reading half of that arrangement.
package fernet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrInvalid is returned for any token or key that does not verify. The
// reason is deliberately not given.
var ErrInvalid = errors.New("fernet: 復号できませんでした")

func decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	return base64.URLEncoding.DecodeString(s + strings.Repeat("=", (4-len(s)%4)%4))
}

// Decrypt verifies and decrypts token with the 32-byte key (both in the
// URL-safe base64 form Fernet uses).
func Decrypt(token, key string) ([]byte, error) {
	k, err := decode(key)
	if err != nil || len(k) != 32 {
		return nil, ErrInvalid
	}
	raw, err := decode(token)
	// version(1) + time(8) + iv(16) + at least one block + hmac(32)
	if err != nil || len(raw) < 1+8+16+aes.BlockSize+32 || raw[0] != 0x80 {
		return nil, ErrInvalid
	}
	body, sum := raw[:len(raw)-32], raw[len(raw)-32:]
	mac := hmac.New(sha256.New, k[:16])
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), sum) {
		return nil, ErrInvalid
	}
	iv, ciphertext := body[9:25], body[25:]
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(k[16:])
	if err != nil {
		return nil, ErrInvalid
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return nil, ErrInvalid
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, ErrInvalid
		}
	}
	return plain[:len(plain)-pad], nil
}

package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// writePPK encodes key the way PuTTYgen does (format 3 with Argon2id when
// encrypted, or an unencrypted format 2/3 file).
func writePPK(t *testing.T, version int, key any, pass string) string {
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	algo, pub := signer.PublicKey().Type(), signer.PublicKey().Marshal()
	var priv []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		priv = ssh.Marshal(struct{ D, P, Q, Iqmp *big.Int }{k.D, k.Primes[0], k.Primes[1], k.Precomputed.Qinv})
	case ed25519.PrivateKey:
		priv = ssh.Marshal(struct{ Seed []byte }{k.Seed()})
	case *ecdsa.PrivateKey:
		priv = ssh.Marshal(struct{ D *big.Int }{k.D})
	}
	enc, comment := "none", "test-key"
	h := map[string]string{}
	var kdfLines string
	var macKey []byte
	newHash := sha256.New
	if version == 2 {
		newHash = sha1.New
		m := sha1.Sum([]byte("putty-private-key-file-mac-key"))
		macKey = m[:]
	}
	if pass != "" {
		enc = "aes256-cbc"
		priv = append(priv, make([]byte, aes.BlockSize-len(priv)%aes.BlockSize)...)
		h = map[string]string{"Key-Derivation": "Argon2id", "Argon2-Memory": "8192", "Argon2-Passes": "1", "Argon2-Parallelism": "1", "Argon2-Salt": "0123456789abcdef0123456789abcdef"}
		kdfLines = fmt.Sprintf("Key-Derivation: Argon2id\nArgon2-Memory: 8192\nArgon2-Passes: 1\nArgon2-Parallelism: 1\nArgon2-Salt: %s\n", h["Argon2-Salt"])
	}
	var mac []byte
	if pass != "" {
		k, err := ppkArgon2(h, []byte(pass))
		if err != nil {
			t.Fatal(err)
		}
		mac = ppkMAC(newHash, k[48:], algo, enc, comment, pub, priv)
		block, _ := aes.NewCipher(k[:32])
		cipher.NewCBCEncrypter(block, k[32:48]).CryptBlocks(priv, priv)
	} else {
		mac = ppkMAC(newHash, macKey, algo, enc, comment, pub, priv)
	}
	b64 := func(b []byte) string {
		s := base64.StdEncoding.EncodeToString(b)
		var lines []string
		for len(s) > 64 {
			lines, s = append(lines, s[:64]), s[64:]
		}
		lines = append(lines, s)
		return fmt.Sprintf("%d\r\n%s", len(lines), strings.Join(lines, "\r\n"))
	}
	return fmt.Sprintf("%s%d: %s\r\nEncryption: %s\r\nComment: %s\r\nPublic-Lines: %s\r\n%sPrivate-Lines: %s\r\nPrivate-MAC: %s\r\n",
		ppkPrefix, version, algo, enc, comment, b64(pub), strings.ReplaceAll(kdfLines, "\n", "\r\n"), b64(priv), hex.EncodeToString(mac))
}

func TestParsePPK(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	for _, key := range []any{rsaKey, edKey, ecKey} {
		want, _ := ssh.NewSignerFromKey(key)
		for _, c := range []struct {
			version int
			pass    string
		}{{3, "secret"}, {3, ""}, {2, ""}} {
			name := fmt.Sprintf("%s/v%d/pass=%q", want.PublicKey().Type(), c.version, c.pass)
			file := writePPK(t, c.version, key, c.pass)
			got, err := parsePPK([]byte(file), []byte(c.pass))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !bytes.Equal(got.PublicKey().Marshal(), want.PublicKey().Marshal()) {
				t.Fatalf("%s: public key mismatch", name)
			}
			sig, err := got.Sign(rand.Reader, []byte("data"))
			if err != nil || want.PublicKey().Verify([]byte("data"), sig) != nil {
				t.Fatalf("%s: signature does not verify: %v", name, err)
			}
			if c.pass != "" {
				if _, err := parsePPK([]byte(file), []byte("wrong")); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
					t.Fatalf("%s: wrong passphrase gave %v", name, err)
				}
			}
		}
	}
}

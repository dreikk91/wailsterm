package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/ssh"
)

const ppkPrefix = "PuTTY-User-Key-File-"

// parsePPK decodes a PuTTY private key file (format 2 or 3) into a signer.
func parsePPK(data, passphrase []byte) (ssh.Signer, error) {
	h := map[string]string{}
	blobs := map[string][]byte{}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		k, v, ok := strings.Cut(strings.TrimSpace(lines[i]), ": ")
		if !ok {
			continue
		}
		if k == "Public-Lines" || k == "Private-Lines" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || i+n >= len(lines) {
				return nil, fmt.Errorf("ppk: bad %s", k)
			}
			b, err := base64.StdEncoding.DecodeString(strings.Join(lines[i+1:i+1+n], ""))
			if err != nil {
				return nil, fmt.Errorf("ppk: %s: %w", k, err)
			}
			blobs[k] = b
			i += n
			continue
		}
		h[k] = v
	}

	algo, version := h[ppkPrefix+"3"], 3
	if algo == "" {
		algo, version = h[ppkPrefix+"2"], 2
	}
	if algo == "" {
		return nil, errors.New("ppk: unsupported file version, only PuTTY key formats 2 and 3 are supported")
	}
	enc, pub, priv := h["Encryption"], blobs["Public-Lines"], blobs["Private-Lines"]
	encrypted := enc == "aes256-cbc"
	switch {
	case !encrypted && enc != "none":
		return nil, fmt.Errorf("ppk: unsupported encryption %q", enc)
	case encrypted && len(passphrase) == 0:
		return nil, errors.New("ppk: key is encrypted, a passphrase is required")
	case !encrypted:
		passphrase = nil
	}

	var cipherKey, iv, macKey []byte
	newHash := sha256.New
	if version == 2 {
		newHash = sha1.New
		m := sha1.Sum(append([]byte("putty-private-key-file-mac-key"), passphrase...))
		macKey = m[:]
		if encrypted {
			a := sha1.Sum(append([]byte{0, 0, 0, 0}, passphrase...))
			b := sha1.Sum(append([]byte{0, 0, 0, 1}, passphrase...))
			cipherKey, iv = append(a[:], b[:]...)[:32], make([]byte, aes.BlockSize)
		}
	} else if encrypted {
		k, err := ppkArgon2(h, passphrase)
		if err != nil {
			return nil, err
		}
		cipherKey, iv, macKey = k[:32], k[32:48], k[48:]
	}

	if encrypted {
		if len(priv)%aes.BlockSize != 0 {
			return nil, errors.New("ppk: private blob is not a whole number of cipher blocks")
		}
		block, _ := aes.NewCipher(cipherKey)
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(priv, priv)
	}
	if !hmac.Equal(ppkMAC(newHash, macKey, algo, enc, h["Comment"], pub, priv), mustHex(h["Private-MAC"])) {
		if encrypted {
			return nil, errors.New("ppk: wrong passphrase")
		}
		return nil, errors.New("ppk: MAC mismatch, the key file is corrupted")
	}

	key, err := ppkKey(algo, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("ppk: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("ppk: %w", err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), pub) {
		return nil, errors.New("ppk: private key does not match the public key")
	}
	return signer, nil
}

// ppkArgon2 derives the 80 bytes of cipher key, IV and MAC key of a format 3 file.
func ppkArgon2(h map[string]string, passphrase []byte) ([]byte, error) {
	kdf := map[string]func([]byte, []byte, uint32, uint32, uint8, uint32) []byte{
		"Argon2id": argon2.IDKey,
		"Argon2i":  argon2.Key,
	}[h["Key-Derivation"]]
	if kdf == nil {
		// x/crypto has no Argon2d; PuTTYgen only writes it when asked to.
		return nil, fmt.Errorf("ppk: unsupported key derivation %q", h["Key-Derivation"])
	}
	mem, err1 := strconv.ParseUint(h["Argon2-Memory"], 10, 32)
	passes, err2 := strconv.ParseUint(h["Argon2-Passes"], 10, 32)
	par, err3 := strconv.ParseUint(h["Argon2-Parallelism"], 10, 8)
	salt, err4 := hex.DecodeString(h["Argon2-Salt"])
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, fmt.Errorf("ppk: bad Argon2 parameters: %w", err)
	}
	return kdf(passphrase, salt, uint32(passes), uint32(mem), uint8(par), 80), nil
}

func ppkMAC(newHash func() hash.Hash, macKey []byte, algo, enc, comment string, pub, priv []byte) []byte {
	m := hmac.New(newHash, macKey)
	m.Write(ssh.Marshal(struct {
		Algo, Enc, Comment string
		Pub, Priv          []byte
	}{algo, enc, comment, pub, priv}))
	return m.Sum(nil)
}

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// ppkKey builds the private key from the public and decrypted private blobs.
// The private blob is padded to the cipher block size, hence the Rest fields.
func ppkKey(algo string, pub, priv []byte) (any, error) {
	switch algo {
	case ssh.KeyAlgoRSA:
		var p struct {
			Name string
			E, N *big.Int
		}
		var s struct {
			D, P, Q, Iqmp *big.Int
			Rest          []byte `ssh:"rest"`
		}
		if err := errors.Join(ssh.Unmarshal(pub, &p), ssh.Unmarshal(priv, &s)); err != nil {
			return nil, err
		}
		k := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: p.N, E: int(p.E.Int64())}, D: s.D, Primes: []*big.Int{s.P, s.Q}}
		if err := k.Validate(); err != nil {
			return nil, err
		}
		k.Precompute()
		return k, nil
	case ssh.KeyAlgoED25519:
		var s struct {
			Seed []byte
			Rest []byte `ssh:"rest"`
		}
		if err := ssh.Unmarshal(priv, &s); err != nil {
			return nil, err
		}
		if len(s.Seed) != ed25519.SeedSize {
			return nil, errors.New("bad ed25519 private key length")
		}
		return ed25519.NewKeyFromSeed(s.Seed), nil
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		curve := map[string]elliptic.Curve{
			ssh.KeyAlgoECDSA256: elliptic.P256(),
			ssh.KeyAlgoECDSA384: elliptic.P384(),
			ssh.KeyAlgoECDSA521: elliptic.P521(),
		}[algo]
		var s struct {
			D    *big.Int
			Rest []byte `ssh:"rest"`
		}
		if err := ssh.Unmarshal(priv, &s); err != nil {
			return nil, err
		}
		return ecdsa.ParseRawPrivateKey(curve, s.D.FillBytes(make([]byte, (curve.Params().BitSize+7)/8)))
	}
	return nil, fmt.Errorf("unsupported key type %q", algo)
}

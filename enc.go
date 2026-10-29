package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// Sync payload encryption, byte-compatible with electerm's lib/enc.js so data synced by
// electerm and by wailsterm can be read by either:
//   gcm:<iv hex>:<salt hex>:<tag hex>:<ciphertext hex>   aes-256-gcm, key = scrypt(password, salt, 32)
//   <ciphertext hex>                                    legacy aes-192-cbc, salt "salt", zero IV

// nodeScrypt uses Node's crypto.scrypt defaults (N=16384, r=8, p=1).
func nodeScrypt(password string, salt []byte, n int) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 16384, 8, 1, n)
}

func encryptSync(plain, password string) (string, error) {
	iv, salt := make([]byte, 12), make([]byte, 16)
	rand.Read(iv)
	rand.Read(salt)
	key, err := nodeScrypt(password, salt, 32)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	return "gcm:" + hex.EncodeToString(iv) + ":" + hex.EncodeToString(salt) + ":" + hex.EncodeToString(tag) + ":" + hex.EncodeToString(ct), nil
}

func decryptSync(enc, password string) (string, error) {
	if strings.HasPrefix(enc, "gcm:") {
		parts := strings.Split(enc, ":")
		if len(parts) != 5 {
			return "", errors.New("malformed encrypted data")
		}
		var raw [4][]byte
		for i := range raw {
			b, err := hex.DecodeString(parts[i+1])
			if err != nil {
				return "", err
			}
			raw[i] = b
		}
		iv, salt, tag, ct := raw[0], raw[1], raw[2], raw[3]
		key, err := nodeScrypt(password, salt, 32)
		if err != nil {
			return "", err
		}
		block, _ := aes.NewCipher(key)
		gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
		if err != nil {
			return "", err
		}
		plain, err := gcm.Open(nil, iv, append(ct, tag...), nil)
		if err != nil {
			return "", errors.New("cannot decrypt sync data: the sync encrypt password differs from the one used to upload")
		}
		return string(plain), nil
	}
	ct, err := hex.DecodeString(enc)
	if err != nil || len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("malformed encrypted data")
	}
	key, err := nodeScrypt(password, []byte("salt"), 24)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(plain, ct)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return "", errors.New("bad decrypt")
	}
	return string(plain[:len(plain)-pad]), nil
}

//go:build !windows

package main

// ponytail: plaintext (0600 files) off Windows; use the OS keyring (libsecret/Keychain) for parity with safeStorage.
func protect(b []byte) ([]byte, error) { return b, nil }

func unprotect(b []byte) ([]byte, error) { return b, nil }

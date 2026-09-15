package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secret.key")
	box, err := Open(keyPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	blob, err := box.Encrypt("vcenter-password")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(blob, []byte("vcenter-password")) {
		t.Fatal("ciphertext contains plaintext")
	}

	got, err := box.Decrypt(blob)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != "vcenter-password" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestKeyPersistsAcrossOpens(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secret.key")
	box1, err := Open(keyPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	blob, err := box1.Encrypt("s3cret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	box2, err := Open(keyPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	got, err := box2.Decrypt(blob)
	if err != nil || got != "s3cret" {
		t.Fatalf("decrypt with reloaded key: %q, %v", got, err)
	}

	st, _ := os.Stat(keyPath)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestDecryptGarbageFails(t *testing.T) {
	box, err := Open(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := box.Decrypt([]byte("not a real ciphertext blob")); err == nil {
		t.Fatal("expected error for garbage ciphertext")
	}
}

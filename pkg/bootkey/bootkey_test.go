package bootkey

import (
	"bytes"
	"os"
	"testing"
)

func TestEnvUnwrapperRoundTrip(t *testing.T) {
	u, err := newEnvUnwrapper("a-test-boot-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"1":"ZGVr"}`)
	sealed, err := u.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("sealed blob must not contain plaintext")
	}
	got, err := u.Unseal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("unseal = %q, want %q", got, plain)
	}
}

func TestEnvUnwrapperWrongKeyFails(t *testing.T) {
	a, _ := newEnvUnwrapper("key-a")
	b, _ := newEnvUnwrapper("key-b")
	sealed, _ := a.Seal([]byte("secret"))
	if _, err := b.Unseal(sealed); err == nil {
		t.Fatal("a different boot key must not unseal the blob")
	}
}

func TestEnvUnwrapperHexKey(t *testing.T) {
	// A 64-hex key is used verbatim; two identical hex keys interoperate.
	hexKey := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	a, err := newEnvUnwrapper(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newEnvUnwrapper(hexKey)
	sealed, _ := a.Seal([]byte("x"))
	if _, err := b.Unseal(sealed); err != nil {
		t.Fatalf("identical hex boot keys must interoperate: %v", err)
	}
}

func TestLocalBootKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/keyring.bootkey"

	// First boot: no key file yet → a fresh in-memory key, nothing on disk.
	k1, err := LoadOrCreateLocalBootKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no key file must exist during runtime")
	}

	// Seal something and persist the key at "graceful shutdown".
	blob, err := k1.Seal([]byte("secret-keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if err := k1.Persist(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("key file must exist after Persist: %v", err)
	} else if fi.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v, want 0600", fi.Mode().Perm())
	}

	// Next boot: the key is loaded (so it can unseal last run's snapshot) AND
	// the file is consumed — nothing left on disk during runtime.
	k2, err := LoadOrCreateLocalBootKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("key file must be deleted (consumed) on load")
	}
	out, err := k2.Unseal(blob)
	if err != nil || string(out) != "secret-keyring" {
		t.Fatalf("reloaded key must unseal the prior snapshot: %q, %v", out, err)
	}
}

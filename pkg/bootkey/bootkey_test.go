package bootkey

import (
	"bytes"
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

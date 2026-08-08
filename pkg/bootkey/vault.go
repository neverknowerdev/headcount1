package bootkey

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// vaultTransit seals/unseals via a Vault (or OpenBao) Transit engine, so the
// boot key never leaves the vault. Encryption-as-a-service: we send base64
// plaintext to /transit/encrypt/<key> and get a "vault:v1:..." ciphertext,
// and reverse it via /transit/decrypt/<key>.
type vaultTransit struct {
	addr      string
	token     string
	namespace string
	keyName   string
	client    *http.Client
}

func vaultTransitFromEnv(addr string) *vaultTransit {
	token := os.Getenv("VAULT_TOKEN")
	keyName := os.Getenv("HEADCOUNT1_BOOT_TRANSIT_KEY")
	if token == "" || keyName == "" {
		// Transit not configured for boot-key use; let another backend win.
		return nil
	}
	return &vaultTransit{
		addr:      strings.TrimRight(addr, "/"),
		token:     token,
		namespace: os.Getenv("VAULT_NAMESPACE"),
		keyName:   keyName,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *vaultTransit) Name() string { return "vault-transit:" + v.keyName }

func (v *vaultTransit) do(path string, body map[string]string, out interface{}) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, v.addr+"/v1/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", v.token)
	if v.namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.namespace)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("bootkey: vault transit %s returned %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (v *vaultTransit) Seal(plaintext []byte) ([]byte, error) {
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := v.do("transit/encrypt/"+v.keyName, map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}, &out); err != nil {
		return nil, err
	}
	return []byte(out.Data.Ciphertext), nil
}

func (v *vaultTransit) Unseal(ciphertext []byte) ([]byte, error) {
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := v.do("transit/decrypt/"+v.keyName, map[string]string{
		"ciphertext": string(ciphertext),
	}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data.Plaintext)
}

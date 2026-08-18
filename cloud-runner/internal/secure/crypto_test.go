package secure

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSealOpenAndPurposeBinding(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Seal([]byte("opaque-auth"), Purpose("codex", "slot-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("opaque-auth")) {
		t.Fatal("ciphertext exposes plaintext")
	}
	plaintext, err := box.Open(ciphertext, Purpose("codex", "slot-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "opaque-auth" {
		t.Fatalf("got %q", plaintext)
	}
	if _, err := box.Open(ciphertext, Purpose("credential", "slot-1")); err == nil {
		t.Fatal("purpose mismatch should fail")
	}
}

func TestRunCapabilityIsScopedAndExpires(t *testing.T) {
	key, _ := GenerateKey()
	box, _ := NewBox(key)
	now := time.Now()
	token, err := box.MintCapability("run_one", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := box.VerifyCapability(token, "run_one", now); err != nil {
		t.Fatal(err)
	}
	if err := box.VerifyCapability(token, "run_two", now); err == nil {
		t.Fatal("cross-run capability accepted")
	}
	if err := box.VerifyCapability(token, "run_one", now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired capability accepted")
	}
}

func TestEventRedactionRemovesKnownSecretsAndSensitiveFields(t *testing.T) {
	secret := "management-secret-value"
	message := Redact("Bearer abcdefghijklmnop "+secret+" ghp_abcdefghijklmnopqrstuvwxyz123456", secret)
	if strings.Contains(message, secret) || strings.Contains(message, "abcdefghijklmnop") || strings.Contains(message, "ghp_") {
		t.Fatalf("message retained a secret: %s", message)
	}
	payload := RedactJSON(json.RawMessage(`{"ticket_key":"T01","access_token":"opaque-token","nested":{"password":"bad","note":"management-secret-value"}}`), secret)
	if strings.Contains(string(payload), "opaque-token") || strings.Contains(string(payload), "bad") || strings.Contains(string(payload), secret) {
		t.Fatalf("payload retained a secret: %s", payload)
	}
	if !strings.Contains(string(payload), `"ticket_key":"T01"`) {
		t.Fatalf("payload lost non-sensitive evidence: %s", payload)
	}
}

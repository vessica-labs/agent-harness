package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const envelopeVersion = "v1"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bgh[opusr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|lin_api_[A-Za-z0-9_-]{16,})\b`),
}

type Box struct {
	aead cipher.AEAD
	key  []byte
}

func NewBox(encoded string) (*Box, error) {
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, key: append([]byte(nil), key...)}, nil
}

func GenerateKey() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeKey(value string) ([]byte, error) {
	for _, decoder := range []func(string) ([]byte, error){base64.RawURLEncoding.DecodeString, base64.StdEncoding.DecodeString, hex.DecodeString} {
		decoded, err := decoder(value)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("HARNESS_CREDENTIAL_KEY must encode exactly 32 bytes")
}

func (b *Box) Seal(plaintext []byte, purpose string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := b.aead.Seal(nil, nonce, plaintext, []byte(purpose))
	return []byte(envelopeVersion + "." + base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(sealed)), nil
}

func (b *Box) Open(envelope []byte, purpose string) ([]byte, error) {
	parts := strings.Split(string(envelope), ".")
	if len(parts) != 3 || parts[0] != envelopeVersion {
		return nil, errors.New("unsupported credential envelope")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid credential nonce")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("invalid credential ciphertext")
	}
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return nil, errors.New("credential authentication failed")
	}
	return plaintext, nil
}

func (b *Box) MintCapability(runID string, expires time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := runID + "." + strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte("capability:" + payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (b *Box) VerifyCapability(token, runID string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != runID {
		return errors.New("invalid run capability")
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.After(time.Unix(expiry, 0)) {
		return errors.New("expired run capability")
	}
	payload := strings.Join(parts[:3], ".")
	provided, err := hex.DecodeString(parts[3])
	if err != nil {
		return errors.New("invalid run capability signature")
	}
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte("capability:" + payload))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("invalid run capability signature")
	}
	return nil
}

func Bearer(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func EqualSecret(left, right string) bool {
	return left != "" && right != "" && hmac.Equal([]byte(left), []byte(right))
}

func Redact(value string, secrets ...string) string {
	result := value
	for _, secret := range secrets {
		if len(secret) >= 8 {
			result = strings.ReplaceAll(result, secret, "[REDACTED]")
		}
	}
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAllString(result, "[REDACTED]")
	}
	return result
}

func RedactJSON(value json.RawMessage, secrets ...string) json.RawMessage {
	if len(value) == 0 {
		return value
	}
	var document any
	if json.Unmarshal(value, &document) != nil {
		return json.RawMessage(Redact(string(value), secrets...))
	}
	document = redactValue(document, secrets)
	redacted, err := json.Marshal(document)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return redacted
}

func redactValue(value any, secrets []string) any {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if sensitiveKey(key) && !safeMetricKey(key) {
				current[key] = "[REDACTED]"
				continue
			}
			current[key] = redactValue(child, secrets)
		}
		return current
	case []any:
		for index, child := range current {
			current[index] = redactValue(child, secrets)
		}
		return current
	case string:
		return Redact(current, secrets...)
	default:
		return current
	}
}

func safeMetricKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch key {
	case "input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "total_tokens":
		return true
	default:
		return false
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	if key == "auth" || strings.HasPrefix(key, "auth_") || strings.HasSuffix(key, "_auth") {
		return true
	}
	for _, fragment := range []string{"secret", "token", "password", "credential", "authorization", "private_key", "cookie"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func Purpose(parts ...string) string {
	return fmt.Sprintf("agent-harness:%s", strings.Join(parts, ":"))
}

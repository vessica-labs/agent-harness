package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

const (
	HeaderDelivery  = "Linear-Delivery"
	HeaderEvent     = "Linear-Event"
	HeaderSignature = "Linear-Signature"
	HeaderTimestamp = "Linear-Timestamp"
)

type ParsedWebhook struct {
	Delivery        model.LinearDelivery
	Labels          []string
	HasParent       bool
	Archived        bool
	Cancelled       bool
	CommentID       string
	ParentCommentID string
	CommentBody     string
	ActorID         string
	ActorName       string
	ActorType       string
}

var (
	dependencyLine = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?depends\s+on\s*:?\s*(.+?)\s*$`)
	issueKey       = regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9_]*-[0-9]+\b`)
)

func Verify(headers http.Header, body []byte, secret string, now time.Time, tolerance time.Duration) error {
	if secret == "" {
		return errors.New("Linear webhook secret is not configured")
	}
	timestampText := headers.Get(HeaderTimestamp)
	if timestampText == "" {
		var envelope struct {
			WebhookTimestamp int64 `json:"webhookTimestamp"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.WebhookTimestamp > 0 {
			timestampText = strconv.FormatInt(envelope.WebhookTimestamp, 10)
		}
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return errors.New("invalid Linear-Timestamp")
	}
	stamp := time.UnixMilli(timestamp)
	if timestamp < 1_000_000_000_000 {
		stamp = time.Unix(timestamp, 0)
	}
	if delta := now.Sub(stamp); delta > tolerance || delta < -tolerance {
		return errors.New("stale Linear webhook timestamp")
	}
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get(HeaderSignature)))
	if err != nil {
		return errors.New("invalid Linear-Signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("Linear webhook signature mismatch")
	}
	if headers.Get(HeaderDelivery) == "" {
		return errors.New("missing Linear-Delivery")
	}
	return nil
}

func Parse(headers http.Header, body []byte, receivedAt time.Time) (ParsedWebhook, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return ParsedWebhook{}, fmt.Errorf("decode Linear webhook: %w", err)
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return ParsedWebhook{}, errors.New("Linear webhook data is missing")
	}
	parsed := ParsedWebhook{}
	parsed.Delivery = model.LinearDelivery{
		DeliveryID: stringValue(headers.Get(HeaderDelivery)),
		EventType:  coalesce(headers.Get(HeaderEvent), value(root, "type")),
		Action:     value(root, "action"), IssueID: value(data, "id"),
		IssueKey: value(data, "identifier"), IssueURL: value(data, "url"),
		IssueTitle: value(data, "title"), WorkspaceID: coalesce(value(root, "organizationId"), nestedID(data, "organization")),
		TeamID: nestedID(data, "team"), ProjectID: nestedID(data, "project"),
		ReceivedAt: receivedAt, RawPayload: append([]byte(nil), body...),
	}
	if strings.EqualFold(parsed.Delivery.EventType, "Comment") {
		parsed.CommentID = value(data, "id")
		parsed.ParentCommentID = coalesce(value(data, "parentId"), nestedID(data, "parent"))
		parsed.CommentBody = strings.TrimSpace(value(data, "body"))
		actor, _ := root["actor"].(map[string]any)
		parsed.ActorID, parsed.ActorName, parsed.ActorType = coalesce(value(actor, "id"), value(data, "userId")), value(actor, "name"), strings.ToLower(value(actor, "type"))
		parsed.Delivery.IssueID = coalesce(value(data, "issueId"), nestedID(data, "issue"))
		if parsed.CommentID == "" || parsed.Delivery.IssueID == "" {
			return ParsedWebhook{}, errors.New("Linear comment webhook comment id or issue id is missing")
		}
		hash := sha256.Sum256(body)
		parsed.Delivery.PayloadSHA256 = hex.EncodeToString(hash[:])
		return parsed, nil
	}
	description := value(data, "description")
	parsed.Delivery.FeatureRequest = strings.TrimSpace(parsed.Delivery.IssueTitle + "\n\n" + description)
	parsed.Delivery.Dependencies = DependencyIssueKeys(description)
	hash := sha256.Sum256(body)
	parsed.Delivery.PayloadSHA256 = hex.EncodeToString(hash[:])
	parsed.HasParent = data["parent"] != nil || value(data, "parentId") != ""
	parsed.Archived = data["archivedAt"] != nil || boolValue(data["archived"])
	parsed.Cancelled = data["canceledAt"] != nil || data["cancelledAt"] != nil
	parsed.Labels = labels(data["labels"])
	if parsed.Delivery.IssueID == "" || parsed.Delivery.TeamID == "" {
		return ParsedWebhook{}, errors.New("Linear webhook issue id or team id is missing")
	}
	return parsed, nil
}

// DependencyIssueKeys extracts top-level issue dependencies from explicit
// "Depends on ISSUE-123" instructions in a Linear issue description.
func DependencyIssueKeys(description string) []string {
	seen := map[string]bool{}
	var result []string
	for _, match := range dependencyLine.FindAllStringSubmatch(description, -1) {
		for _, raw := range issueKey.FindAllString(match[1], -1) {
			key := strings.ToUpper(raw)
			if !seen[key] {
				seen[key] = true
				result = append(result, key)
			}
		}
	}
	return result
}

func (p ParsedWebhook) Eligible(triggerLabel string) (bool, string) {
	if !strings.EqualFold(p.Delivery.EventType, "Issue") {
		return false, "not_issue_event"
	}
	if p.Delivery.Action != "create" && p.Delivery.Action != "update" {
		return false, "unsupported_action"
	}
	if p.HasParent {
		return false, "child_issue"
	}
	if p.Archived || p.Cancelled {
		return false, "inactive_issue"
	}
	for _, label := range p.Labels {
		if strings.EqualFold(label, triggerLabel) {
			return true, ""
		}
	}
	return false, "trigger_label_missing"
}

func labels(raw any) []string {
	if object, ok := raw.(map[string]any); ok {
		if nodes, ok := object["nodes"]; ok {
			raw = nodes
		}
	}
	values, _ := raw.([]any)
	result := make([]string, 0, len(values))
	for _, item := range values {
		switch value := item.(type) {
		case string:
			result = append(result, value)
		case map[string]any:
			if name := value["name"]; name != nil {
				result = append(result, fmt.Sprint(name))
			}
		}
	}
	return result
}

func value(object map[string]any, key string) string {
	if object == nil || object[key] == nil {
		return ""
	}
	return fmt.Sprint(object[key])
}

func nestedID(object map[string]any, key string) string {
	nested, _ := object[key].(map[string]any)
	return value(nested, "id")
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func coalesce(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value string) string { return strings.TrimSpace(value) }

package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signedHeaders(body []byte, secret string, now time.Time) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return http.Header{HeaderDelivery: []string{"delivery-1"}, HeaderEvent: []string{"Issue"}, HeaderTimestamp: []string{strconv.FormatInt(now.UnixMilli(), 10)}, HeaderSignature: []string{hex.EncodeToString(mac.Sum(nil))}}
}

func TestVerifyParseAndEligibility(t *testing.T) {
	now := time.Now()
	secret := "webhook-secret"
	body := []byte(`{"action":"update","type":"Issue","organizationId":"org-1","data":{"id":"issue-1","identifier":"ENG-42","title":"Build it","description":"Details","url":"https://linear.app/issue/ENG-42","team":{"id":"team-1"},"project":{"id":"project-1"},"labels":{"nodes":[{"name":"agent-harness"}]}}}`)
	headers := signedHeaders(body, secret, now)
	if err := Verify(headers, body, secret, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := parsed.Eligible("agent-harness"); !ok {
		t.Fatalf("ineligible: %s", reason)
	}
	if parsed.Delivery.IssueKey != "ENG-42" || parsed.Delivery.WorkspaceID != "org-1" {
		t.Fatalf("bad parse: %+v", parsed)
	}
	headers.Set(HeaderSignature, "00")
	if err := Verify(headers, body, secret, now, time.Minute); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestChildAndMissingLabelAreIgnored(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"create","type":"Issue","organizationId":"org-1","data":{"id":"child","identifier":"ENG-43","title":"Child","team":{"id":"team-1"},"parent":{"id":"parent"},"labels":[{"name":"agent-harness"}]}}`)
	parsed, err := Parse(signedHeaders(body, "s", now), body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := parsed.Eligible("agent-harness"); ok || reason != "child_issue" {
		t.Fatalf("got %v %s", ok, reason)
	}
	parsed.HasParent = false
	parsed.Labels = nil
	if ok, reason := parsed.Eligible("agent-harness"); ok || reason != "trigger_label_missing" {
		t.Fatalf("got %v %s", ok, reason)
	}
}

func TestVerifyUsesDocumentedBodyTimestampFallback(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	body := []byte(`{"webhookTimestamp":` + strconv.FormatInt(now.UnixMilli(), 10) + `,"data":{}}`)
	headers := signedHeaders(body, "secret", now)
	headers.Del(HeaderTimestamp)
	if err := Verify(headers, body, "secret", now, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestParseCommentReply(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"create","type":"Comment","organizationId":"org-1","actor":{"id":"user-1","name":"Taylor","type":"user"},"data":{"id":"reply-1","issueId":"issue-1","parentId":"question-comment","body":"Choose the recommended option."}}`)
	headers := signedHeaders(body, "s", now)
	headers.Set(HeaderEvent, "Comment")
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ParentCommentID != "question-comment" || parsed.ActorID != "user-1" || parsed.CommentBody == "" {
		t.Fatalf("bad comment parse: %+v", parsed)
	}
}

func TestDependencyIssueKeysRequireExplicitDependsOnInstruction(t *testing.T) {
	description := "Related context mentions AGE-10.\n\nDepends on AGE-22, age_2-7 and AGE-22\n- Depends on: OPS-9\n"
	dependencies := DependencyIssueKeys(description)
	if strings.Join(dependencies, ",") != "AGE-22,AGE_2-7,OPS-9" {
		t.Fatalf("unexpected dependencies: %v", dependencies)
	}
}

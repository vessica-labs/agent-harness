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

func TestVerifyAndParseIssueUpdate(t *testing.T) {
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
	if parsed.Delivery.IssueKey != "ENG-42" || parsed.Delivery.WorkspaceID != "org-1" {
		t.Fatalf("bad parse: %+v", parsed)
	}
	headers.Set(HeaderSignature, "00")
	if err := Verify(headers, body, secret, now, time.Minute); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestChildIssueShapeIsParsed(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"create","type":"Issue","organizationId":"org-1","data":{"id":"child","identifier":"ENG-43","title":"Child","team":{"id":"team-1"},"parent":{"id":"parent"},"labels":[{"name":"agent-harness"}]}}`)
	parsed, err := Parse(signedHeaders(body, "s", now), body, now)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.HasParent {
		t.Fatal("child issue parent was not parsed")
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

func TestParseDelegatedAgentSession(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"created","type":"AgentSessionEvent","organizationId":"org-1","appUserId":"vessica-user","promptContext":"<issue>Build it</issue>","agentSession":{"id":"session-1","appUserId":"vessica-user","issue":{"id":"issue-1","identifier":"ENG-44","title":"Build it","description":"Details","url":"https://linear.app/ENG-44","teamId":"team-1","delegateId":"vessica-user"}}}`)
	headers := signedHeaders(body, "s", now)
	headers.Set(HeaderEvent, "AgentSessionEvent")
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := parsed.AgentSessionEligible(); !ok {
		t.Fatalf("ineligible: %s", reason)
	}
	if parsed.AgentSessionID != "session-1" || parsed.Delivery.IssueKey != "ENG-44" || parsed.PromptContext == "" {
		t.Fatalf("bad agent session parse: %+v", parsed)
	}
}

func TestParseDelegatedAgentSessionWithGeneratedThreadComment(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"created","type":"AgentSessionEvent","organizationId":"org-1","appUserId":"vessica-user","promptContext":"<issue>Build it</issue>","agentSession":{"id":"session-2","appUserId":"vessica-user","commentId":"session-thread-comment","sourceCommentId":null,"issue":{"id":"issue-2","identifier":"ENG-45","title":"Build it","description":"Details","url":"https://linear.app/ENG-45","teamId":"team-1"}}}`)
	headers := signedHeaders(body, "s", now)
	headers.Set(HeaderEvent, "AgentSessionEvent")
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := parsed.AgentSessionEligible(); !ok {
		t.Fatalf("ineligible: %s", reason)
	}
}

func TestParsePromptedAgentSessionMessage(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"prompted","type":"AgentSessionEvent","organizationId":"org-1","appUserId":"vessica-user","actor":{"id":"user-1","name":"Taylor","type":"user"},"agentSession":{"id":"session-2","appUserId":"vessica-user"},"agentActivity":{"id":"prompt-1","body":"Use the recommended option."}}`)
	headers := signedHeaders(body, "s", now)
	headers.Set(HeaderEvent, "AgentSessionEvent")
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AgentSessionID != "session-2" || parsed.AgentActivityID != "prompt-1" ||
		parsed.AgentPromptBody != "Use the recommended option." || parsed.ActorID != "user-1" {
		t.Fatalf("bad prompted agent session parse: %+v", parsed)
	}
}

func TestMentionedAgentSessionIsNotDispatchEligible(t *testing.T) {
	now := time.Now()
	body := []byte(`{"action":"created","type":"AgentSessionEvent","organizationId":"org-1","appUserId":"vessica-user","agentSession":{"id":"session-3","appUserId":"vessica-user","commentId":"session-thread-comment","sourceCommentId":"mention-comment","issue":{"id":"issue-3","identifier":"ENG-46","title":"Question","url":"https://linear.app/ENG-46","teamId":"team-1"}}}`)
	headers := signedHeaders(body, "s", now)
	headers.Set(HeaderEvent, "AgentSessionEvent")
	parsed, err := Parse(headers, body, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := parsed.AgentSessionEligible(); ok || reason != "not_issue_delegation" {
		t.Fatalf("got %v %s", ok, reason)
	}
}

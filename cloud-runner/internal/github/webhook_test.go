package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestVerifyAndParseMergedPullRequest(t *testing.T) {
	body := []byte(`{"action":"closed","pull_request":{"merged":true,"html_url":"https://github.com/vessica-labs/agent-harness/pull/42"},"repository":{"name":"agent-harness","owner":{"login":"vessica-labs"}}}`)
	headers := http.Header{}
	headers.Set(HeaderDelivery, "delivery-1")
	headers.Set(HeaderEvent, "pull_request")
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	headers.Set(HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if err := Verify(headers, body, "secret"); err != nil {
		t.Fatal(err)
	}
	value, eligible, err := ParsePullRequestMerged(headers, body)
	if err != nil || !eligible || value.Owner != "vessica-labs" || value.Repository != "agent-harness" || value.DeliveryID != "delivery-1" {
		t.Fatalf("unexpected parsed webhook: %+v eligible=%v err=%v", value, eligible, err)
	}
}

func TestNonMergedPullRequestIsIgnored(t *testing.T) {
	headers := http.Header{HeaderEvent: []string{"pull_request"}, HeaderDelivery: []string{"delivery-2"}}
	_, eligible, err := ParsePullRequestMerged(headers, []byte(`{"action":"closed","pull_request":{"merged":false}}`))
	if err != nil || eligible {
		t.Fatalf("eligible=%v err=%v", eligible, err)
	}
}

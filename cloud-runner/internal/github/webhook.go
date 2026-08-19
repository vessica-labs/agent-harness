package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const (
	HeaderDelivery  = "X-GitHub-Delivery"
	HeaderEvent     = "X-GitHub-Event"
	HeaderSignature = "X-Hub-Signature-256"
)

type PullRequestMerged struct {
	DeliveryID  string
	Owner       string
	Repository  string
	PullRequest string
}

func Verify(headers http.Header, body []byte, secret string) error {
	if secret == "" {
		return errors.New("GitHub webhook secret is not configured")
	}
	header := strings.TrimSpace(headers.Get(HeaderSignature))
	if !strings.HasPrefix(header, "sha256=") {
		return errors.New("invalid GitHub webhook signature")
	}
	provided := strings.TrimPrefix(header, "sha256=")
	signature, err := hex.DecodeString(provided)
	if err != nil || len(signature) == 0 {
		return errors.New("invalid GitHub webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("GitHub webhook signature mismatch")
	}
	if strings.TrimSpace(headers.Get(HeaderDelivery)) == "" {
		return errors.New("missing X-GitHub-Delivery")
	}
	return nil
}

func ParsePullRequestMerged(headers http.Header, body []byte) (PullRequestMerged, bool, error) {
	if !strings.EqualFold(headers.Get(HeaderEvent), "pull_request") {
		return PullRequestMerged{}, false, nil
	}
	var payload struct {
		Action      string `json:"action"`
		PullRequest struct {
			Merged  bool   `json:"merged"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return PullRequestMerged{}, false, err
	}
	if !strings.EqualFold(payload.Action, "closed") || !payload.PullRequest.Merged {
		return PullRequestMerged{}, false, nil
	}
	value := PullRequestMerged{DeliveryID: strings.TrimSpace(headers.Get(HeaderDelivery)),
		Owner: payload.Repository.Owner.Login, Repository: payload.Repository.Name,
		PullRequest: payload.PullRequest.HTMLURL}
	if value.Owner == "" || value.Repository == "" || value.PullRequest == "" {
		return PullRequestMerged{}, false, errors.New("GitHub pull request webhook is missing repository or pull request identity")
	}
	return value, true, nil
}

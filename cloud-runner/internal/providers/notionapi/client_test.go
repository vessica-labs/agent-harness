package notionapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreatePageAppendsLargeMarkdownInBoundedBatches(t *testing.T) {
	var appendSizes []int
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/blocks/parent/children":
			_, _ = w.Write([]byte(`{"results":[],"has_more":false}`))
		case r.Method == http.MethodPost && r.URL.Path == "/pages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, exists := body["children"]; exists {
				t.Error("page creation must not send an unbounded children collection")
			}
			_, _ = w.Write([]byte(`{"id":"page","url":"https://notion.test/page"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/blocks/page/children":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/blocks/page/children":
			var body struct {
				Children []any `json:"children"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			appendSizes = append(appendSizes, len(body.Children))
			if len(body.Children) > 100 {
				t.Errorf("append batch has %d children", len(body.Children))
			}
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))
	defer host.Close()
	client := &Client{token: "token", baseURL: host.URL, version: "test", http: &http.Client{Timeout: time.Second}}
	lines := make([]string, 205)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index)
	}
	page, err := client.UpsertPage(context.Background(), "parent", "", "PRD", strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if page.ID != "page" || fmt.Sprint(appendSizes) != "[100 100 5]" {
		t.Fatalf("page=%+v append batches=%v", page, appendSizes)
	}
}

func TestRequestIncludesNotionErrorDetails(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"validation_error","message":"children must contain at most 100 blocks"}`))
	}))
	defer host.Close()
	client := &Client{token: "token", baseURL: host.URL, version: "test", http: &http.Client{Timeout: time.Second}}
	err := client.request(context.Background(), http.MethodGet, "/pages/page", nil, &map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "children must contain at most 100 blocks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

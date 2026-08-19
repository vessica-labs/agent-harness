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

func TestUpdateRestoresPageAndPreservesNestedArtifacts(t *testing.T) {
	var deleted []string
	var update map[string]any
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/pages/hub":
			_ = json.NewDecoder(r.Body).Decode(&update)
			_, _ = w.Write([]byte(`{"id":"hub","url":"https://notion.test/hub"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/blocks/hub/children" && r.URL.Query().Get("start_cursor") == "":
			_, _ = w.Write([]byte(`{"results":[{"id":"body-1","type":"paragraph"},{"id":"prd-page","type":"child_page"}],"has_more":true,"next_cursor":"next"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/blocks/hub/children" && r.URL.Query().Get("start_cursor") == "next":
			_, _ = w.Write([]byte(`{"results":[{"id":"adr-page","type":"child_page"},{"id":"body-2","type":"heading_1"}],"has_more":false}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/blocks/"))
			_, _ = w.Write([]byte(`{"in_trash":true}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/blocks/hub/children":
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
	defer host.Close()
	client := &Client{token: "token", baseURL: host.URL, version: "test", http: &http.Client{Timeout: time.Second}}
	if _, err := client.UpsertPage(context.Background(), "parent", "hub", "Issue hub", "# refreshed"); err != nil {
		t.Fatal(err)
	}
	if update["in_trash"] != false {
		t.Fatalf("existing page was not explicitly restored: %#v", update)
	}
	if fmt.Sprint(deleted) != "[body-1 body-2]" {
		t.Fatalf("nested artifact pages must be preserved, deleted=%v", deleted)
	}
}

func TestRestorePageOnlyChangesTrashState(t *testing.T) {
	var input map[string]any
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&input)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"prd","url":"https://notion.test/prd"}`))
	}))
	defer host.Close()
	client := &Client{token: "token", baseURL: host.URL, version: "test", http: &http.Client{Timeout: time.Second}}
	page, err := client.RestorePage(context.Background(), "prd")
	if err != nil {
		t.Fatal(err)
	}
	if page.ID != "prd" || len(input) != 1 || input["in_trash"] != false {
		t.Fatalf("page=%+v input=%#v", page, input)
	}
}

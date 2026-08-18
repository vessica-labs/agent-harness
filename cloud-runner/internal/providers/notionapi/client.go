package notionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	token, baseURL, version string
	http                    *http.Client
}
type Page struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func New(token string) *Client {
	return &Client{token: token, baseURL: "https://api.notion.com/v1", version: "2026-03-11", http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) ValidateParent(ctx context.Context, pageID string) error {
	var page struct {
		ID       string `json:"id"`
		Archived bool   `json:"archived"`
		InTrash  bool   `json:"in_trash"`
	}
	if err := c.request(ctx, http.MethodGet, "/pages/"+pageID, nil, &page); err != nil {
		return err
	}
	if page.ID == "" || page.Archived || page.InTrash {
		return errors.New("Notion parent page is unavailable")
	}
	return nil
}

func (c *Client) UpsertPage(ctx context.Context, parentID, existingID, title, markdown string) (Page, error) {
	if existingID == "" {
		if found, err := c.findChildPage(ctx, parentID, title); err == nil {
			existingID = found.ID
		}
	}
	if existingID == "" {
		payload := map[string]any{"parent": map[string]string{"page_id": parentID}, "properties": titleProperties(title), "children": markdownBlocks(markdown)}
		var page Page
		return page, c.request(ctx, http.MethodPost, "/pages", payload, &page)
	}
	var page Page
	if err := c.request(ctx, http.MethodPatch, "/pages/"+existingID, map[string]any{"properties": titleProperties(title)}, &page); err != nil {
		return page, err
	}
	if err := c.replaceChildren(ctx, existingID, markdownBlocks(markdown)); err != nil {
		return page, err
	}
	if page.ID == "" {
		page.ID = existingID
	}
	return page, nil
}

func (c *Client) findChildPage(ctx context.Context, parentID, title string) (Page, error) {
	var cursor string
	for {
		path := "/blocks/" + parentID + "/children?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + cursor
		}
		var result struct {
			Results []struct {
				ID, Type  string
				ChildPage struct {
					Title string `json:"title"`
				} `json:"child_page"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := c.request(ctx, http.MethodGet, path, nil, &result); err != nil {
			return Page{}, err
		}
		for _, block := range result.Results {
			if block.Type == "child_page" && block.ChildPage.Title == title {
				return Page{ID: block.ID}, nil
			}
		}
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}
	return Page{}, errors.New("Notion child page not found")
}

func (c *Client) replaceChildren(ctx context.Context, pageID string, blocks []any) error {
	var result struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := c.request(ctx, http.MethodGet, "/blocks/"+pageID+"/children?page_size=100", nil, &result); err != nil {
		return err
	}
	for _, block := range result.Results {
		if err := c.request(ctx, http.MethodDelete, "/blocks/"+block.ID, nil, &map[string]any{}); err != nil {
			return err
		}
	}
	for len(blocks) > 0 {
		count := len(blocks)
		if count > 100 {
			count = 100
		}
		if err := c.request(ctx, http.MethodPatch, "/blocks/"+pageID+"/children", map[string]any{"children": blocks[:count]}, &map[string]any{}); err != nil {
			return err
		}
		blocks = blocks[count:]
	}
	return nil
}

func titleProperties(title string) map[string]any {
	return map[string]any{"title": map[string]any{"title": []any{rich(title)}}}
}
func rich(content string) map[string]any {
	if len(content) > 2000 {
		content = content[:2000]
	}
	return map[string]any{"type": "text", "text": map[string]string{"content": content}}
}

func markdownBlocks(markdown string) []any {
	lines := strings.Split(markdown, "\n")
	blocks := make([]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind := "paragraph"
		text := line
		switch {
		case strings.HasPrefix(line, "### "):
			kind = "heading_3"
			text = strings.TrimPrefix(line, "### ")
		case strings.HasPrefix(line, "## "):
			kind = "heading_2"
			text = strings.TrimPrefix(line, "## ")
		case strings.HasPrefix(line, "# "):
			kind = "heading_1"
			text = strings.TrimPrefix(line, "# ")
		case strings.HasPrefix(line, "- "):
			kind = "bulleted_list_item"
			text = strings.TrimPrefix(line, "- ")
		}
		blocks = append(blocks, map[string]any{"object": "block", "type": kind, kind: map[string]any{"rich_text": []any{rich(text)}}})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"object": "block", "type": "paragraph", "paragraph": map[string]any{"rich_text": []any{rich("No content")}}})
	}
	return blocks
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, _ := json.Marshal(input)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Notion-Version", c.version)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Notion API returned %d", response.StatusCode)
	}
	if output == nil || len(responseBody) == 0 {
		return nil
	}
	return json.Unmarshal(responseBody, output)
}

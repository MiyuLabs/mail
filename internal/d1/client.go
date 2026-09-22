// Package d1 provides a client for the Cloudflare D1 Sync API Worker.
// This is a thin HTTP client that talks to the authenticated REST proxy
// deployed as a Cloudflare Worker. All persistence lives in D1; this client
// provides the Go interface to that data.
package d1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// Client is the D1 Sync API client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new D1 API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Identities
// ──────────────────────────────────────────────────────────────────────────────

// ListIdentities returns all active identities.
func (c *Client) ListIdentities(ctx context.Context) ([]mailpkg.Identity, error) {
	var result struct {
		Identities []mailpkg.Identity `json:"identities"`
	}
	if err := c.get(ctx, "/api/v1/identities", nil, &result); err != nil {
		return nil, err
	}
	return result.Identities, nil
}

// UpsertIdentity creates or updates an identity.
func (c *Client) UpsertIdentity(ctx context.Context, id *mailpkg.Identity) error {
	return c.post(ctx, "/api/v1/identities", id, nil)
}

// ──────────────────────────────────────────────────────────────────────────────
// Threads
// ──────────────────────────────────────────────────────────────────────────────

// ListThreads returns a page of threads for the given identity.
func (c *Client) ListThreads(ctx context.Context, req mailpkg.PageRequest) (*mailpkg.ThreadPage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = mailpkg.DefaultPageSize
	}

	params := url.Values{}
	if req.IdentityID != "" {
		params.Set("identity", req.IdentityID)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))
	if req.Cursor != "" {
		params.Set("cursor", req.Cursor)
	}

	var result mailpkg.ThreadPage
	if err := c.get(ctx, "/api/v1/threads?"+params.Encode(), nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetThread returns a single thread with all its messages.
func (c *Client) GetThread(ctx context.Context, threadID string) (*mailpkg.Thread, error) {
	var thread mailpkg.Thread
	if err := c.get(ctx, "/api/v1/threads/"+threadID, nil, &thread); err != nil {
		return nil, err
	}
	return &thread, nil
}

// UpsertThread creates or updates a thread's metadata.
func (c *Client) UpsertThread(ctx context.Context, t *mailpkg.Thread) error {
	return c.post(ctx, "/api/v1/threads", t, nil)
}

// ──────────────────────────────────────────────────────────────────────────────
// Messages
// ──────────────────────────────────────────────────────────────────────────────

// SaveMessage saves an inbound or outbound message to D1.
func (c *Client) SaveMessage(ctx context.Context, m *mailpkg.Message) error {
	return c.post(ctx, "/api/v1/messages", m, nil)
}

// RecordSent records an outbound message (alias for SaveMessage).
func (c *Client) RecordSent(ctx context.Context, m *mailpkg.Message) error {
	return c.post(ctx, "/api/v1/sent", m, nil)
}

// ──────────────────────────────────────────────────────────────────────────────
// Drafts
// ──────────────────────────────────────────────────────────────────────────────

// ListDrafts returns all drafts for the current user.
func (c *Client) ListDrafts(ctx context.Context) ([]mailpkg.Draft, error) {
	var result struct {
		Drafts []mailpkg.Draft `json:"drafts"`
	}
	if err := c.get(ctx, "/api/v1/drafts", nil, &result); err != nil {
		return nil, err
	}
	return result.Drafts, nil
}

// SaveDraft creates or updates a draft.
func (c *Client) SaveDraft(ctx context.Context, d *mailpkg.Draft) error {
	return c.post(ctx, "/api/v1/drafts", d, nil)
}

// DeleteDraft removes a draft (call after successful send).
func (c *Client) DeleteDraft(ctx context.Context, draftID string) error {
	return c.delete(ctx, "/api/v1/drafts/"+draftID)
}

// ──────────────────────────────────────────────────────────────────────────────
// Sync cursor
// ──────────────────────────────────────────────────────────────────────────────

// PullRequest is the payload for a sync pull request.
type PullRequest struct {
	ClientID  string `json:"client_id"`
	D1Cursor  string `json:"d1_cursor"`
}

// PullResponse contains changes since the last sync.
type PullResponse struct {
	Threads    []mailpkg.Thread   `json:"threads"`
	Messages   []mailpkg.Message  `json:"messages"`
	Drafts     []mailpkg.Draft    `json:"drafts"`
	NextCursor string             `json:"next_cursor"`
	LastIMAPUID uint32            `json:"last_imap_uid"`
}

// Pull fetches all changes since the last sync cursor.
func (c *Client) Pull(ctx context.Context, req PullRequest) (*PullResponse, error) {
	var resp PullResponse
	if err := c.post(ctx, "/api/v1/sync/pull", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PushRequest batches local changes to sync to D1.
type PushRequest struct {
	ClientID  string             `json:"client_id"`
	Threads   []mailpkg.Thread   `json:"threads,omitempty"`
	Messages  []mailpkg.Message  `json:"messages,omitempty"`
	Drafts    []mailpkg.Draft    `json:"drafts,omitempty"`
	LastIMAPUID uint32           `json:"last_imap_uid,omitempty"`
}

// Push sends local state changes to D1.
func (c *Client) Push(ctx context.Context, req PushRequest) error {
	return c.post(ctx, "/api/v1/sync/push", req, nil)
}

// ──────────────────────────────────────────────────────────────────────────────
// HTTP helpers
// ──────────────────────────────────────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path string, _ interface{}, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("d1: request build failed: %w", err)
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("d1: marshal failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("d1: request build failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("d1: request build failed: %w", err)
	}
	return c.do(req, nil)
}

func (c *Client) do(req *http.Request, out interface{}) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("d1: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("d1: API error %d: %s", resp.StatusCode, string(body))
	}

	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("d1: response decode failed: %w", err)
		}
	}
	return nil
}

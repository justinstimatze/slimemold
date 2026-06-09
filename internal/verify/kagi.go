package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// kagiSearchEndpoint is the Kagi Search API endpoint. The token is
// passed via Authorization: Bot <token>; the API returns ranked
// search results with title, url, and snippet.
const kagiSearchEndpoint = "https://kagi.com/api/v0/search"

// kagiClient wraps Kagi's search HTTP API. Token comes from
// KAGI_API_KEY (the canonical name kagiapi, kagimcp, and Kagi's own
// docs use; absent key disables the client. The HTTP client is
// reused so connections pool across calls.
type kagiClient struct {
	token string
	http  *http.Client
}

func newKagiClient() *kagiClient {
	return &kagiClient{
		token: strings.TrimSpace(os.Getenv("KAGI_API_KEY")),
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *kagiClient) enabled() bool {
	return c.token != ""
}

// kagiResponse mirrors the JSON shape Kagi returns. We only need the
// first search-result item (t==0); related-search items (t==1) are
// ignored.
type kagiResponse struct {
	Data []kagiItem `json:"data"`
}

type kagiItem struct {
	T       int    `json:"t"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// search runs a single query and returns the top search result. If
// the response is empty or contains no search items the call returns
// an error — callers treat that as "nothing to inline" and skip.
func (c *kagiClient) search(ctx context.Context, query string) (Reconciled, error) {
	if !c.enabled() {
		return Reconciled{}, fmt.Errorf("kagi: no token configured")
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", "3")
	reqURL := kagiSearchEndpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Reconciled{}, fmt.Errorf("kagi: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Reconciled{}, fmt.Errorf("kagi: http: %w", err)
	}
	// Drain before close so HTTP/1.1 keep-alive can reuse the
	// connection. The non-200 path reads only 1KB and json.Decoder
	// may stop short of the closing brace; in both cases the
	// remaining unread bytes leave the connection un-reusable.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return Reconciled{}, fmt.Errorf("kagi: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var body kagiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Reconciled{}, fmt.Errorf("kagi: decode: %w", err)
	}
	for _, item := range body.Data {
		if item.T != 0 {
			continue
		}
		return Reconciled{
			Query:   query,
			Source:  item.URL,
			Title:   strings.TrimSpace(item.Title),
			Snippet: strings.TrimSpace(item.Snippet),
		}, nil
	}
	return Reconciled{}, fmt.Errorf("kagi: empty result set")
}

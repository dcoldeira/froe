package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// tavilyAPIKeyEnv names the environment variable holding the Tavily API key.
// Read at call time, not at registry construction, so a missing key produces
// an actionable tool error instead of froe refusing to start.
const tavilyAPIKeyEnv = "TAVILY_API_KEY"

// tavilySearchURL is a var, not a const, so tests can point it at a fake
// server instead of making a real network call.
var tavilySearchURL = "https://api.tavily.com/search"

// maxWebSearchResults bounds how many results are returned. Tavily's default
// is already summarized content rather than raw HTML, but five results of
// that is still a meaningful chunk of an 8K local context.
const maxWebSearchResults = 5

// WebSearch queries the open web. Unlike every other tool in this package it
// crosses the machine boundary the rest of froe is built to respect
// (docs/ARCHITECTURE.md's "why it exists" #1) - the request sends a query
// string, never file contents, but it is still traffic leaving the LAN. It is
// therefore Mutating(), gated through the same approval prompt as bash,
// rather than auto-running like grep or read_file.
type WebSearch struct{}

func (WebSearch) Name() string   { return "web_search" }
func (WebSearch) Mutating() bool { return true }
func (WebSearch) Description() string {
	return "Search the public web via Tavily and return summarized results (title, URL, content snippet). " +
		"Requires " + tavilyAPIKeyEnv + " to be set. Use this for anything outside the repo - current " +
		"events, library documentation, error messages, API references. Do not paste file contents into " +
		"the query; a search only needs the question, not the code."
}

func (WebSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What to search for"},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 10, "description": "Default 5"}
  },
  "required": ["query"]
}`)
}

type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

func (WebSearch) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	n := a.MaxResults
	if n <= 0 || n > 10 {
		n = maxWebSearchResults
	}

	apiKey := os.Getenv(tavilyAPIKeyEnv)
	if apiKey == "" {
		return "", fmt.Errorf("%s is not set - get a key at https://tavily.com and export it", tavilyAPIKeyEnv)
	}

	body, err := json.Marshal(tavilyRequest{Query: a.Query, MaxResults: n})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tavily: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Surface Tavily's own message where there is one; a bare status code
		// tells the model nothing it can act on, same reasoning as grep.go.
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return "", fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, msg)
	}

	var tr tavilyResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return "", fmt.Errorf("tavily: unexpected response: %w", err)
	}

	if len(tr.Results) == 0 {
		return fmt.Sprintf("(no results for %q)", a.Query), nil
	}

	var b strings.Builder
	if tr.Answer != "" {
		fmt.Fprintf(&b, "Answer: %s\n\n", tr.Answer)
	}
	for i, r := range tr.Results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

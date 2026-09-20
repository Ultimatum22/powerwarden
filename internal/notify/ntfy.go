package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Ntfy sends notifications via ntfy (https://ntfy.sh or a self-hosted
// instance), the default provider in CLAUDE.md's example config.
type Ntfy struct {
	// URL is the full topic URL, e.g. "https://ntfy.sh/my-topic".
	URL string
	// Token is an optional ntfy access token, sent as a Bearer token.
	Token string

	HTTPClient *http.Client
}

func (n Ntfy) Notify(ctx context.Context, note Notification) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(note.Body))
	if err != nil {
		return fmt.Errorf("notify: build ntfy request: %w", err)
	}
	if note.Title != "" {
		req.Header.Set("Title", note.Title)
	}
	req.Header.Set("Priority", strconv.Itoa(ntfyPriority(note.Priority)))
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}

	client := n.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: ntfy request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: ntfy request to %s returned status %d", n.URL, resp.StatusCode)
	}
	return nil
}

func ntfyPriority(p Priority) int {
	switch p {
	case PriorityHigh:
		return 4
	case PriorityUrgent:
		return 5
	default:
		return 3
	}
}

var _ Notifier = Ntfy{}

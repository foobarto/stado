//go:build !airgap

package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
	"golang.org/x/net/html"
)

func (WebFetchTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", p.URL, nil)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	req.Header.Set("User-Agent", "stado/0.1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return tool.Result{Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	content := htmlToMarkdown(string(body))
	content = budget.TruncateBytes(content, budget.WebfetchBytes,
		"narrow the URL path or target a specific page section")

	return tool.Result{Content: content}, nil
}

func htmlToMarkdown(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	extractText(doc, &b)
	return strings.TrimSpace(b.String())
}

func extractText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		txt := strings.TrimSpace(n.Data)
		if txt != "" {
			b.WriteString(txt)
			b.WriteString(" ")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Data == "script" || c.Data == "style" || c.Data == "noscript" {
			continue
		}
		if c.Data == "p" || c.Data == "div" || c.Data == "h1" || c.Data == "h2" || c.Data == "h3" {
			b.WriteString("\n\n")
		}
		if c.Data == "br" {
			b.WriteString("\n")
		}
		extractText(c, b)
	}
}

//go:build !airgap

package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
	"golang.org/x/net/html"
)

const maxResponseBytes = 512 * 1024

func (WebFetchTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p Args
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	u, err := validateFetchURL(p.URL)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	initialHost := strings.ToLower(u.Hostname())

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	req.Header.Set("User-Agent", "stado/0.1.0")

	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("webfetch: stopped after %d redirects", len(via))
			}
			if err := validateRedirectURL(req.URL, initialHost); err != nil {
				return err
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return tool.Result{Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)}, nil
	}

	body, bodyTruncated, err := readResponseBody(resp.Body)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	content := htmlToMarkdown(string(body))
	if bodyTruncated {
		content += fmt.Sprintf("\n[truncated: response body exceeded %d bytes]", maxResponseBytes)
	}
	content = budget.TruncateBytes(content, budget.WebfetchBytes,
		"narrow the URL path or target a specific page section")

	return tool.Result{Content: content}, nil
}

func validateFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webfetch: unsupported URL scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("webfetch: URL must include a host")
	}
	return u, nil
}

func validateRedirectURL(u *url.URL, initialHost string) error {
	if u == nil {
		return fmt.Errorf("webfetch: redirect missing URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webfetch: redirect to unsupported URL scheme %q denied", u.Scheme)
	}
	if strings.ToLower(u.Hostname()) != initialHost {
		return fmt.Errorf("webfetch: redirect to different host %q denied", u.Hostname())
	}
	return nil
}

func readResponseBody(r io.Reader) ([]byte, bool, error) {
	var b bytes.Buffer
	if _, err := b.ReadFrom(io.LimitReader(r, maxResponseBytes+1)); err != nil {
		return nil, false, err
	}
	body := b.Bytes()
	if len(body) > maxResponseBytes {
		return body[:maxResponseBytes], true, nil
	}
	return body, false, nil
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

package onejav

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"

	"jacred/config"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "onejav.com"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "onejav.com" }

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	escaped := strings.ReplaceAll(strings.TrimSpace(query), " ", "%20")
	searchURL := fmt.Sprintf("https://%s/search/%s", t.cfg.Domain, escaped)

	body, err := t.fetch(searchURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Results = parsePage(body, "https://"+t.cfg.Domain)
	result.Count = len(result.Results)
	return result
}

func (t *Tracker) fetch(rawURL string) (string, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parsePage finds all <div class="card mb-3"> blocks and extracts torrent info.
func parsePage(content, base string) []tracker.Torrent {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	var torrents []tracker.Torrent
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "card") && hasClass(n, "mb-3") {
			if t := extractCard(n, base); t != nil {
				torrents = append(torrents, *t)
			}
			return // don't recurse into card
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return torrents
}

func extractCard(card *html.Node, base string) *tracker.Torrent {
	var t tracker.Torrent

	// Title: <h5 class="title ..."><a href="/torrent/...">NAME</a>
	h5 := findNode(card, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "h5" && hasClass(n, "title")
	})
	if h5 == nil {
		return nil
	}
	titleLink := findNode(h5, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.HasPrefix(getAttr(n, "href"), "/torrent/") &&
			!strings.Contains(getAttr(n, "href"), "/download/")
	})
	if titleLink == nil {
		return nil
	}
	t.Name = cleanText(extractText(titleLink))
	t.URL = base + getAttr(titleLink, "href")

	// Size: <span class="is-size-6 ..."> inside h5
	sizeSpan := findNode(h5, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "span" && hasClass(n, "is-size-6")
	})
	if sizeSpan != nil {
		t.Size = cleanText(extractText(sizeSpan))
	}

	// Date: <a href="/YYYY/MM/DD"> — extract from href directly
	dateLink := findNode(card, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "a" {
			return false
		}
		href := getAttr(n, "href")
		parts := strings.Split(strings.Trim(href, "/"), "/")
		return len(parts) == 3 && len(parts[0]) == 4
	})
	if dateLink != nil {
		href := getAttr(dateLink, "href")
		parts := strings.Split(strings.Trim(href, "/"), "/")
		t.Date = parts[0] + "-" + parts[1] + "-" + parts[2]
	}

	// Download link: <a href="/torrent/.../download/....torrent">
	dlLink := findNode(card, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "a" {
			return false
		}
		href := getAttr(n, "href")
		return strings.Contains(href, "/download/") && strings.HasSuffix(href, ".torrent")
	})
	if dlLink == nil {
		return nil
	}
	t.Magnet = base + getAttr(dlLink, "href")

	return &t
}

// ─── DOM helpers ─────────────────────────────────────────────────────────────

func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findNode(c, match); r != nil {
			return r
		}
	}
	return nil
}

func hasClass(n *html.Node, cls string) bool {
	return strings.Contains(" "+getAttr(n, "class")+" ", " "+cls+" ")
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func extractText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

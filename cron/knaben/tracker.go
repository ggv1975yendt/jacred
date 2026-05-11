package knaben

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"jacred/config"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var magnetHashRe = regexp.MustCompile(`(?i)xt=urn:btih:([a-fA-F0-9]{40}|[a-zA-Z2-7]{32})`)

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "knaben.org"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "knaben.org" }

func (t *Tracker) Search(query string, sort int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	sortField := "date"
	if sort == 2 {
		sortField = "seeders"
	}

	q := strings.TrimSpace(query)
	if q == "" {
		result.Error = "empty query"
		return result
	}

	searchURL := fmt.Sprintf("https://%s/search/%s/0/1/%s?hideXXX",
		t.cfg.Domain, url.PathEscape(q), sortField)

	body, err := t.fetch(searchURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Results = parsePage(body)
	result.Count = len(result.Results)
	return result
}

func (t *Tracker) fetch(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
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

// parsePage finds <tr class="text-nowrap border-start"> rows and extracts torrent data.
func parsePage(content string) []tracker.Torrent {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	var torrents []tracker.Torrent
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" &&
			hasClass(n, "text-nowrap") && hasClass(n, "border-start") {
			if t := extractRow(n); t != nil {
				torrents = append(torrents, *t)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return torrents
}

func extractRow(tr *html.Node) *tracker.Torrent {
	// Collect <td> children
	var cells []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "td" {
			cells = append(cells, c)
		}
	}
	// Expect at least 6 cells: category, title, size, date, seeds, peers
	if len(cells) < 6 {
		return nil
	}

	var t tracker.Torrent

	// Cell 1 (text-wrap): title and magnet from first <a href="magnet:..."> with title attr
	titleCell := cells[1]
	titleLink := findNode(titleCell, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.HasPrefix(getAttr(n, "href"), "magnet:") &&
			getAttr(n, "title") != ""
	})
	if titleLink == nil {
		return nil
	}
	t.Name = strings.TrimSpace(getAttr(titleLink, "title"))
	t.Magnet = getAttr(titleLink, "href")

	// Info hash from span[data-copy-to-clipboard-content]
	hashSpan := findNode(titleCell, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "span" &&
			getAttr(n, "data-copy-to-clipboard-content") != ""
	})
	if hashSpan != nil {
		t.InfoHash = strings.ToUpper(getAttr(hashSpan, "data-copy-to-clipboard-content"))
	} else if t.Magnet != "" {
		if m := magnetHashRe.FindStringSubmatch(t.Magnet); len(m) > 1 {
			t.InfoHash = strings.ToUpper(m[1])
		}
	}

	// Cell 2: size — text content
	t.Size = cleanText(extractText(cells[2]))

	// Cell 3: date — text content is already YYYY-MM-DD
	t.Date = cleanText(extractText(cells[3]))

	// Cell 4: seeds
	t.Seeds = cleanText(extractText(cells[4]))

	// Cell 5: peers
	t.Peers = cleanText(extractText(cells[5]))

	// Cell 6 (optional): source tracker link
	if len(cells) >= 7 {
		sourceLink := findNode(cells[6], func(n *html.Node) bool {
			return n.Type == html.ElementNode && n.Data == "a"
		})
		if sourceLink != nil {
			t.Source = cleanText(extractText(sourceLink))
			t.URL = getAttr(sourceLink, "href")
		}
	}

	if t.Name == "" || t.Magnet == "" {
		return nil
	}
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

package xxxtor

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
	"jacred/dateutil"
	"jacred/tracker"
)

var reHash = regexp.MustCompile(`btih:([a-fA-F0-9]{32,40})`)

type Tracker struct {
	cfg config.TrackerConfig
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "xxxtor.com"
	}
	return &Tracker{cfg: cfg}
}

func (t *Tracker) Name() string {
	return "xxxtor.com"
}

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	searchURL := fmt.Sprintf("https://%s/b.php?search=%s", t.cfg.Domain, url.QueryEscape(query))

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Results = parseHTML(string(body), "https://"+t.cfg.Domain)
	result.Count = len(result.Results)
	return result
}

// ─── HTML parsing ───────────────────────────────────────────────────────────

func parseHTML(htmlContent, baseURL string) []tracker.Torrent {
	var torrents []tracker.Torrent

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return torrents
	}

	indexDiv := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "div" && getAttr(n, "id") == "index"
	})
	if indexDiv == nil {
		return torrents
	}

	var rows []*html.Node
	collectNodes(indexDiv, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "tr" {
			return false
		}
		cls := getAttr(n, "class")
		return cls == "gai" || cls == "tum"
	}, &rows)

	for _, tr := range rows {
		if t := extractTorrent(tr, baseURL); t != nil {
			torrents = append(torrents, *t)
		}
	}

	return torrents
}

func extractTorrent(tr *html.Node, baseURL string) *tracker.Torrent {
	var tds []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "td" {
			tds = append(tds, c)
		}
	}
	if len(tds) < 4 {
		return nil
	}

	var t tracker.Torrent

	// td[0]: date
	t.Date = dateutil.Normalize(cleanText(extractText(tds[0])))

	// td[1]: magnet link + title link (colspan=2)
	var links []*html.Node
	collectNodes(tds[1], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a"
	}, &links)
	for _, a := range links {
		href := getAttr(a, "href")
		switch {
		case strings.HasPrefix(href, "magnet:"):
			t.Magnet = href
			if m := reHash.FindStringSubmatch(href); len(m) > 1 {
				t.InfoHash = strings.ToUpper(m[1])
			}
		case strings.HasPrefix(href, "/torrent/"):
			t.Name = cleanText(extractText(a))
			t.URL = baseURL + href
		}
	}

	// td[2]: size
	t.Size = cleanText(extractText(tds[2]))

	// td[3]: seeds (span.green) and leechers (span.red)
	if span := findNode(tds[3], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "span" && getAttr(n, "class") == "green"
	}); span != nil {
		t.Seeds = cleanText(extractText(span))
	}
	if span := findNode(tds[3], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "span" && getAttr(n, "class") == "red"
	}); span != nil {
		t.Peers = cleanText(extractText(span))
	}

	if t.Name == "" || t.Magnet == "" {
		return nil
	}
	return &t
}

// ─── Helpers ────────────────────────────────────────────────────────────────

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

// collectNodes appends all nodes matching the predicate (does not recurse into matched nodes).
func collectNodes(n *html.Node, match func(*html.Node) bool, result *[]*html.Node) {
	if match(n) {
		*result = append(*result, n)
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectNodes(c, match, result)
	}
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

package omagnet

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"jacred/config"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var reHash = regexp.MustCompile(`btih:([a-fA-F0-9]{32,40})`)

type searchEntry struct {
	path, title, size string
}

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "16mag.net"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "16mag.net" }

func (t *Tracker) get(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	searchURL := "https://" + t.cfg.Domain + "/search?q=" + url.QueryEscape(query)
	body, err := t.get(searchURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	entries := parseSearchPage(string(body))
	if len(entries) == 0 {
		return result
	}

	// Fetch detail pages concurrently, max 5 at a time.
	sem := make(chan struct{}, 5)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		torrents []tracker.Torrent
	)
	for _, e := range entries {
		wg.Add(1)
		go func(e searchEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			detailURL := "https://" + t.cfg.Domain + e.path
			dBody, err := t.get(detailURL)
			if err != nil {
				return
			}
			magnet, hash, date := parseDetailPage(string(dBody))
			if magnet == "" {
				return
			}
			tor := tracker.Torrent{
				Name:     e.title,
				URL:      detailURL,
				Magnet:   magnet,
				InfoHash: hash,
				Size:     e.size,
				Date:     date,
			}
			mu.Lock()
			torrents = append(torrents, tor)
			mu.Unlock()
		}(e)
	}
	wg.Wait()

	result.Results = torrents
	result.Count = len(torrents)
	return result
}

// ─── Search page parsing ────────────────────────────────────────────────────

func parseSearchPage(content string) []searchEntry {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	table := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "table" &&
			strings.Contains(getAttr(n, "class"), "file-list")
	})
	if table == nil {
		return nil
	}

	var entries []searchEntry
	walkRows(table, func(tr *html.Node) {
		if e := extractSearchEntry(tr); e != nil {
			entries = append(entries, *e)
		}
	})
	return entries
}

func walkRows(n *html.Node, fn func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.Data == "tr" {
			fn(c)
		} else {
			walkRows(c, fn)
		}
	}
}

func extractSearchEntry(tr *html.Node) *searchEntry {
	var tds []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "td" {
			tds = append(tds, c)
		}
	}
	if len(tds) < 1 {
		return nil
	}

	// First td: <a href="/!XXXX">Title<p class="sample">...</p></a>
	a := findNode(tds[0], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.HasPrefix(getAttr(n, "href"), "/!")
	})
	if a == nil {
		return nil
	}
	path := getAttr(a, "href")
	title := directText(a)
	if title == "" {
		return nil
	}

	// Find size from td with class "td-size"
	var size string
	for _, td := range tds[1:] {
		if strings.Contains(getAttr(td, "class"), "td-size") {
			size = cleanText(extractText(td))
			break
		}
	}

	return &searchEntry{path: path, title: title, size: size}
}

// directText returns only direct text nodes of n (excludes child element text).
func directText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return cleanText(b.String())
}

// ─── Detail page parsing ────────────────────────────────────────────────────

func parseDetailPage(content string) (magnet, hash, date string) {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return
	}

	// Magnet: <input id="input-magnet" value="magnet:...">
	input := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "input" &&
			getAttr(n, "id") == "input-magnet"
	})
	if input != nil {
		magnet = getAttr(input, "value")
		if m := reHash.FindStringSubmatch(magnet); len(m) > 1 {
			hash = strings.ToUpper(m[1])
		}
	}

	// Date: <dt>Date :</dt> followed by <dd>yyyy-mm-dd HH:MM:SS</dd>
	dtNode := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "dt" &&
			strings.TrimSpace(extractText(n)) == "Date :"
	})
	if dtNode != nil {
		for sib := dtNode.NextSibling; sib != nil; sib = sib.NextSibling {
			if sib.Type == html.ElementNode && sib.Data == "dd" {
				full := cleanText(extractText(sib))
				if len(full) >= 10 {
					date = full[:10]
				} else {
					date = full
				}
				break
			}
		}
	}

	return
}

// ─── DOM helpers ────────────────────────────────────────────────────────────

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

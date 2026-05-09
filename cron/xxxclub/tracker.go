package xxxclub

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"jacred/config"
	"jacred/dateutil"
	"jacred/tracker"
)

var defaultCategories = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}

var magnetTrackers = []string{
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://explodie.org:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://exodus.desync.com:6969/announce",
}

type Tracker struct {
	cfg config.TrackerConfig
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "xxxclub.to"
	}
	if len(cfg.Categories) == 0 {
		cfg.Categories = defaultCategories
	}
	return &Tracker{cfg: cfg}
}

func (t *Tracker) Name() string {
	return "xxxclub.to"
}

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	encoded := url.PathEscape(query)

	var mu sync.Mutex
	var wg sync.WaitGroup
	var allTorrents []tracker.Torrent

	for _, cat := range t.cfg.Categories {
		wg.Add(1)
		go func(cat string) {
			defer wg.Done()

			searchURL := fmt.Sprintf("https://%s/torrents/search/%s/%s", t.cfg.Domain, cat, encoded)

			client := &http.Client{Timeout: 15 * time.Second}
			req, err := http.NewRequest("GET", searchURL, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")

			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return
			}

			torrents := parseHTML(string(body), "https://"+t.cfg.Domain)

			mu.Lock()
			allTorrents = append(allTorrents, torrents...)
			mu.Unlock()
		}(cat)
	}

	wg.Wait()

	result.Results = removeDuplicates(allTorrents)
	result.Count = len(result.Results)
	return result
}

// ─── HTML parsing ──────────────────────────────────────────

func parseHTML(htmlContent, baseURL string) []tracker.Torrent {
	var torrents []tracker.Torrent

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return torrents
	}

	list := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "ul" && hasClass(n, "tsearch")
	})
	if list == nil {
		return torrents
	}

	firstLi := true
	for li := list.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.Data != "li" {
			continue
		}
		if firstLi {
			firstLi = false // skip header row
			continue
		}
		if t := extractTorrent(li, baseURL); t != nil {
			torrents = append(torrents, *t)
		}
	}

	return torrents
}

func extractTorrent(li *html.Node, baseURL string) *tracker.Torrent {
	var t tracker.Torrent

	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "span" {
			continue
		}
		cls := getAttr(c, "class")
		switch {
		case strings.Contains(cls, "toral"):
			extractNameAndHash(c, baseURL, &t)
		case strings.Contains(cls, "adde"):
			t.Date = dateutil.Normalize(cleanText(extractText(c)))
		case strings.Contains(cls, "siz"):
			t.Size = cleanText(extractText(c))
		case strings.Contains(cls, "see"):
			t.Seeds = cleanText(extractText(c))
		case strings.Contains(cls, "lee"):
			t.Peers = cleanText(extractText(c))
		}
	}

	if t.Name == "" || t.InfoHash == "" {
		return nil
	}

	t.Magnet = buildMagnet(t.InfoHash, t.Name)
	return &t
}

// extractNameAndHash находит видимую ссылку в span.toral и извлекает название,
// URL страницы и info hash (встроен в атрибут id как "#i"+hash).
func extractNameAndHash(toralSpan *html.Node, baseURL string, t *tracker.Torrent) {
	for c := toralSpan.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "a" {
			continue
		}
		href := getAttr(c, "href")
		if !strings.HasPrefix(href, "/torrents/details/") {
			continue
		}
		// Skip the hidden promotional link
		if strings.Contains(getAttr(c, "style"), "left:-99999px") {
			continue
		}

		id := getAttr(c, "id")
		// id format: "#i" + 40-char hex SHA1
		if !strings.HasPrefix(id, "#i") || len(id) < 42 {
			continue
		}
		hash := strings.ToUpper(id[2:])

		name := cleanText(extractText(c))
		if name == "" {
			continue
		}

		t.Name = name
		t.InfoHash = hash
		t.URL = baseURL + href
		return
	}
}

func buildMagnet(hash, name string) string {
	var sb strings.Builder
	sb.WriteString("magnet:?xt=urn:btih:")
	sb.WriteString(strings.ToLower(hash))
	sb.WriteString("&dn=")
	sb.WriteString(url.QueryEscape(name))
	for _, tr := range magnetTrackers {
		sb.WriteString("&tr=")
		sb.WriteString(url.QueryEscape(tr))
	}
	return sb.String()
}

// ─── Helpers ───────────────────────────────────────────────

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

func removeDuplicates(torrents []tracker.Torrent) []tracker.Torrent {
	seen := make(map[string]bool)
	var result []tracker.Torrent
	for _, t := range torrents {
		key := t.InfoHash
		if key == "" {
			key = t.Name
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, t)
		}
	}
	return result
}

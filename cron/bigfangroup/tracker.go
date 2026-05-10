package bigfangroup

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"

	"jacred/config"
	"jacred/dateutil"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var magnetTrackers = []string{
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.filemail.com:6969/announce",
}

type searchEntry struct {
	id, title, size, seeds, peers, date string
}

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "www.bigfangroup.org"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "bigfangroup.org" }

func (t *Tracker) get(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	cats := t.cfg.Categories
	if len(cats) == 0 {
		cats = []string{""}
	}

	var (
		mu         sync.Mutex
		catWg      sync.WaitGroup
		allEntries []searchEntry
	)

	for _, cat := range cats {
		catWg.Add(1)
		go func(cat string) {
			defer catWg.Done()
			searchURL := "https://" + t.cfg.Domain + "/browse.php?search=" + url.QueryEscape(query)
			if cat != "" {
				searchURL += "&cat=" + cat
			}
			body, err := t.get(searchURL)
			if err != nil {
				return
			}
			decoded, err := charmap.Windows1251.NewDecoder().Bytes(body)
			if err != nil {
				return
			}
			entries := parseSearchPage(string(decoded))
			mu.Lock()
			allEntries = append(allEntries, entries...)
			mu.Unlock()
		}(cat)
	}
	catWg.Wait()

	allEntries = dedup(allEntries)
	if len(allEntries) == 0 {
		return result
	}

	// Download .torrent files concurrently to extract infohash, max 5 at a time.
	sem := make(chan struct{}, 5)
	var (
		torWg    sync.WaitGroup
		torMu    sync.Mutex
		torrents []tracker.Torrent
	)
	for _, e := range allEntries {
		torWg.Add(1)
		go func(e searchEntry) {
			defer torWg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			hash := t.fetchHash(e.id)
			if hash == "" {
				return
			}
			tor := tracker.Torrent{
				Name:     e.title,
				URL:      "https://" + t.cfg.Domain + "/details.php?id=" + e.id,
				Magnet:   buildMagnet(hash, e.title),
				Size:     e.size,
				Seeds:    e.seeds,
				Peers:    e.peers,
				Date:     e.date,
				InfoHash: hash,
			}
			torMu.Lock()
			torrents = append(torrents, tor)
			torMu.Unlock()
		}(e)
	}
	torWg.Wait()

	result.Results = torrents
	result.Count = len(torrents)
	return result
}

func (t *Tracker) fetchHash(id string) string {
	body, err := t.get("https://" + t.cfg.Domain + "/download.php?id=" + id)
	if err != nil || !isTorrent(body) {
		return ""
	}
	return extractInfoHash(body)
}

func dedup(entries []searchEntry) []searchEntry {
	seen := make(map[string]bool)
	out := entries[:0]
	for _, e := range entries {
		if !seen[e.id] {
			seen[e.id] = true
			out = append(out, e)
		}
	}
	return out
}

// ─── HTML parsing ────────────────────────────────────────────────────────────

func parseSearchPage(content string) []searchEntry {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	tbody := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "tbody" && getAttr(n, "id") == "highlighted"
	})
	if tbody == nil {
		return nil
	}

	var entries []searchEntry
	for c := tbody.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "tr" {
			continue
		}
		if e := extractEntry(c); e != nil {
			entries = append(entries, *e)
		}
	}
	return entries
}

func extractEntry(tr *html.Node) *searchEntry {
	var tds []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "td" {
			tds = append(tds, c)
		}
	}
	if len(tds) < 8 {
		return nil
	}

	var e searchEntry

	// tds[1]: title cell — full title in <acronym title="...">, ID from href
	titleLink := findNode(tds[1], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.HasPrefix(getAttr(n, "href"), "details.php?id=") &&
			!strings.Contains(getAttr(n, "href"), "&")
	})
	if titleLink == nil {
		return nil
	}
	e.id = strings.TrimPrefix(getAttr(titleLink, "href"), "details.php?id=")
	if acronym := findNode(titleLink, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "acronym"
	}); acronym != nil {
		e.title = cleanText(getAttr(acronym, "title"))
	}
	if e.title == "" {
		e.title = cleanText(extractText(titleLink))
	}

	// tds[3]: icons + date — date in <img src="...time.png" title="28 сентября 2025 в ...">
	if timeImg := findNode(tds[3], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "img" &&
			strings.Contains(getAttr(n, "src"), "time.png")
	}); timeImg != nil {
		raw := getAttr(timeImg, "title")
		if raw == "" {
			raw = getAttr(timeImg, "alt")
		}
		e.date = dateutil.Normalize(raw)
	}

	// tds[5]: size text (e.g. "73.02 GB")
	e.size = cleanText(extractText(tds[5]))

	// tds[6]: seeds — inside <a href="...&toseeders=1">
	if a := findNode(tds[6], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.Contains(getAttr(n, "href"), "toseeders=1")
	}); a != nil {
		e.seeds = cleanText(extractText(a))
	}

	// tds[7]: leechers — inside <a href="...&todlers=1">
	if a := findNode(tds[7], func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			strings.Contains(getAttr(n, "href"), "todlers=1")
	}); a != nil {
		e.peers = cleanText(extractText(a))
	}

	if e.id == "" || e.title == "" {
		return nil
	}
	return &e
}

// ─── Torrent / bencode ───────────────────────────────────────────────────────

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

func isTorrent(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c == 'd'
	}
	return false
}

func extractInfoHash(torrent []byte) string {
	key := []byte("4:info")
	idx := bytes.Index(torrent, key)
	if idx == -1 {
		return ""
	}
	start := idx + len(key)
	end, err := bencEnd(torrent, start)
	if err != nil || end > len(torrent) {
		return ""
	}
	h := sha1.Sum(torrent[start:end])
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

func bencEnd(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, fmt.Errorf("out of bounds at %d", pos)
	}
	switch data[pos] {
	case 'd', 'l':
		pos++
		for pos < len(data) && data[pos] != 'e' {
			var err error
			pos, err = bencEnd(data, pos)
			if err != nil {
				return 0, err
			}
		}
		if pos >= len(data) {
			return 0, fmt.Errorf("unterminated container")
		}
		return pos + 1, nil
	case 'i':
		e := bytes.IndexByte(data[pos:], 'e')
		if e == -1 {
			return 0, fmt.Errorf("unterminated int")
		}
		return pos + e + 1, nil
	default:
		colon := bytes.IndexByte(data[pos:], ':')
		if colon == -1 {
			return 0, fmt.Errorf("no colon at %d", pos)
		}
		n := 0
		for _, c := range data[pos : pos+colon] {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("bad length char %q", c)
			}
			n = n*10 + int(c-'0')
		}
		end := pos + colon + 1 + n
		if end > len(data) {
			return 0, fmt.Errorf("string overflow")
		}
		return end, nil
	}
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

package kinozal

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"

	"jacred/config"
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

// defaultCategories — категории kinozal.tv (c=0 означает «все»).
var defaultCategories = []string{"0"}

type searchEntry struct {
	id, title, size, seeds, peers, date string
}

type Tracker struct {
	cfg    config.TrackerConfig
	mu     sync.Mutex
	client *http.Client
	ready  bool
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "kinozal.tv"
	}
	if len(cfg.Categories) == 0 {
		cfg.Categories = defaultCategories
	}
	jar, _ := cookiejar.New(nil)
	return &Tracker{
		cfg: cfg,
		client: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		},
	}
}

func (t *Tracker) Name() string { return "kinozal.tv" }

// ─── Auth ──────────────────────────────────────────────────

func (t *Tracker) ensureReady() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return nil
	}
	return t.doLogin()
}

// doLogin must be called with t.mu held.
func (t *Tracker) doLogin() error {
	if t.cfg.Username == "" || t.cfg.Password == "" {
		return fmt.Errorf("username/password not configured")
	}
	form := url.Values{
		"username": {t.cfg.Username},
		"password": {t.cfg.Password},
		"returnto": {""},
	}
	req, err := http.NewRequest("POST",
		"https://"+t.cfg.Domain+"/takelogin.php",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://"+t.cfg.Domain+"/")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	t.ready = true
	return nil
}

// relogin resets the ready flag and logs in again (called when session expires).
func (t *Tracker) relogin() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ready = false
	return t.doLogin()
}

// ─── HTTP helper ───────────────────────────────────────────

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

// ─── Search ────────────────────────────────────────────────

func (t *Tracker) Search(query string, _ int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	if err := t.ensureReady(); err != nil {
		result.Error = "авторизация: " + err.Error()
		return result
	}

	// 1. Собираем записи из всех категорий параллельно.
	var (
		entriesMu sync.Mutex
		allEntries []searchEntry
		catWg      sync.WaitGroup
	)
	for _, cat := range t.cfg.Categories {
		catWg.Add(1)
		go func(cat string) {
			defer catWg.Done()
			searchURL := "https://" + t.cfg.Domain + "/browse.php" +
				"?s=" + url.QueryEscape(query) +
				"&g=0&c=" + cat + "&v=0&d=0&w=0&t=0&f=0"

			body, err := t.get(searchURL)
			if err != nil {
				return
			}
			decoded, err := charmap.Windows1251.NewDecoder().Bytes(body)
			if err != nil {
				return
			}
			entries := parseSearchPage(string(decoded))

			entriesMu.Lock()
			allEntries = append(allEntries, entries...)
			entriesMu.Unlock()
		}(cat)
	}
	catWg.Wait()

	allEntries = deduplicateEntries(allEntries)
	if len(allEntries) == 0 {
		return result
	}

	// 2. Скачиваем .torrent файлы и извлекаем infohash, не более 5 одновременно.
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

func deduplicateEntries(entries []searchEntry) []searchEntry {
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

// fetchHash downloads the .torrent file for a given ID and returns its SHA1 infohash.
// A torrent file starts with 'd' (bencode dict). If we get HTML instead, the
// session has expired — we re-login once and retry.
func (t *Tracker) fetchHash(id string) string {
	dlURL := "https://" + t.cfg.Domain + "/download.php?id=" + id

	body, err := t.get(dlURL)
	if err != nil || len(body) == 0 {
		return ""
	}

	if !isTorrent(body) {
		// Session likely expired — try to re-login and fetch again.
		if err := t.relogin(); err != nil {
			return ""
		}
		body, err = t.get(dlURL)
		if err != nil || !isTorrent(body) {
			return ""
		}
	}

	return extractInfoHash(body)
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

// ─── Bencode infohash extraction ───────────────────────────

// extractInfoHash computes the SHA1 of the bencoded "info" dict in a torrent file.
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
	default: // string: N:bytes
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

// ─── HTML parsing ──────────────────────────────────────────

func parseSearchPage(content string) []searchEntry {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	table := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "table" && hasClass(n, "t_peer")
	})
	if table == nil {
		return nil
	}

	var entries []searchEntry
	walkTR(table, func(tr *html.Node) {
		if strings.Contains(getAttr(tr, "class"), "bg") {
			if e := extractEntry(tr); e != nil {
				entries = append(entries, *e)
			}
		}
	})
	return entries
}

// walkTR calls fn for every <tr> descendant of n.
func walkTR(n *html.Node, fn func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.Data == "tr" {
			fn(c)
		} else {
			walkTR(c, fn)
		}
	}
}

// extractEntry parses a result <tr> row.
// Column layout: bt | nam | s(comments) | s(size) | sl_s | sl_p | s(date) | sl
func extractEntry(tr *html.Node) *searchEntry {
	var e searchEntry
	sCount := 0

	for td := tr.FirstChild; td != nil; td = td.NextSibling {
		if td.Type != html.ElementNode || td.Data != "td" {
			continue
		}
		cls := getAttr(td, "class")
		switch cls {
		case "nam":
			a := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "a" &&
					strings.HasPrefix(getAttr(n, "href"), "/details.php?id=")
			})
			if a != nil {
				href := getAttr(a, "href")
				if i := strings.Index(href, "id="); i != -1 {
					e.id = href[i+3:]
				}
				e.title = cleanText(extractText(a))
			}
		case "s":
			sCount++
			switch sCount {
			case 2:
				e.size = normalizeSize(cleanText(extractText(td)))
			case 3:
				e.date = cleanText(extractText(td))
			}
		case "sl_s":
			e.seeds = cleanText(extractText(td))
		case "sl_p":
			e.peers = cleanText(extractText(td))
		}
	}

	if e.id == "" || e.title == "" {
		return nil
	}
	return &e
}

// normalizeSize converts Russian storage unit abbreviations to English.
func normalizeSize(s string) string {
	s = strings.ReplaceAll(s, "ТБ", "TB")
	s = strings.ReplaceAll(s, "ГБ", "GB")
	s = strings.ReplaceAll(s, "МБ", "MB")
	s = strings.ReplaceAll(s, "КБ", "KB")
	return s
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

// ─── DOM helpers ───────────────────────────────────────────

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

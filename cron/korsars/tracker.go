package korsars

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
	"jacred/dateutil"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var reHash = regexp.MustCompile(`btih:([a-fA-F0-9]{32,40})`)

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "korsars.pro"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "korsars.pro" }

func (t *Tracker) post(searchURL string, body url.Values) ([]byte, error) {
	req, err := http.NewRequest("POST", searchURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Referer", "https://"+t.cfg.Domain+"/tracker.php")

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
		cats = []string{"-1"} // all forums
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		torrents []tracker.Torrent
	)

	for _, cat := range cats {
		wg.Add(1)
		go func(cat string) {
			defer wg.Done()

			form := url.Values{
				"prev_allw":     {"1"},
				"prev_a":        {"0"},
				"prev_dla":      {"0"},
				"prev_dlc":      {"0"},
				"prev_dld":      {"0"},
				"prev_dlw":      {"0"},
				"prev_my":       {"0"},
				"prev_new":      {"0"},
				"prev_sd":       {"0"},
				"prev_da":       {"1"},
				"prev_dc":       {"0"},
				"prev_df":       {"1"},
				"prev_ds":       {"0"},
				"prev_tor_type": {"0"},
				"f[]":           {cat},
				"o":             {"1"},
				"s":             {"2"},
				"tm":            {"-1"},
				"df":            {"1"},
				"da":            {"1"},
				"nm":            {query},
				"allw":          {"1"},
				"submit":        {"+"},
			}

			body, err := t.post("https://"+t.cfg.Domain+"/tracker.php", form)
			if err != nil {
				return
			}

			rows := parseSearchPage(string(body), "https://"+t.cfg.Domain)
			mu.Lock()
			torrents = append(torrents, rows...)
			mu.Unlock()
		}(cat)
	}
	wg.Wait()

	result.Results = dedup(torrents)
	result.Count = len(result.Results)
	return result
}

func dedup(items []tracker.Torrent) []tracker.Torrent {
	seen := make(map[string]bool)
	out := items[:0]
	for _, t := range items {
		if t.InfoHash != "" && seen[t.InfoHash] {
			continue
		}
		if t.InfoHash != "" {
			seen[t.InfoHash] = true
		}
		out = append(out, t)
	}
	return out
}

// ─── HTML parsing ────────────────────────────────────────────────────────────

func parseSearchPage(content, baseURL string) []tracker.Torrent {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}

	table := findNode(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "table" && getAttr(n, "id") == "tor-tbl"
	})
	if table == nil {
		return nil
	}

	var torrents []tracker.Torrent
	walkRows(table, func(tr *html.Node) {
		if !hasClass(tr, "hl-tr") {
			return
		}
		if t := extractTorrent(tr, baseURL); t != nil {
			torrents = append(torrents, *t)
		}
	})
	return torrents
}

func extractTorrent(tr *html.Node, baseURL string) *tracker.Torrent {
	var t tracker.Torrent

	for td := tr.FirstChild; td != nil; td = td.NextSibling {
		if td.Type != html.ElementNode || td.Data != "td" {
			continue
		}
		cls := getAttr(td, "class")
		titleAttr := getAttr(td, "title")

		switch {
		// Title cell: class contains "tLeft"
		case strings.Contains(cls, "tLeft"):
			if a := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "a" && strings.Contains(getAttr(n, "class"), "tLink")
			}); a != nil {
				t.Name = cleanText(extractText(a))
				href := getAttr(a, "href")
				if strings.HasPrefix(href, "./") {
					href = href[1:]
				}
				t.URL = baseURL + href
			}

		// Size + magnet cell: class "row4 small nowrap" without title "Добавлен"
		case strings.Contains(cls, "row4") && strings.Contains(cls, "small") && strings.Contains(cls, "nowrap") && titleAttr != "Добавлен":
			if a := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "a" && strings.Contains(getAttr(n, "class"), "tr-dl")
			}); a != nil {
				t.Size = strings.ReplaceAll(cleanText(extractText(a)), " ", " ")
			}
			if a := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "a" && strings.HasPrefix(getAttr(n, "href"), "magnet:")
			}); a != nil {
				t.Magnet = getAttr(a, "href")
				if m := reHash.FindStringSubmatch(t.Magnet); len(m) > 1 {
					t.InfoHash = strings.ToUpper(m[1])
				}
			}

		// Seeds cell
		case strings.Contains(cls, "seedmed"):
			if b := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "b"
			}); b != nil {
				t.Seeds = cleanText(extractText(b))
			}

		// Leechers cell
		case strings.Contains(cls, "leechmed"):
			if b := findNode(td, func(n *html.Node) bool {
				return n.Type == html.ElementNode && n.Data == "b"
			}); b != nil {
				t.Peers = cleanText(extractText(b))
			}

		// Date cell: title="Добавлен"
		case titleAttr == "Добавлен":
			// Second <p> has the absolute date like "15-Фев-26"
			var pCount int
			for c := td.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "p" {
					pCount++
					if pCount == 2 {
						t.Date = dateutil.Normalize(cleanText(extractText(c)))
						break
					}
				}
			}
		}
	}

	if t.Name == "" || t.Magnet == "" {
		return nil
	}
	return &t
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

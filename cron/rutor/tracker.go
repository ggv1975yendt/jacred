package rutor

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"

	"jacred/config"
	"jacred/tracker"
)

var defaultCategories = []string{"1", "5", "4", "16", "12", "6", "7", "10", "17", "13", "15"}

type Tracker struct {
	cfg config.TrackerConfig
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "rutor.info"
	}
	if cfg.AltDomain == "" {
		cfg.AltDomain = "rutor.is"
	}
	if len(cfg.Categories) == 0 {
		cfg.Categories = defaultCategories
	}
	return &Tracker{cfg: cfg}
}

func (t *Tracker) Name() string {
	return "rutor.info"
}

func (t *Tracker) Search(query string, sort int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var allTorrents []tracker.Torrent

	for _, category := range t.cfg.Categories {
		wg.Add(1)
		go func(cat string) {
			defer wg.Done()

			encoded := strings.ReplaceAll(query, " ", "+")
			searchURL := fmt.Sprintf("https://%s/search/0/%s/000/%d/%s", t.cfg.Domain, cat, sort, encoded)

			client := &http.Client{Timeout: 15 * time.Second}
			req, err := http.NewRequest("GET", searchURL, nil)
			if err != nil {
				return
			}

			setDefaultHeaders(req)
			baseURL := "https://" + t.cfg.Domain
			resp, err := client.Do(req)
			if err != nil {
				// Пробуем альтернативный домен
				altURL := fmt.Sprintf("https://%s/search/0/%s/000/%d/%s", t.cfg.AltDomain, cat, sort, encoded)
				req2, err2 := http.NewRequest("GET", altURL, nil)
				if err2 != nil {
					return
				}
				setDefaultHeaders(req2)
				resp, err = client.Do(req2)
				if err != nil {
					return
				}
				baseURL = "https://" + t.cfg.AltDomain
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return
			}

			bodyStr := string(body)
			if strings.Contains(resp.Header.Get("Content-Type"), "windows-1251") ||
				strings.Contains(bodyStr, "windows-1251") {
				decoder := charmap.Windows1251.NewDecoder()
				decoded, err := decoder.Bytes(body)
				if err == nil {
					bodyStr = string(decoded)
				}
			}

			torrents := parseHTML(bodyStr, baseURL)

			mu.Lock()
			allTorrents = append(allTorrents, torrents...)
			mu.Unlock()
		}(category)
	}

	wg.Wait()

	uniqueTorrents := removeDuplicates(allTorrents)

	result.Results = uniqueTorrents
	result.Count = len(result.Results)
	return result
}

func setDefaultHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
}

func parseHTML(htmlContent, baseURL string) []tracker.Torrent {
	var torrents []tracker.Torrent

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return torrents
	}

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			if t := extractTorrentFromRow(n, baseURL); t != nil {
				torrents = append(torrents, *t)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)

	return torrents
}

func extractTorrentFromRow(tr *html.Node, baseURL string) *tracker.Torrent {
	var cells []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "td" {
			cells = append(cells, c)
		}
	}

	if len(cells) < 2 {
		return nil
	}

	var torrent tracker.Torrent
	var hasMagnet bool

	var walkNode func(*html.Node)
	walkNode = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := getAttr(n, "href")
			text := extractText(n)

			if strings.HasPrefix(href, "magnet:") {
				torrent.Magnet = href
				hasMagnet = true
				re := regexp.MustCompile(`xt=urn:btih:([a-fA-F0-9]{40}|[A-Z2-7]{32})`)
				if m := re.FindStringSubmatch(href); len(m) > 1 {
					torrent.InfoHash = strings.ToUpper(m[1])
				}
			} else if strings.Contains(href, "/torrent/") && text != "" && !strings.Contains(text, "↓") {
				if torrent.Name == "" {
					torrent.Name = cleanText(text)
					if strings.HasPrefix(href, "/") {
						torrent.URL = baseURL + href
					} else {
						torrent.URL = href
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(c)
		}
	}

	for _, cell := range cells {
		walkNode(cell)
	}

	if len(cells) >= 4 {
		torrent.Size = cleanText(extractText(cells[len(cells)-2]))
	}
	if len(cells) >= 2 {
		last := extractColoredNumbers(cells[len(cells)-1])
		if len(last) >= 2 {
			torrent.Seeds = last[0]
			torrent.Peers = last[1]
		} else if len(last) == 1 {
			torrent.Seeds = last[0]
		}
	}

	if len(cells) > 0 {
		dateText := cleanText(extractText(cells[0]))
		if isDate(dateText) {
			torrent.Date = dateText
		}
	}

	if !hasMagnet || torrent.Name == "" {
		return nil
	}

	return &torrent
}

func extractColoredNumbers(n *html.Node) []string {
	var results []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && (node.Data == "span" || node.Data == "font") {
			text := cleanText(extractText(node))
			if _, err := strconv.Atoi(text); err == nil {
				results = append(results, text)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
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
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func isDate(s string) bool {
	dateRe := regexp.MustCompile(`^\d{2}\.\d{2}\.\d{4}$|^\d{4}-\d{2}-\d{2}$`)
	return dateRe.MatchString(s)
}

func removeDuplicates(torrents []tracker.Torrent) []tracker.Torrent {
	seen := make(map[string]bool)
	var result []tracker.Torrent

	for _, t := range torrents {
		hash := t.InfoHash
		if hash == "" {
			hash = t.Magnet
		}
		if !seen[hash] {
			seen[hash] = true
			result = append(result, t)
		}
	}

	return result
}

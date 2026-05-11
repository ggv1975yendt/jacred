package therarbg

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"jacred/config"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var validCategories = map[string]bool{
	"Movies":        true,
	"TV":            true,
	"Anime":         true,
	"Documentaries": true,
	"Games":         true,
	"Music":         true,
	"Apps":          true,
	"Books":         true,
	"Other":         true,
}

type Tracker struct {
	cfg    config.TrackerConfig
	client *http.Client
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "therarbg.com"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "therarbg.com" }

type apiResponse struct {
	Results []apiResult `json:"results"`
}

type apiResult struct {
	PK       string   `json:"pk"`
	Name     string   `json:"n"`
	Added    int64    `json:"a"`
	Category string   `json:"c"`
	Size     int64    `json:"s"`
	Seeders  int      `json:"se"`
	Peers    int      `json:"le"`
	InfoHash string   `json:"h"`
	Tags     []string `json:"tg"`
}

func (t *Tracker) Search(query string, sort int) *tracker.SearchResult {
	result := &tracker.SearchResult{Query: query}

	q := strings.TrimSpace(query)
	if q == "" {
		result.Error = "empty query"
		return result
	}
	q = cleanQuery(q)

	orderPrefix := ""
	if sort == 2 {
		orderPrefix = "order:-se:"
	}

	categories := filterValidCategories(t.cfg.Categories)

	// No categories or all categories configured → single request without category filter
	if len(categories) == 0 || len(categories) == len(validCategories) {
		result.Results = t.fetchCategory(q, orderPrefix, "")
		result.Count = len(result.Results)
		return result
	}

	// Multiple specific categories → parallel requests, merge by info hash
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := make(map[string]bool)
	var all []tracker.Torrent

	for _, cat := range categories {
		wg.Add(1)
		go func(category string) {
			defer wg.Done()
			torrents := t.fetchCategory(q, orderPrefix, category)
			mu.Lock()
			for _, tor := range torrents {
				key := tor.InfoHash
				if key == "" {
					key = tor.Magnet
				}
				if !seen[key] {
					seen[key] = true
					all = append(all, tor)
				}
			}
			mu.Unlock()
		}(cat)
	}
	wg.Wait()

	result.Results = all
	result.Count = len(result.Results)
	return result
}

func (t *Tracker) fetchCategory(q, orderPrefix, category string) []tracker.Torrent {
	catSegment := ""
	if category != "" {
		catSegment = "category:" + category + ":"
	}

	searchURL := fmt.Sprintf("https://%s/get-posts/%s%skeywords:%s:format:json/",
		t.cfg.Domain, orderPrefix, catSegment, url.PathEscape(q))

	body, err := t.fetch(searchURL)
	if err != nil {
		return nil
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	base := "https://" + t.cfg.Domain
	out := make([]tracker.Torrent, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if tor := mapResult(r, base); tor != nil {
			out = append(out, *tor)
		}
	}
	return out
}

func (t *Tracker) fetch(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, */*")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func mapResult(r apiResult, base string) *tracker.Torrent {
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.InfoHash) == "" {
		return nil
	}

	date := ""
	if r.Added > 0 {
		date = time.Unix(r.Added, 0).UTC().Format("2006-01-02")
	}

	return &tracker.Torrent{
		Name:     strings.TrimSpace(r.Name),
		URL:      buildDetailURL(base, r.PK, r.Name),
		Magnet:   buildMagnet(r.InfoHash, r.Name),
		Size:     formatSize(r.Size),
		Seeds:    fmt.Sprint(r.Seeders),
		Peers:    fmt.Sprint(r.Peers),
		Date:     date,
		InfoHash: strings.ToUpper(r.InfoHash),
		Source:   r.Category,
	}
}

func buildMagnet(hash, name string) string {
	return fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s",
		strings.ToUpper(hash), url.QueryEscape(name))
}

func buildDetailURL(base, pk, name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", ".")
	slug = strings.ReplaceAll(slug, ".", "-")
	return fmt.Sprintf("%s/post-detail/%s/%s/", base, pk, slug)
}

func cleanQuery(q string) string {
	r := strings.NewReplacer(
		":", " ", ",", " ", ".", " ",
		"[", " ", "]", " ", "(", " ", ")", " ",
	)
	return strings.Join(strings.Fields(r.Replace(q)), " ")
}

func filterValidCategories(cats []string) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		c = strings.TrimSpace(c)
		if validCategories[c] {
			out = append(out, c)
		}
	}
	return out
}

func formatSize(b int64) string {
	const (
		mb = 1024 * 1024
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b < gb:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(mb))
	case b < tb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	default:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(tb))
	}
}

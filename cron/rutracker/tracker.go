package rutracker

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/charmap"

	"jacred/config"
	"jacred/tracker"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

var (
	rowTopicIDRe  = regexp.MustCompile(`data-topic_id="([0-9]+)"`)
	rowTitleRe    = regexp.MustCompile(`class="[^"]*tLink[^"]*"[^>]*>([^<\n\r]+)`)
	rowSidRe      = regexp.MustCompile(`<b class="seedmed">([0-9]+)</b>`)
	rowPirRe      = regexp.MustCompile(`class="[^"]*leechmed[^"]*"[^>]*>([0-9]+)`)
	rowSizeRe     = regexp.MustCompile(`dl-stub[^>]*>([^<]+)</a>`)
	rowDateRe     = regexp.MustCompile(`<p>([0-9]{1,2}-[^<\n]{2,6}-[0-9]{2,4})</p>`)
	topicMagnetRe = regexp.MustCompile(`href="(magnet:[^"]+)" class="(?:med )?magnet-link"`)
	magnetHashRe  = regexp.MustCompile(`xt=urn:btih:([a-fA-F0-9]{40}|[A-Z2-7]{32})`)
)

type searchEntry struct {
	id, title, size, seeds, peers, date string
}

type Tracker struct {
	cfg      config.TrackerConfig
	mu       sync.Mutex
	client   *http.Client
	cookie   string
	cookieAt time.Time
}

func New(cfg config.TrackerConfig) *Tracker {
	if cfg.Domain == "" {
		cfg.Domain = "rutracker.org"
	}
	return &Tracker{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) Name() string { return "rutracker.org" }

// ─── Auth ──────────────────────────────────────────────────

func (t *Tracker) getCookie() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cookie != "" && time.Since(t.cookieAt) < 2*time.Hour {
		return t.cookie
	}
	return ""
}

func (t *Tracker) doLogin() (string, error) {
	if t.cfg.Username == "" || t.cfg.Password == "" {
		return "", fmt.Errorf("username/password not configured")
	}
	form := url.Values{
		"login_username": {t.cfg.Username},
		"login_password": {t.cfg.Password},
		"login":          {"\xc2\xf5\xee\xe4"}, // "Вход" в CP1251
	}
	loginClient := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // не следовать редиректам, чтобы не потерять Set-Cookie
		},
	}
	req, err := http.NewRequest("POST",
		"https://"+t.cfg.Domain+"/forum/login.php",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://"+t.cfg.Domain+"/forum/")

	resp, err := loginClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parts []string
	for _, line := range resp.Header.Values("Set-Cookie") {
		parts = append(parts, strings.SplitN(line, ";", 2)[0])
	}
	cookie := strings.Join(parts, "; ")
	if !strings.Contains(cookie, "bb_session") {
		return "", fmt.Errorf("login failed: bb_session not received (status %d)", resp.StatusCode)
	}
	return cookie, nil
}

func (t *Tracker) ensureLogin() (string, error) {
	if c := t.getCookie(); c != "" {
		return c, nil
	}
	cookie, err := t.doLogin()
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.cookie = cookie
	t.cookieAt = time.Now()
	t.mu.Unlock()
	return cookie, nil
}

// ─── HTTP helper ───────────────────────────────────────────

func (t *Tracker) get(rawURL, cookie string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
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

	cookie, err := t.ensureLogin()
	if err != nil {
		result.Error = "авторизация: " + err.Error()
		return result
	}

	encodedQuery := encodeWin1251(query)
	forumBase := "https://" + t.cfg.Domain + "/forum"

	// 1. Собираем записи из всех категорий параллельно.
	cats := t.cfg.Categories
	if len(cats) == 0 {
		cats = []string{""}
	}

	var (
		entriesMu  sync.Mutex
		allEntries []searchEntry
		catWg      sync.WaitGroup
	)
	for _, cat := range cats {
		catWg.Add(1)
		go func(cat string) {
			defer catWg.Done()

			var searchURL string
			if cat == "" {
				searchURL = forumBase + "/tracker.php?nm=" + encodedQuery
			} else {
				searchURL = forumBase + "/tracker.php?f=" + cat + "&nm=" + encodedQuery
			}

			body, err := t.get(searchURL, cookie)
			if err != nil || isLoginPage(body) {
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

	// 2. Получаем magnet-ссылки со страниц топиков (не более 5 одновременно).
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

			topicURL := forumBase + "/viewtopic.php?t=" + e.id
			magnet := t.fetchMagnet(topicURL, cookie)
			if magnet == "" {
				return
			}
			tor := tracker.Torrent{
				Name:   e.title,
				URL:    topicURL,
				Magnet: magnet,
				Size:   e.size,
				Seeds:  e.seeds,
				Peers:  e.peers,
				Date:   e.date,
			}
			if m := magnetHashRe.FindStringSubmatch(magnet); len(m) > 1 {
				tor.InfoHash = strings.ToUpper(m[1])
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

func (t *Tracker) fetchMagnet(topicURL, cookie string) string {
	body, err := t.get(topicURL, cookie)
	if err != nil || len(body) == 0 {
		return ""
	}
	decoded, err := charmap.Windows1251.NewDecoder().Bytes(body)
	if err != nil {
		return ""
	}
	if m := topicMagnetRe.FindStringSubmatch(string(decoded)); len(m) > 1 {
		return m[1]
	}
	return ""
}

// ─── HTML parsing ──────────────────────────────────────────

func parseSearchPage(content string) []searchEntry {
	var entries []searchEntry
	// Search results use class="tCenter hl-tr" on each torrent row
	rows := strings.Split(content, `class="tCenter hl-tr"`)
	for _, row := range rows[1:] {
		// data-topic_id appears first on the <tr> itself
		id := match1(rowTopicIDRe, row)
		title := match1(rowTitleRe, row)
		if id == "" || title == "" {
			continue
		}
		// Size: strip download arrow entity and non-breaking space
		size := match1(rowSizeRe, row)
		size = strings.ReplaceAll(size, "&#8595;", "")
		size = strings.ReplaceAll(size, "&nbsp;", " ")
		size = strings.TrimSpace(size)
		entries = append(entries, searchEntry{
			id:    id,
			title: title,
			seeds: match1(rowSidRe, row),
			peers: match1(rowPirRe, row),
			size:  size,
			date:  match1(rowDateRe, row),
		})
	}
	return entries
}

func match1(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// ─── Helpers ───────────────────────────────────────────────

// encodeWin1251 percent-encodes a string using Windows-1251 byte values.
func encodeWin1251(s string) string {
	w1251, err := charmap.Windows1251.NewEncoder().String(s)
	if err != nil {
		return url.QueryEscape(s)
	}
	var sb strings.Builder
	for i := 0; i < len(w1251); i++ {
		b := w1251[i]
		switch {
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~':
			sb.WriteByte(b)
		case b == ' ':
			sb.WriteByte('+')
		default:
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

func isLoginPage(body []byte) bool {
	return bytes.Contains(body, []byte("login_username"))
}

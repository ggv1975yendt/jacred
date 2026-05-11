package router

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jacred/config"
	"jacred/tracker"
)

// TrackerFactory creates a tracker instance from its config.
type TrackerFactory func(cfg config.TrackerConfig) tracker.Tracker

// TrackerHealth holds the last known availability of a tracker.
type TrackerHealth struct {
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
}

type Router struct {
	mu            sync.RWMutex
	cfg           *config.Config
	cfgPath       string
	factories     map[string]TrackerFactory
	allTrackers   map[string]tracker.Tracker
	indexTemplate *template.Template
	adminTemplate *template.Template
	Mux           *http.ServeMux

	healthMu   sync.RWMutex
	health     map[string]TrackerHealth
	healthPing chan struct{} // send to trigger an immediate health check
}

type PageData struct {
	TrackersJSON template.JS
}

type AdminPageData struct {
	ConfigJSON       template.JS
	BaseTrackersJSON template.JS // factory names (for type selector in aliases)
}

func New(factories map[string]TrackerFactory, templateDir, cfgPath string, cfg *config.Config) (*Router, error) {
	indexTmpl, err := loadTemplate(filepath.Join(templateDir, "index.html"), template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"mul": func(a, b int) int { return a * b },
		"not": func(v any) bool {
			if v == nil {
				return true
			}
			if s, ok := v.(string); ok {
				return s == ""
			}
			return false
		},
	})
	if err != nil {
		return nil, fmt.Errorf("index template: %w", err)
	}

	adminTmpl, err := loadTemplate(filepath.Join(templateDir, "admin.html"), nil)
	if err != nil {
		return nil, fmt.Errorf("admin template: %w", err)
	}

	absStatic, err := filepath.Abs(filepath.Join(templateDir, "static"))
	if err != nil {
		return nil, fmt.Errorf("static dir: %w", err)
	}
	absIco, err := filepath.Abs(filepath.Join(templateDir, "ico"))
	if err != nil {
		return nil, fmt.Errorf("ico dir: %w", err)
	}

	r := &Router{
		cfg:           cfg,
		cfgPath:       cfgPath,
		factories:     factories,
		allTrackers:   buildTrackers(factories, cfg),
		indexTemplate: indexTmpl,
		adminTemplate: adminTmpl,
		Mux:           http.NewServeMux(),
		health:        make(map[string]TrackerHealth),
		healthPing:    make(chan struct{}, 1),
	}

	r.Mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(absStatic))))
	r.Mux.Handle("/ico/", http.StripPrefix("/ico/", http.FileServer(http.Dir(absIco))))
	r.Mux.HandleFunc("/", r.handleIndex)
	r.Mux.HandleFunc("/api/search", r.handleAPISearch)
	r.Mux.HandleFunc("/api/ui/search", r.handleUISearch)
	r.Mux.HandleFunc("/api/trackers/status", r.handleTrackersStatus)
	r.Mux.HandleFunc("/admin", r.handleAdmin)
	r.Mux.HandleFunc("/admin/save", r.handleAdminSave)

	r.startHealthChecker(context.Background())

	return r, nil
}

// ─── Health checker ────────────────────────────────────────

func (r *Router) startHealthChecker(ctx context.Context) {
	go func() {
		r.checkAllTrackers() // immediate first check
		for {
			r.mu.RLock()
			interval := time.Duration(r.cfg.PingInterval) * time.Minute
			r.mu.RUnlock()
			if interval < time.Minute {
				interval = 10 * time.Minute
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				r.checkAllTrackers()
			case <-r.healthPing:
				r.checkAllTrackers()
			}
		}
	}()
}

func (r *Router) checkAllTrackers() {
	r.mu.RLock()
	cfg := r.cfg
	names := make([]string, 0, len(r.allTrackers))
	for name := range r.allTrackers {
		names = append(names, name)
	}
	r.mu.RUnlock()

	// Pass 1: ping base trackers only (no type, or type == self).
	var wg sync.WaitGroup
	for _, name := range names {
		tcfg := cfg.Trackers[name]
		if tcfg.Type != "" && tcfg.Type != name {
			continue // alias — handled in pass 2
		}
		wg.Add(1)
		go func(tname string, tc config.TrackerConfig) {
			defer wg.Done()
			available := pingHost(tc.Domain)
			if !available && tc.AltDomain != "" {
				available = pingHost(tc.AltDomain)
			}
			r.healthMu.Lock()
			r.health[tname] = TrackerHealth{Available: available, CheckedAt: time.Now()}
			r.healthMu.Unlock()
		}(name, tcfg)
	}
	wg.Wait()

	// Pass 2: aliases inherit health from their base tracker.
	now := time.Now()
	r.healthMu.Lock()
	for _, name := range names {
		tcfg := cfg.Trackers[name]
		if tcfg.Type == "" || tcfg.Type == name {
			continue
		}
		if baseHealth, ok := r.health[tcfg.Type]; ok {
			r.health[name] = TrackerHealth{Available: baseHealth.Available, CheckedAt: now}
		}
	}
	r.healthMu.Unlock()
}

func pingHost(domain string) bool {
	if domain == "" {
		return false
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://" + domain + "/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// ─── Helpers ───────────────────────────────────────────────

func buildTrackers(factories map[string]TrackerFactory, cfg *config.Config) map[string]tracker.Tracker {
	result := make(map[string]tracker.Tracker)
	for name, tcfg := range cfg.Trackers {
		if !tcfg.Enable {
			continue
		}

		factoryKey := name
		resolvedCfg := tcfg

		// Alias: inherit all settings from base profile, override only categories.
		if tcfg.Type != "" && tcfg.Type != name {
			factoryKey = tcfg.Type
			if base, ok := cfg.Trackers[tcfg.Type]; ok {
				cats := resolvedCfg.Categories
				resolvedCfg = base
				resolvedCfg.Categories = cats
			}
		}

		factory, ok := factories[factoryKey]
		if !ok {
			log.Printf("tracker %q: no factory for type %q, skipping", name, factoryKey)
			continue
		}
		result[name] = factory(resolvedCfg)
	}
	return result
}

func loadTemplate(path string, funcs template.FuncMap) (*template.Template, error) {
	fullPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(fullPath)
	tmpl := template.New(name)
	if funcs != nil {
		tmpl = tmpl.Funcs(funcs)
	}
	return tmpl.Parse(string(content))
}

func (r *Router) trackerNamesJSON() template.JS {
	names := r.trackerNames()
	b, _ := json.Marshal(names)
	return template.JS(b)
}

// ─── Web UI handlers ───────────────────────────────────────

func (r *Router) handleIndex(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	r.renderPage(w, PageData{TrackersJSON: r.trackerNamesJSON()})
}

func (r *Router) handleUISearch(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	var body struct {
		Query    string   `json:"query"`
		Trackers []string `json:"trackers"`
		Sort     int      `json:"sort"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}

	query := strings.TrimSpace(body.Query)
	if query == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "empty query"})
		return
	}

	var searchTrackers []tracker.Tracker
	if len(body.Trackers) > 0 {
		searchTrackers = r.trackersForNames(body.Trackers)
	}
	if len(searchTrackers) == 0 {
		searchTrackers = r.allTrackersList()
	}

	rawResults := r.searchTrackers(searchTrackers, query, body.Sort)
	json.NewEncoder(w).Encode(mergeAndSort(rawResults, body.Sort))
}

func (r *Router) renderPage(w http.ResponseWriter, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := r.indexTemplate.Execute(w, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

// ─── API handlers ──────────────────────────────────────────

func (r *Router) handleAPISearch(w http.ResponseWriter, req *http.Request) {
	query := strings.TrimSpace(req.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")

	if query == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "empty query"})
		return
	}

	key := req.Header.Get("X-Api-Key")
	if key == "" {
		key = req.URL.Query().Get("apikey")
	}

	r.mu.RLock()
	api := r.findAPI(key)
	r.mu.RUnlock()

	if api == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid API key"})
		return
	}

	trackers := r.trackersForNames(api.Trackers)
	results := r.searchTrackers(trackers, query, 0)
	json.NewEncoder(w).Encode(results)
}

func (r *Router) handleTrackersStatus(w http.ResponseWriter, req *http.Request) {
	r.healthMu.RLock()
	status := make(map[string]TrackerHealth, len(r.health))
	for k, v := range r.health {
		status[k] = v
	}
	r.healthMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// ─── Admin handlers ────────────────────────────────────────

func (r *Router) handleAdmin(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	names := r.allFactoryNames()
	trackersJSON, err := json.Marshal(names)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := AdminPageData{
		ConfigJSON:       template.JS(cfgJSON),
		BaseTrackersJSON: template.JS(trackersJSON),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := r.adminTemplate.Execute(w, data); err != nil {
		log.Printf("Admin template error: %v", err)
	}
}

func (r *Router) handleAdminSave(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var newCfg config.Config
	if err := json.NewDecoder(req.Body).Decode(&newCfg); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request: " + err.Error()})
		return
	}

	if strings.TrimSpace(newCfg.Port) == "" {
		newCfg.Port = "9117"
	}
	if newCfg.PingInterval < 1 {
		newCfg.PingInterval = 10
	}
	if newCfg.APIs == nil {
		newCfg.APIs = []config.APIConfig{}
	}
	if newCfg.Trackers == nil {
		newCfg.Trackers = map[string]config.TrackerConfig{}
	}

	seen := make(map[string]bool)
	for i, api := range newCfg.APIs {
		if strings.TrimSpace(api.Key) == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("API #%d: ключ не может быть пустым", i+1)})
			return
		}
		if seen[api.Key] {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("API #%d: дублирующийся ключ", i+1)})
			return
		}
		seen[api.Key] = true
		if newCfg.APIs[i].Trackers == nil {
			newCfg.APIs[i].Trackers = []string{}
		}
	}

	if err := config.Save(r.cfgPath, &newCfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "saving config: " + err.Error()})
		return
	}

	newTrackers := buildTrackers(r.factories, &newCfg)

	r.mu.Lock()
	r.cfg = &newCfg
	r.allTrackers = newTrackers
	r.mu.Unlock()

	// Trigger immediate health re-check with new settings
	select {
	case r.healthPing <- struct{}{}:
	default:
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ─── Shared helpers ────────────────────────────────────────

func (r *Router) findAPI(key string) *config.APIConfig {
	if key == "" {
		return nil
	}
	for i := range r.cfg.APIs {
		if r.cfg.APIs[i].Key == key {
			return &r.cfg.APIs[i]
		}
	}
	return nil
}

func (r *Router) trackersForNames(names []string) []tracker.Tracker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []tracker.Tracker
	for _, name := range names {
		if t, ok := r.allTrackers[name]; ok {
			result = append(result, t)
		}
	}
	return result
}

func (r *Router) allTrackersList() []tracker.Tracker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]tracker.Tracker, 0, len(r.allTrackers))
	for _, t := range r.allTrackers {
		result = append(result, t)
	}
	return result
}

func (r *Router) trackerNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.allTrackers))
	for name := range r.allTrackers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// allFactoryNames returns every registered tracker name regardless of enabled state.
// Used by the admin panel so all trackers can be configured.
func (r *Router) allFactoryNames() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseSeeds(s string) int {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	n, _ := strconv.Atoi(b.String())
	return n
}

func parseSizeBytes(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "—" {
		return 0
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == ',') {
		i++
	}
	if i == 0 {
		return 0
	}
	numStr := strings.ReplaceAll(s[:i], ",", ".")
	unitStr := strings.ToUpper(strings.TrimSpace(s[i:]))
	val, _ := strconv.ParseFloat(numStr, 64)
	switch unitStr {
	case "TB":
		return val * 1099511627776
	case "GB":
		return val * 1073741824
	case "MB":
		return val * 1048576
	case "KB":
		return val * 1024
	default:
		return val
	}
}

// mergeAndSort объединяет результаты всех трекеров в один список и сортирует по убыванию.
// Ошибочные результаты передаются без изменений, успешные — объединяются.
func mergeAndSort(results []*tracker.SearchResult, sortBy int) []*tracker.SearchResult {
	var errors []*tracker.SearchResult
	var allTorrents []tracker.Torrent
	var sources []string
	query := ""

	for _, r := range results {
		if r == nil {
			continue
		}
		if query == "" && r.Query != "" {
			query = r.Query
		}
		if r.Error != "" {
			errors = append(errors, r)
			continue
		}
		allTorrents = append(allTorrents, r.Results...)
		if r.Source != "" && len(r.Results) > 0 {
			sources = append(sources, r.Source)
		}
	}

	sort.SliceStable(allTorrents, func(i, j int) bool {
		switch sortBy {
		case 2: // по сидам
			return parseSeeds(allTorrents[i].Seeds) > parseSeeds(allTorrents[j].Seeds)
		case 8: // по размеру
			return parseSizeBytes(allTorrents[i].Size) > parseSizeBytes(allTorrents[j].Size)
		default: // по дате
			return allTorrents[i].Date > allTorrents[j].Date
		}
	})

	sourceName := strings.Join(sources, ", ")
	if sourceName == "" {
		sourceName = "Все трекеры"
	}

	merged := &tracker.SearchResult{
		Query:   query,
		Source:  sourceName,
		Count:   len(allTorrents),
		Results: allTorrents,
	}

	return append(errors, merged)
}

func (r *Router) searchTrackers(trackers []tracker.Tracker, query string, sortBy int) []*tracker.SearchResult {
	results := make([]*tracker.SearchResult, len(trackers))
	var wg sync.WaitGroup
	for i, t := range trackers {
		wg.Add(1)
		go func(idx int, tr tracker.Tracker) {
			defer wg.Done()
			result := tr.Search(query, sortBy)
			result.Source = tr.Name()
			for i := range result.Results {
				result.Results[i].Source = result.Source
			}
			results[idx] = result
		}(i, t)
	}
	wg.Wait()
	return results
}
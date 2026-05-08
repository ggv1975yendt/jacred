package router

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"jacred/config"
	"jacred/tracker"
)

// TrackerFactory creates a tracker instance from its config.
type TrackerFactory func(cfg config.TrackerConfig) tracker.Tracker

type Router struct {
	mu            sync.RWMutex
	cfg           *config.Config
	cfgPath       string
	factories     map[string]TrackerFactory
	allTrackers   map[string]tracker.Tracker
	indexTemplate *template.Template
	adminTemplate *template.Template
	Mux           *http.ServeMux
}

type PageData struct {
	Query   string
	Results []*tracker.SearchResult
}

type AdminPageData struct {
	ConfigJSON   template.JS
	TrackersJSON template.JS
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

	r := &Router{
		cfg:           cfg,
		cfgPath:       cfgPath,
		factories:     factories,
		allTrackers:   buildTrackers(factories, cfg),
		indexTemplate: indexTmpl,
		adminTemplate: adminTmpl,
		Mux:           http.NewServeMux(),
	}

	absStatic, err := filepath.Abs(filepath.Join(templateDir, "static"))
	if err != nil {
		return nil, fmt.Errorf("static dir: %w", err)
	}
	r.Mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(absStatic))))

	r.Mux.HandleFunc("/", r.handleIndex)
	r.Mux.HandleFunc("/search", r.handleSearch)
	r.Mux.HandleFunc("/api/search", r.handleAPISearch)
	r.Mux.HandleFunc("/admin", r.handleAdmin)
	r.Mux.HandleFunc("/admin/save", r.handleAdminSave)

	return r, nil
}

func buildTrackers(factories map[string]TrackerFactory, cfg *config.Config) map[string]tracker.Tracker {
	result := make(map[string]tracker.Tracker, len(factories))
	for name, factory := range factories {
		result[name] = factory(cfg.Trackers[name])
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

// --- Web UI handlers ---

func (r *Router) handleIndex(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	r.renderPage(w, PageData{Results: make([]*tracker.SearchResult, 0)})
}

func (r *Router) handleSearch(w http.ResponseWriter, req *http.Request) {
	query := strings.TrimSpace(req.URL.Query().Get("q"))
	if query == "" {
		http.Redirect(w, req, "/", http.StatusSeeOther)
		return
	}
	results := r.searchTrackers(r.allTrackersList(), query)
	r.renderPage(w, PageData{Query: query, Results: results})
}

func (r *Router) renderPage(w http.ResponseWriter, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := r.indexTemplate.Execute(w, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

// --- API handler ---

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
	results := r.searchTrackers(trackers, query)
	json.NewEncoder(w).Encode(results)
}

// --- Admin handlers ---

func (r *Router) handleAdmin(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	names := r.trackerNames()
	trackersJSON, err := json.Marshal(names)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := AdminPageData{
		ConfigJSON:   template.JS(cfgJSON),
		TrackersJSON: template.JS(trackersJSON),
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

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Helpers ---

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

func (r *Router) searchTrackers(trackers []tracker.Tracker, query string) []*tracker.SearchResult {
	results := make([]*tracker.SearchResult, len(trackers))
	var wg sync.WaitGroup
	for i, t := range trackers {
		wg.Add(1)
		go func(idx int, tr tracker.Tracker) {
			defer wg.Done()
			result := tr.Search(query)
			result.Source = tr.Name()
			results[idx] = result
		}(i, t)
	}
	wg.Wait()
	return results
}
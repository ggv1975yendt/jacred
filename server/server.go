package server

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"jacred/tracker"
)

type Server struct {
	trackers      []tracker.Tracker
	indexTemplate *template.Template
	mux           *http.ServeMux
}

type PageData struct {
	Query   string
	Results []*tracker.SearchResult
}

func NewServer(trackers []tracker.Tracker, templatePath string) (*Server, error) {
	fullPath, err := filepath.Abs(templatePath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("index").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"mul": func(a, b int) int { return a * b },
		"not": func(v interface{}) bool {
			if v == nil {
				return true
			}
			if s, ok := v.(string); ok {
				return s == ""
			}
			return false
		},
	}).Parse(string(content))
	if err != nil {
		return nil, err
	}

	s := &Server{
		trackers:      trackers,
		indexTemplate: tmpl,
		mux:           http.NewServeMux(),
	}

	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/search", s.handleSearch)
	s.mux.HandleFunc("/api/search", s.handleAPISearch)

	return s, nil
}

func (s *Server) Start(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	sourceNames := make([]string, len(s.trackers))
	for i, t := range s.trackers {
		sourceNames[i] = t.Name()
	}

	s.renderPage(w, PageData{Results: make([]*tracker.SearchResult, 0)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Run all trackers in parallel goroutines
	results := s.searchAllTrackers(query)
	s.renderPage(w, PageData{Query: query, Results: results})
}

// searchAllTrackers runs search on all trackers in parallel and collects results
func (s *Server) searchAllTrackers(query string) []*tracker.SearchResult {
	results := make([]*tracker.SearchResult, len(s.trackers))
	var wg sync.WaitGroup

	for i, t := range s.trackers {
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

func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")
	if query == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "empty query"})
		return
	}

	results := s.searchAllTrackers(query)
	json.NewEncoder(w).Encode(results)
}

func (s *Server) renderPage(w http.ResponseWriter, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.indexTemplate.Execute(w, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

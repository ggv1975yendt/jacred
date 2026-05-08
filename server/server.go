package server

import (
	"net/http"

	"jacred/config"
	"jacred/server/router"
)

type Server struct {
	r *router.Router
}

func NewServer(factories map[string]router.TrackerFactory, templateDir, cfgPath string, cfg *config.Config) (*Server, error) {
	r, err := router.New(factories, templateDir, cfgPath, cfg)
	if err != nil {
		return nil, err
	}
	return &Server{r: r}, nil
}

func (s *Server) Start(addr string) error {
	return http.ListenAndServe(addr, s.r.Mux)
}
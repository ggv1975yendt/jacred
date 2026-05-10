package main

import (
	"log"

	"jacred/config"
	"jacred/cron/bigfangroup"
	"jacred/cron/kinozal"
	"jacred/cron/korsars"
	"jacred/cron/omagnet"
	"jacred/cron/rutor"
	"jacred/cron/rutracker"
	"jacred/cron/xxxclub"
	"jacred/cron/xxxtor"
	"jacred/server"
	"jacred/server/router"
	"jacred/tracker"
)

func main() {
	cfg, err := config.Load("init.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	factories := map[string]router.TrackerFactory{
		"rutor.info": func(tcfg config.TrackerConfig) tracker.Tracker {
			return rutor.New(tcfg)
		},
		"xxxclub.to": func(tcfg config.TrackerConfig) tracker.Tracker {
			return xxxclub.New(tcfg)
		},
		"kinozal.tv": func(tcfg config.TrackerConfig) tracker.Tracker {
			return kinozal.New(tcfg)
		},
		"rutracker.org": func(tcfg config.TrackerConfig) tracker.Tracker {
			return rutracker.New(tcfg)
		},
		"xxxtor.com": func(tcfg config.TrackerConfig) tracker.Tracker {
			return xxxtor.New(tcfg)
		},
		"16mag.net": func(tcfg config.TrackerConfig) tracker.Tracker {
			return omagnet.New(tcfg)
		},
		"korsars.pro": func(tcfg config.TrackerConfig) tracker.Tracker {
			return korsars.New(tcfg)
		},
		"bigfangroup.org": func(tcfg config.TrackerConfig) tracker.Tracker {
			return bigfangroup.New(tcfg)
		},
	}

	svr, err := server.NewServer(factories, "wwwroot", "init.yaml", cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	log.Printf("🚀 Torrent Search started on http://localhost:%s", cfg.Port)
	if err := svr.Start(":" + cfg.Port); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
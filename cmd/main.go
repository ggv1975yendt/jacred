package main

import (
	"log"
	"os"

	"jacred/cron/rutor"
	"jacred/server"
	"jacred/tracker"
)

func main() {
	port := "9117"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	// Create all trackers
	trackers := []tracker.Tracker{
		rutor.New(),
		// Add more trackers here in the future
	}

	svr, err := server.NewServer(trackers, "wwwroot/index.html")
	if err != nil {
		log.Fatalf("Failed to load template: %v", err)
	}

	log.Printf("🚀 Torrent Search started on http://localhost:%s", port)
	if err := svr.Start(":" + port); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

package tracker

// Torrent represents a torrent entry
type Torrent struct {
	Name     string `json:"name"`
	Magnet   string `json:"magnet"`
	Size     string `json:"size"`
	Seeds    string `json:"seeds"`
	Peers    string `json:"peers"`
	Date     string `json:"date"`
	InfoHash string `json:"info_hash"`
}

// SearchResult contains search results from a tracker
type SearchResult struct {
	Query   string    `json:"query"`
	Source  string    `json:"source,omitempty"`
	Count   int       `json:"count"`
	Results []Torrent `json:"results"`
	Error   string    `json:"error,omitempty"`
}

// Tracker interface that all trackers must implement
type Tracker interface {
	Name() string
	Search(query string, sort int) *SearchResult
}

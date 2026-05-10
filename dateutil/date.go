package dateutil

import (
	"strconv"
	"strings"
	"time"
)

// ruMonths maps Russian and English abbreviated month names (lowercase) to month numbers.
var ruMonths = map[string]int{
	"янв": 1, "jan": 1,
	"фев": 2, "feb": 2,
	"мар": 3, "mar": 3,
	"апр": 4, "apr": 4,
	"май": 5, "мая": 5, "may": 5,
	"июн": 6, "jun": 6,
	"июл": 7, "jul": 7,
	"авг": 8, "aug": 8,
	"сен": 9, "sep": 9,
	"окт": 10, "oct": 10,
	"ноя": 11, "nov": 11,
	"дек": 12, "dec": 12,
}

// enLayouts are tried in order using Go's time.Parse (handles English month names).
var enLayouts = []string{
	"2006-01-02",
	"02.01.2006",
	"2.01.2006",
	"02 Jan 2006 15:04:05",
	"2 Jan 2006 15:04:05",
	"02 Jan 2006",
	"2 Jan 2006",
}

// Normalize converts various date formats to yyyy-mm-dd.
// Returns the original string unchanged if parsing fails.
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	t, ok := tryParse(s)
	if !ok {
		return s
	}
	return t.Format("2006-01-02")
}

func tryParse(s string) (time.Time, bool) {
	for _, layout := range enLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return parseRu(s)
}

// parseRu handles formats with Russian abbreviated month names.
// Supported separators: "-" (rutracker: "9-апр-26") or " " (xxxtor: "09 Май 26").
func parseRu(s string) (time.Time, bool) {
	var parts []string
	if strings.ContainsRune(s, '-') {
		parts = strings.SplitN(s, "-", 3)
	} else {
		parts = strings.Fields(s)
	}
	if len(parts) < 3 {
		return time.Time{}, false
	}

	day, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}

	monthKey := strings.ToLower(strings.TrimSpace(parts[1]))
	// Truncate to first 3 Unicode runes to handle longer Russian words.
	if runes := []rune(monthKey); len(runes) > 3 {
		monthKey = string(runes[:3])
	}
	month, ok := ruMonths[monthKey]
	if !ok {
		return time.Time{}, false
	}

	yearStr := strings.TrimSpace(parts[2])
	// Strip any trailing time portion (e.g. "2024 16:34:10").
	if i := strings.IndexByte(yearStr, ' '); i != -1 {
		yearStr = yearStr[:i]
	}
	year, err := strconv.Atoi(yearStr)
	if err != nil || year < 0 {
		return time.Time{}, false
	}
	if year < 100 {
		year += 2000
	}

	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

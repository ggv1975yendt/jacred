package dateutil

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// rutor / kinozal
		{"09.05.2026", "2026-05-09"},
		{"1.05.2026", "2026-05-01"},
		// already normalized
		{"2026-05-09", "2026-05-09"},
		// rutracker: d-RuMonth-yy
		{"9-апр-26", "2026-04-09"},
		{"12-дек-2024", "2024-12-12"},
		// xxxclub: DD Mon YYYY HH:MM:SS
		{"14 Dec 2024 16:34:10", "2024-12-14"},
		{"9 Dec 2024 16:34:10", "2024-12-09"},
		// xxxtor: DD RuMon YY
		{"09 Май 26", "2026-05-09"},
		{"1 Янв 25", "2025-01-01"},
		// unknown → unchanged
		{"something", "something"},
		{"", ""},
	}
	for _, c := range cases {
		got := Normalize(c.in)
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

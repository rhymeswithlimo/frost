package snapshot

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Resolve picks one snapshot out of snaps using a selector:
//
//	latest (or empty)       the newest snapshot
//	maple-otter-3f1c, map   an exact ID or a unique ID prefix
//	3 days ago, 12h, 2w     the newest snapshot at or before that point
//	yesterday               the newest snapshot before today started
//	2026-09-20              the newest snapshot on or before that day
//	2026-09-20 14:30        the newest snapshot at or before that minute
func Resolve(snaps []Snapshot, sel string, now time.Time) (Snapshot, error) {
	if len(snaps) == 0 {
		return Snapshot{}, fmt.Errorf("no snapshots yet, run `frost backup` first")
	}
	sorted := slices.Clone(snaps)
	slices.SortFunc(sorted, func(a, b Snapshot) int { return b.Time.Compare(a.Time) }) // newest first

	sel = strings.TrimSpace(strings.ToLower(sel))
	if sel == "" || sel == "latest" {
		return sorted[0], nil
	}

	// IDs first, so an ID can never be mistaken for a time.
	var matches []Snapshot
	for _, s := range sorted {
		if s.ID == sel {
			return s, nil
		}
		if strings.HasPrefix(s.ID, sel) {
			matches = append(matches, s)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Snapshot{}, fmt.Errorf("%q matches %d snapshots, use more of the ID", sel, len(matches))
	}

	at, err := ParseTime(sel, now)
	if err != nil {
		return Snapshot{}, err
	}
	for _, s := range sorted {
		if !s.Time.After(at) {
			return s, nil
		}
	}
	return Snapshot{}, fmt.Errorf("no snapshot at or before %s (oldest is %s)",
		at.Format("2006-01-02 15:04"), sorted[len(sorted)-1].Time.Local().Format("2006-01-02 15:04"))
}

var relRE = regexp.MustCompile(`^(\d+)\s*([a-z]+?)s?(\s+ago)?$`)

var units = map[string]time.Duration{
	"m": time.Minute, "min": time.Minute, "minute": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hour": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour,
}

// ParseTime turns a relative or absolute time expression into a point in
// time. Dates are read in local time, and a bare date means the end of that day.
func ParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "now":
		return now, nil
	case "today":
		return endOfDay(now), nil
	case "yesterday":
		return endOfDay(now.AddDate(0, 0, -1)), nil
	}

	if m := relRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "month", "mo":
			return now.AddDate(0, -n, 0), nil
		case "year", "y", "yr":
			return now.AddDate(-n, 0, 0), nil
		}
		if u, ok := units[m[2]]; ok {
			return now.Add(-time.Duration(n) * u), nil
		}
	}

	if t, err := time.ParseInLocation("2006-01-02 15:04", s, now.Location()); err == nil {
		return t.Add(time.Minute - time.Nanosecond), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return endOfDay(t), nil
	}
	return time.Time{}, fmt.Errorf("can't read %q as a snapshot ID or time (try \"latest\", \"3 days ago\" or \"2026-09-20\")", s)
}

func endOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 23, 59, 59, int(time.Second-time.Nanosecond), t.Location())
}

package snapshot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Resolve picks one snapshot out of snaps using a selector:
//
//	latest (or empty)       the newest snapshot
//	maple-absurd-3f1c, map  an exact ID or a unique ID prefix
//	3 days ago, 12h, 2w     the newest snapshot at or before that point
//	yesterday               the newest snapshot before today started
//	2026-09-20              the newest snapshot on or before that day
//	2026-09-20 14:30        the newest snapshot at or before that minute
func Resolve(snaps []Snapshot, sel string, now time.Time) (Snapshot, error) {
	if len(snaps) == 0 {
		return Snapshot{}, fmt.Errorf("no snapshots yet, run `frost backup` first")
	}
	sel = strings.TrimSpace(strings.ToLower(sel))
	if sel == "" || sel == "latest" {
		newest := snaps[0]
		for _, s := range snaps[1:] {
			if s.Time.After(newest.Time) {
				newest = s
			}
		}
		return newest, nil
	}

	// IDs first, so an ID can never be mistaken for a time.
	var exact, match Snapshot
	matches, foundExact := 0, false
	for _, s := range snaps {
		if s.ID == sel {
			if !foundExact || s.Time.After(exact.Time) {
				exact = s
			}
			foundExact = true
		}
		if strings.HasPrefix(s.ID, sel) {
			match = s
			matches++
		}
	}
	if foundExact {
		return exact, nil
	}
	if matches == 1 {
		return match, nil
	}
	if matches > 1 {
		return Snapshot{}, fmt.Errorf("%q matches %d snapshots, use more of the ID", sel, matches)
	}

	at, err := ParseTime(sel, now)
	if err != nil {
		return Snapshot{}, err
	}
	var selected Snapshot
	found := false
	oldest := snaps[0].Time
	for _, s := range snaps {
		if s.Time.Before(oldest) {
			oldest = s.Time
		}
		if !s.Time.After(at) && (!found || s.Time.After(selected.Time)) {
			selected, found = s, true
		}
	}
	if found {
		return selected, nil
	}
	return Snapshot{}, fmt.Errorf("no snapshot at or before %s (oldest is %s)",
		at.Format("2006-01-02 15:04"), oldest.Local().Format("2006-01-02 15:04"))
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
		n, err := strconv.Atoi(m[1])
		if err != nil || n > 100000 {
			return time.Time{}, fmt.Errorf("relative time %q is too large", s)
		}
		switch m[2] {
		case "month", "mo":
			return now.AddDate(0, -n, 0), nil
		case "year", "y", "yr":
			return now.AddDate(-n, 0, 0), nil
		}
		if u, ok := units[m[2]]; ok {
			if int64(n) > (1<<63-1)/int64(u) {
				return time.Time{}, fmt.Errorf("relative time %q is too large", s)
			}
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

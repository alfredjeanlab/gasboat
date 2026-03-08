package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpr represents a parsed cron expression (5 fields: min hour dom month dow).
type CronExpr struct {
	Minutes    []bool // [0..59]
	Hours      []bool // [0..23]
	DaysOfMonth []bool // [1..31]
	Months     []bool // [1..12]
	DaysOfWeek []bool // [0..6] (0=Sunday)
}

// ParseCron parses a standard 5-field cron expression.
// Supports: *, ranges (1-5), steps (*/5), lists (1,3,5), and named months/days.
func ParseCron(expr string) (*CronExpr, error) {
	// Handle common aliases.
	switch expr {
	case "@yearly", "@annually":
		expr = "0 0 1 1 *"
	case "@monthly":
		expr = "0 0 1 * *"
	case "@weekly":
		expr = "0 0 * * 0"
	case "@daily", "@midnight":
		expr = "0 0 * * *"
	case "@hourly":
		expr = "0 * * * *"
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	minutes, err := parseField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hours, err := parseField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	dom, err := parseField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	months, err := parseField(fields[3], 1, 12, monthNames)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	dow, err := parseField(fields[4], 0, 6, dowNames)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}

	return &CronExpr{
		Minutes:     minutes,
		Hours:       hours,
		DaysOfMonth: dom,
		Months:      months,
		DaysOfWeek:  dow,
	}, nil
}

// Prev returns the most recent time before or equal to t that matches the cron expression.
// Returns zero time if no match is found within 366 days.
func (c *CronExpr) Prev(t time.Time) time.Time {
	// Truncate to the current minute.
	t = t.Truncate(time.Minute)

	// Search backwards up to 366 days.
	limit := t.Add(-366 * 24 * time.Hour)

	for t.After(limit) {
		if !c.matchMonth(t) {
			// Go to end of previous month.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if !c.matchDay(t) {
			// Go to end of previous day.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if !c.matchHour(t) {
			// Go to end of previous hour.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if !c.matchMinute(t) {
			t = t.Add(-time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// Next returns the next time after t that matches the cron expression.
// Returns zero time if no match is found within 366 days.
func (c *CronExpr) Next(t time.Time) time.Time {
	// Start from the next minute.
	t = t.Truncate(time.Minute).Add(time.Minute)

	limit := t.Add(366 * 24 * time.Hour)

	for t.Before(limit) {
		if !c.matchMonth(t) {
			// Skip to first day of next month.
			if t.Month() == 12 {
				t = time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, t.Location())
			} else {
				t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			}
			continue
		}
		if !c.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.matchHour(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !c.matchMinute(t) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (c *CronExpr) matchMonth(t time.Time) bool {
	return c.Months[int(t.Month())]
}

func (c *CronExpr) matchDay(t time.Time) bool {
	return c.DaysOfMonth[t.Day()] && c.DaysOfWeek[int(t.Weekday())]
}

func (c *CronExpr) matchHour(t time.Time) bool {
	return c.Hours[t.Hour()]
}

func (c *CronExpr) matchMinute(t time.Time) bool {
	return c.Minutes[t.Minute()]
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4,
	"may": 5, "jun": 6, "jul": 7, "aug": 8,
	"sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3,
	"thu": 4, "fri": 5, "sat": 6,
}

// parseField parses a single cron field into a boolean slice.
// The slice is indexed from 0 to max; for fields starting at 1 (months, DOM),
// index 0 is unused.
func parseField(field string, min, max int, names map[string]int) ([]bool, error) {
	bits := make([]bool, max+1)

	for _, part := range strings.Split(field, ",") {
		if err := parsePart(part, min, max, names, bits); err != nil {
			return nil, err
		}
	}
	return bits, nil
}

func parsePart(part string, min, max int, names map[string]int, bits []bool) error {
	// Handle step: "*/2", "1-10/3"
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		var err error
		step, err = strconv.Atoi(part[idx+1:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step in %q", part)
		}
		part = part[:idx]
	}

	// Handle wildcard.
	if part == "*" {
		for i := min; i <= max; i += step {
			bits[i] = true
		}
		return nil
	}

	// Handle range: "1-5"
	if idx := strings.Index(part, "-"); idx >= 0 {
		lo, err := resolveValue(part[:idx], names)
		if err != nil {
			return err
		}
		hi, err := resolveValue(part[idx+1:], names)
		if err != nil {
			return err
		}
		if lo < min || hi > max || lo > hi {
			return fmt.Errorf("range %d-%d out of bounds [%d,%d]", lo, hi, min, max)
		}
		for i := lo; i <= hi; i += step {
			bits[i] = true
		}
		return nil
	}

	// Single value.
	val, err := resolveValue(part, names)
	if err != nil {
		return err
	}
	if val < min || val > max {
		return fmt.Errorf("value %d out of bounds [%d,%d]", val, min, max)
	}
	bits[val] = true
	return nil
}

func resolveValue(s string, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return v, nil
}

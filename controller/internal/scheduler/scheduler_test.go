package scheduler

import (
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{"0 0 * * *", false},       // daily at midnight
		{"*/5 * * * *", false},     // every 5 minutes
		{"0 9 * * 1-5", false},     // weekdays at 9am
		{"0 0 1 * *", false},       // monthly
		{"30 2 * * *", false},      // 2:30 AM
		{"0 0 1 1 *", false},       // yearly
		{"@daily", false},          // alias
		{"@hourly", false},         // alias
		{"@weekly", false},         // alias
		{"@monthly", false},        // alias
		{"@yearly", false},         // alias
		{"0 9 * * mon-fri", false}, // named days
		{"0 0 1 jan *", false},     // named month
		{"bad", true},              // invalid
		{"0 0 0 * *", true},       // day 0 out of bounds
		{"60 0 * * *", true},       // minute 60 out of bounds
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := ParseCron(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestCronPrev(t *testing.T) {
	tests := []struct {
		name string
		expr string
		now  time.Time
		want time.Time
	}{
		{
			name: "daily midnight, checked at 3am",
			expr: "0 0 * * *",
			now:  time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "daily midnight, checked at midnight exactly",
			expr: "0 0 * * *",
			now:  time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "every 5 min, checked at 10:13",
			expr: "*/5 * * * *",
			now:  time.Date(2026, 3, 7, 10, 13, 0, 0, time.UTC),
			want: time.Date(2026, 3, 7, 10, 10, 0, 0, time.UTC),
		},
		{
			name: "weekdays 9am, checked Saturday",
			expr: "0 9 * * 1-5",
			now:  time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), // Saturday
			want: time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC),  // Friday
		},
		{
			name: "monthly 1st at midnight, checked mid-month",
			expr: "0 0 1 * *",
			now:  time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.expr, err)
			}
			got := expr.Prev(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("Prev(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestCronNext(t *testing.T) {
	tests := []struct {
		name string
		expr string
		now  time.Time
		want time.Time
	}{
		{
			name: "daily midnight, checked at 3am",
			expr: "0 0 * * *",
			now:  time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "every 5 min, checked at 10:13",
			expr: "*/5 * * * *",
			now:  time.Date(2026, 3, 7, 10, 13, 0, 0, time.UTC),
			want: time.Date(2026, 3, 7, 10, 15, 0, 0, time.UTC),
		},
		{
			name: "weekdays 9am, checked Saturday",
			expr: "0 9 * * 1-5",
			now:  time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), // Saturday
			want: time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC),  // Monday
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.expr, err)
			}
			got := expr.Next(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("Next(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestCronAliases(t *testing.T) {
	tests := []struct {
		alias    string
		equivExpr string
	}{
		{"@daily", "0 0 * * *"},
		{"@midnight", "0 0 * * *"},
		{"@hourly", "0 * * * *"},
		{"@weekly", "0 0 * * 0"},
		{"@monthly", "0 0 1 * *"},
		{"@yearly", "0 0 1 1 *"},
		{"@annually", "0 0 1 1 *"},
	}

	now := time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			a, err := ParseCron(tt.alias)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.alias, err)
			}
			b, err := ParseCron(tt.equivExpr)
			if err != nil {
				t.Fatalf("ParseCron(%q) error: %v", tt.equivExpr, err)
			}
			aPrev := a.Prev(now)
			bPrev := b.Prev(now)
			if !aPrev.Equal(bPrev) {
				t.Errorf("Prev mismatch: %q=%v, %q=%v", tt.alias, aPrev, tt.equivExpr, bPrev)
			}
		})
	}
}

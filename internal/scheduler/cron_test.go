package scheduler

import (
	"testing"
	"time"
)

func TestLatestDueIsUTCAndBoundsCatchUp(t *testing.T) {
	rule, err := Parse("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 26, 12, 13, 20, 0, time.FixedZone("UTC+7", 7*60*60))
	want := time.Date(2026, 11, 26, 5, 10, 0, 0, time.UTC)
	if due := LatestDue(rule, now.Add(-time.Hour), now); !due.Equal(want) || due.Location() != time.UTC {
		t.Fatalf("due = %v, want %v", due, want)
	}
	if due := LatestDue(rule, want, now); !due.IsZero() {
		t.Fatalf("checkpoint replayed: %v", due)
	}
	hourly, err := Parse("0 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if due := LatestDue(hourly, now.Add(-time.Hour), now.Add(8*time.Minute)); !due.IsZero() {
		t.Fatalf("expired occurrence replayed: %v", due)
	}
}

func TestParseRejectsNonUTCMinuteSchedules(t *testing.T) {
	for _, expr := range []string{"@every 1m", "* * * * * *", "CRON_TZ=Asia/Ho_Chi_Minh * * * * *", "*/0 * * * *", "99 * * * *"} {
		if _, err := Parse(expr); err == nil {
			t.Fatalf("accepted invalid expression %q", expr)
		}
	}
}

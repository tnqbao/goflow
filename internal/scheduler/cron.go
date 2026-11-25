// Package scheduler calculates workflow schedule occurrences in UTC.
package scheduler

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const CatchUpWindow = 15 * time.Minute

func Parse(expression string) (cron.Schedule, error) {
	if len(expression) > 128 || len(strings.Fields(expression)) != 5 || strings.Contains(expression, "=") {
		return nil, fmt.Errorf("schedule must be a five-field UTC cron expression")
	}
	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression")
	}
	return schedule, nil
}

// LatestDue returns the newest occurrence after the checkpoint in the catch-up window.
func LatestDue(schedule cron.Schedule, after, now time.Time) time.Time {
	start := now.UTC().Add(-CatchUpWindow)
	if after.After(start) {
		start = after.UTC()
	}
	var due time.Time
	for next := schedule.Next(start); !next.IsZero() && !next.After(now); next = schedule.Next(next) {
		due = next
	}
	return due
}

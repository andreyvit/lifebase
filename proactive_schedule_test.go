package main

import (
	"testing"
	"time"
)

func TestShouldTriggerProactive(t *testing.T) {
	oldBoundary := config.DayBoundaryHour
	oldLocal := time.Local
	time.Local = time.FixedZone("TestLocal", 0)
	config.DayBoundaryHour = 5
	t.Cleanup(func() {
		config.DayBoundaryHour = oldBoundary
		time.Local = oldLocal
	})

	tm := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, time.Local)
	}
	hm := func(s string) HourMinute {
		t.Helper()
		v, err := parseHourMinute(s)
		if err != nil {
			t.Fatalf("parseHourMinute(%q): %v", s, err)
		}
		return v
	}

	tests := []struct {
		name       string
		schedule   []HourMinute
		lastRun    time.Time
		now        time.Time
		wantRun    bool
		wantTarget time.Time
	}{
		{
			name:     "no schedule",
			schedule: nil,
			now:      tm(2026, time.January, 2, 8, 10),
			wantRun:  false,
		},
		{
			name:       "single due slot",
			schedule:   []HourMinute{hm("8:00")},
			now:        tm(2026, time.January, 2, 8, 10),
			wantRun:    true,
			wantTarget: tm(2026, time.January, 2, 8, 0),
		},
		{
			name:     "missed slot is not due after 60m",
			schedule: []HourMinute{hm("13:00")},
			now:      tm(2026, time.January, 2, 14, 1),
			wantRun:  false,
		},
		{
			name:     "already ran at/after target",
			schedule: []HourMinute{hm("8:00")},
			lastRun:  tm(2026, time.January, 2, 8, 5),
			now:      tm(2026, time.January, 2, 8, 10),
			wantRun:  false,
		},
		{
			name:     "preemptive run suppresses single slot",
			schedule: []HourMinute{hm("8:00")},
			lastRun:  tm(2026, time.January, 2, 7, 50),
			now:      tm(2026, time.January, 2, 8, 10),
			wantRun:  false,
		},
		{
			name:       "preemptive run does not suppress when another slot exists in prior hour",
			schedule:   []HourMinute{hm("7:30"), hm("8:00")},
			lastRun:    tm(2026, time.January, 2, 7, 50),
			now:        tm(2026, time.January, 2, 8, 10),
			wantRun:    true,
			wantTarget: tm(2026, time.January, 2, 8, 0),
		},
		{
			name:       "latest due slot wins",
			schedule:   []HourMinute{hm("14:20"), hm("14:45")},
			now:        tm(2026, time.January, 2, 14, 50),
			wantRun:    true,
			wantTarget: tm(2026, time.January, 2, 14, 45),
		},
		{
			name:       "latest due slot wins even after an hour overlap",
			schedule:   []HourMinute{hm("14:20"), hm("14:45")},
			now:        tm(2026, time.January, 2, 15, 10),
			wantRun:    true,
			wantTarget: tm(2026, time.January, 2, 14, 45),
		},
		{
			name:       "preemptive run does not suppress later slot when another slot exists in prior hour",
			schedule:   []HourMinute{hm("14:20"), hm("14:45")},
			lastRun:    tm(2026, time.January, 2, 14, 30),
			now:        tm(2026, time.January, 2, 14, 50),
			wantRun:    true,
			wantTarget: tm(2026, time.January, 2, 14, 45),
		},
		{
			name:     "wrap-around slot suppresses with day boundary",
			schedule: []HourMinute{hm("23:15"), hm("3:00")},
			lastRun:  tm(2026, time.January, 2, 2, 50),
			now:      tm(2026, time.January, 2, 3, 10),
			wantRun:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := shouldTriggerProactive(tc.schedule, tc.lastRun, tc.now)
			if ok != tc.wantRun {
				t.Fatalf("shouldTriggerProactive ok=%v, want %v (target=%v)", ok, tc.wantRun, target)
			}
			if !tc.wantRun {
				return
			}
			if !target.Equal(tc.wantTarget) {
				t.Fatalf("target=%v, want %v", target, tc.wantTarget)
			}
		})
	}
}

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/andreyvit/lifebase/applehealth"
)

type healthTimelineItem struct {
	t        time.Time
	priority int
	text     string
}

func renderAppleHealthLast48Hours(now time.Time) (string, error) {
	exportDir := strings.TrimSpace(appleHealthExportDir)
	if exportDir == "" {
		return "Apple Health export is not configured (set apple_health_export_dir in lifebase config).", nil
	}
	if _, err := os.Stat(exportDir); err != nil {
		return fmt.Sprintf("Apple Health export dir is not accessible: %v", err), nil
	}

	now = now.Local()
	from := now.Add(-48 * time.Hour)

	events, err := applehealth.LoadEvents(exportDir, from, now)
	if err != nil {
		return "", err
	}

	effectiveTo := now
	if len(events) > 0 {
		effectiveTo = timelineEndFromEvents(now, events)
	}
	buckets, err := applehealth.Bucketize(events, from, effectiveTo, time.Hour)
	if err != nil {
		return "", err
	}

	var items []healthTimelineItem
	for _, b := range buckets {
		ts := b.Start.Format("2006-01-02 15:04")
		if b.Samples == 0 {
			items = append(items, healthTimelineItem{t: b.Start, priority: 0, text: fmt.Sprintf("%s - (no data)", ts)})
			continue
		}
		items = append(items, healthTimelineItem{
			t:        b.Start,
			priority: 0,
			text: fmt.Sprintf("%s - steps %d, stand %d h, exercise %d min, active energy %d kcal",
				ts,
				applehealth.RoundInt(b.Steps),
				applehealth.RoundInt(b.StandHours),
				applehealth.RoundInt(b.ExerciseMinutes),
				applehealth.RoundInt(b.ActiveEnergyKcal),
			),
		})
	}
	for _, e := range events {
		if e.Kind != applehealth.EventWeightKg {
			continue
		}
		if e.Time.After(effectiveTo) {
			continue
		}
		ts := e.Time.Format("2006-01-02 15:04")
		items = append(items, healthTimelineItem{
			t:        e.Time,
			priority: 1,
			text:     fmt.Sprintf("%s - weight %.1f kg", ts, applehealth.Round1(e.Value)),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].t.Before(items[j].t) {
			return true
		}
		if items[j].t.Before(items[i].t) {
			return false
		}
		return items[i].priority < items[j].priority
	})

	var out strings.Builder
	out.WriteString("Apple Health — last 48h:\n\n")
	for _, it := range items {
		out.WriteString(it.text)
		out.WriteByte('\n')
	}
	if effectiveTo.Before(now) {
		out.WriteString(fmt.Sprintf("\nNote: export seems stale; data through %s\n", effectiveTo.Format("2006-01-02 15:04")))
	}
	return strings.TrimSpace(out.String()), nil
}

func builtinAppleHealthTodayContext(now time.Time) (string, error) {
	exportDir := strings.TrimSpace(appleHealthExportDir)
	if exportDir == "" {
		return "", nil
	}
	if _, err := os.Stat(exportDir); err != nil {
		return "", nil
	}

	now = now.Local()
	loc := now.Location()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dayBoundary := time.Date(now.Year(), now.Month(), now.Day(), config.DayBoundaryHour, 0, 0, 0, loc)
	includeYesterday := config.DayBoundaryHour > 0 && now.Before(dayBoundary)

	var sections []string

	if includeYesterday {
		yesterdayStart := todayStart.AddDate(0, 0, -1)
		events, err := applehealth.LoadEvents(exportDir, yesterdayStart, todayStart)
		if err != nil {
			return "", err
		}
		if len(events) > 0 {
			effectiveTo := timelineEndFromEvents(todayStart, events)
			buckets, err := applehealth.Bucketize(events, yesterdayStart, effectiveTo, time.Hour)
			if err != nil {
				return "", err
			}

			var items []healthTimelineItem
			for _, b := range buckets {
				ts := b.Start.Format("15:04")
				if b.Samples == 0 {
					items = append(items, healthTimelineItem{t: b.Start, priority: 0, text: fmt.Sprintf("%s - (no data)", ts)})
					continue
				}
				items = append(items, healthTimelineItem{
					t:        b.Start,
					priority: 0,
					text: fmt.Sprintf("%s - steps %d, stand %d h, exercise %d min, active energy %d kcal",
						ts,
						applehealth.RoundInt(b.Steps),
						applehealth.RoundInt(b.StandHours),
						applehealth.RoundInt(b.ExerciseMinutes),
						applehealth.RoundInt(b.ActiveEnergyKcal),
					),
				})
			}
			for _, e := range events {
				if e.Kind != applehealth.EventWeightKg {
					continue
				}
				ts := e.Time.Format("15:04")
				items = append(items, healthTimelineItem{
					t:        e.Time,
					priority: 1,
					text:     fmt.Sprintf("%s - weight %.1f kg", ts, applehealth.Round1(e.Value)),
				})
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].t.Before(items[j].t) {
					return true
				}
				if items[j].t.Before(items[i].t) {
					return false
				}
				return items[i].priority < items[j].priority
			})
			var b strings.Builder
			b.WriteString(yesterdayStart.Format("2006-01-02 Mon"))
			b.WriteByte('\n')
			for _, it := range items {
				b.WriteString(it.text)
				b.WriteByte('\n')
			}
			if effectiveTo.Before(todayStart) {
				b.WriteString(fmt.Sprintf("Note: export seems stale; data through %s\n", effectiveTo.Format("15:04")))
			}
			sections = append(sections, strings.TrimSpace(b.String()))
		}
	}

	{
		from := todayStart
		to := now

		events, err := applehealth.LoadEvents(exportDir, from, to)
		if err != nil {
			return "", err
		}
		if len(events) > 0 {
			effectiveTo := timelineEndFromEvents(now, events)
			if effectiveTo.Before(now) {
				to = effectiveTo
			}

			cut := to.Add(-2 * time.Hour)
			recentStart := time.Date(
				cut.Year(),
				cut.Month(),
				cut.Day(),
				cut.Hour(),
				(cut.Minute()/15)*15,
				0, 0, loc,
			)
			if recentStart.Before(todayStart) {
				recentStart = todayStart
			}

			hourlyBuckets, err := applehealth.Bucketize(events, todayStart, minTime(recentStart, to), time.Hour)
			if err != nil {
				return "", err
			}
			recentBuckets, err := applehealth.Bucketize(events, minTime(recentStart, to), to, 15*time.Minute)
			if err != nil {
				return "", err
			}

			var items []healthTimelineItem
			for _, b := range hourlyBuckets {
				ts := b.Start.Format("15:04")
				if b.Samples == 0 {
					items = append(items, healthTimelineItem{t: b.Start, priority: 0, text: fmt.Sprintf("%s - (no data)", ts)})
					continue
				}
				items = append(items, healthTimelineItem{
					t:        b.Start,
					priority: 0,
					text: fmt.Sprintf("%s - steps %d, stand %d h, exercise %d min, active energy %d kcal",
						ts,
						applehealth.RoundInt(b.Steps),
						applehealth.RoundInt(b.StandHours),
						applehealth.RoundInt(b.ExerciseMinutes),
						applehealth.RoundInt(b.ActiveEnergyKcal),
					),
				})
			}
			for _, b := range recentBuckets {
				ts := b.Start.Format("15:04")
				if b.Samples == 0 {
					items = append(items, healthTimelineItem{t: b.Start, priority: 0, text: fmt.Sprintf("%s - (no data)", ts)})
					continue
				}
				items = append(items, healthTimelineItem{
					t:        b.Start,
					priority: 0,
					text: fmt.Sprintf("%s - steps %d, stand %d h, exercise %d min, active energy %d kcal",
						ts,
						applehealth.RoundInt(b.Steps),
						applehealth.RoundInt(b.StandHours),
						applehealth.RoundInt(b.ExerciseMinutes),
						applehealth.RoundInt(b.ActiveEnergyKcal),
					),
				})
			}
			for _, e := range events {
				if e.Kind != applehealth.EventWeightKg {
					continue
				}
				ts := e.Time.Format("15:04")
				items = append(items, healthTimelineItem{
					t:        e.Time,
					priority: 1,
					text:     fmt.Sprintf("%s - weight %.1f kg", ts, applehealth.Round1(e.Value)),
				})
			}

			sort.Slice(items, func(i, j int) bool {
				if items[i].t.Before(items[j].t) {
					return true
				}
				if items[j].t.Before(items[i].t) {
					return false
				}
				return items[i].priority < items[j].priority
			})
			var b strings.Builder
			b.WriteString(todayStart.Format("2006-01-02 Mon"))
			b.WriteByte('\n')
			for _, it := range items {
				b.WriteString(it.text)
				b.WriteByte('\n')
			}
			if to.Before(now) {
				b.WriteString(fmt.Sprintf("Note: export seems stale; data through %s\n", to.Format("15:04")))
			}
			sections = append(sections, strings.TrimSpace(b.String()))
		}
	}

	if len(sections) == 0 {
		return "", nil
	}

	var out strings.Builder
	out.WriteString("<health_data>\n")
	out.WriteString(strings.Join(sections, "\n\n"))
	out.WriteString("\n</health_data>\n")
	return strings.TrimSpace(out.String()), nil
}

func timelineEndFromEvents(defaultEnd time.Time, events []applehealth.Event) time.Time {
	defaultEnd = defaultEnd.Local()
	latest, ok := latestNonWeightSampleTime(events)
	if !ok {
		return defaultEnd
	}
	// Don't emit buckets beyond the last synced sample; use 15-minute granularity.
	cap := time.Date(
		latest.Year(),
		latest.Month(),
		latest.Day(),
		latest.Hour(),
		(latest.Minute()/15)*15,
		0, 0, latest.Location(),
	).Add(15 * time.Minute)
	if cap.Before(defaultEnd) {
		return cap
	}
	return defaultEnd
}

func latestNonWeightSampleTime(events []applehealth.Event) (time.Time, bool) {
	var latest time.Time
	for _, e := range events {
		if e.Kind == applehealth.EventWeightKg {
			continue
		}
		if latest.IsZero() || latest.Before(e.Time) {
			latest = e.Time
		}
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest.Local(), true
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

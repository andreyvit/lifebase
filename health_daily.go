package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/andreyvit/lifebase/applehealth"
)

func maybeRunHealthDayChangeProcessing(ctx context.Context, now time.Time) {
	exportDir := strings.TrimSpace(appleHealthExportDir)
	if exportDir == "" {
		return
	}
	if _, err := os.Stat(exportDir); err != nil {
		return
	}

	now = now.Local()
	// Apple Health days are calendar days (midnight-to-midnight in local time).
	// We only snapshot days that are complete, i.e. up to yesterday.
	loc := now.Location()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dayBoundary := time.Date(now.Year(), now.Month(), now.Day(), config.DayBoundaryHour, 0, 0, 0, loc)
	lastCompleteDayStart := todayStart.AddDate(0, 0, -1)
	if now.Before(dayBoundary) {
		// Give iOS -> export sync time; don't snapshot yesterday until after our day boundary.
		lastCompleteDayStart = todayStart.AddDate(0, 0, -2)
	}

	var lastKey string
	ReadState(func(s *State) {
		lastKey = strings.TrimSpace(s.HealthDailyLastProcessedDay)
	})

	var (
		lastDayStart time.Time
		hasLast      bool
	)
	if lastKey != "" {
		if t, err := time.ParseInLocation("2006-01-02", lastKey, loc); err == nil {
			lastDayStart = t
			hasLast = true
		} else {
			log.Printf("Health day-change: invalid last processed day %q; resetting", lastKey)
		}
	}
	if hasLast {
		if ok, err := healthLogHasDayEntry(lastDayStart); err == nil {
			if !ok {
				// State claims the day is processed, but log doesn't contain it (or was removed).
				// Treat as first-run to avoid skipping a day.
				hasLast = false
			}
		} else {
			log.Printf("Health day-change: cannot verify HealthLog for %s: %v", lastDayStart.Format("2006-01-02"), err)
			hasLast = false
		}
	}
	if !hasLast {
		// First run (or invalid state): attempt snapshot for yesterday only.
		lastDayStart = lastCompleteDayStart.AddDate(0, 0, -1)
	}
	if !lastCompleteDayStart.After(lastDayStart) {
		return
	}

	cursor := lastDayStart
	for {
		next := cursor.AddDate(0, 0, 1)
		if next.After(lastCompleteDayStart) {
			break
		}
		requireFresh := next.Equal(lastCompleteDayStart)
		if err := writeHealthSnapshotForDay(ctx, next, now, requireFresh); err != nil {
			log.Printf("Health snapshot %s failed: %v", next.Format("2006-01-02"), err)
			break
		}
		exists, err := healthLogHasDayEntry(next)
		if err != nil {
			log.Printf("Health snapshot %s verify failed: %v", next.Format("2006-01-02"), err)
			break
		}
		if !exists {
			// Likely missing export file (sync delay); retry on next invocation.
			break
		}
		cursor = next
	}

	if cursor.After(lastDayStart) {
		UpdateState(func(s *State) { s.HealthDailyLastProcessedDay = cursor.Format("2006-01-02") })
	}
}

func writeHealthSnapshotForDay(ctx context.Context, dayStart, now time.Time, requireFresh bool) error {
	if requireFresh {
		ok, err := appleHealthExportFreshEnough(dayStart, now)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	s, ok, err := applehealth.ReadDaySummary(appleHealthExportDir, dayStart)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	exists, err := healthLogHasDayEntry(dayStart)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	when := time.Date(s.DayStart.Year(), s.DayStart.Month(), s.DayStart.Day(), 23, 59, 0, 0, s.DayStart.Location())

	var parts []string
	parts = append(parts, fmt.Sprintf("steps %d", applehealth.RoundInt(s.Steps)))
	parts = append(parts, fmt.Sprintf("stand %d h", applehealth.RoundInt(s.StandHours)))
	if s.WeightKg != nil {
		parts = append(parts, fmt.Sprintf("weight %.1f kg", applehealth.Round1(*s.WeightKg)))
	}
	parts = append(parts, fmt.Sprintf("exercise %d min", applehealth.RoundInt(s.ExerciseMinutes)))
	parts = append(parts, fmt.Sprintf("active energy %d kcal", applehealth.RoundInt(s.ActiveEnergyKcal)))

	spec, err := findLogSpec("HealthLog")
	if err != nil {
		return err
	}
	if spec == nil {
		return nil
	}
	_, err = addLogEntry(ctx, *spec, when, strings.Join(parts, ", "))
	return err
}

func healthLogHasDayEntry(dayStart time.Time) (bool, error) {
	spec, err := findLogSpec("HealthLog")
	if err != nil {
		return false, err
	}
	if spec == nil {
		return false, nil
	}
	fn := spec.logFilePath(dayStart)
	lines, err := readLogFileNonEmptyLines(fn)
	if err != nil {
		return false, err
	}
	dayKey := dayStart.Local().Format("2006-01-02")
	prefix := "- " + dayKey + " "
	for _, ln := range lines {
		if strings.HasPrefix(ln, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func appleHealthExportFreshEnough(dayStart, now time.Time) (bool, error) {
	dayStart = time.Date(dayStart.Local().Year(), dayStart.Local().Month(), dayStart.Local().Day(), 0, 0, 0, 0, dayStart.Local().Location())
	now = now.Local()
	// We consider the export "fresh enough" if the JSON file has been updated
	// after the day is complete (i.e. after midnight of the next calendar day).
	// We still only *attempt* this after our configured day boundary to give iCloud sync time.
	expectedAfter := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 0, 0, 0, 0, dayStart.Location()).
		AddDate(0, 0, 1)
	if now.Before(expectedAfter) {
		return false, nil
	}

	fn := applehealth.DayFilePath(appleHealthExportDir, dayStart)
	fi, err := os.Stat(fn)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !fi.ModTime().Local().Before(expectedAfter), nil
}

package applehealth

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	exportFilePrefix = "HealthAutoExport-"
	exportFileSuffix = ".json"

	exportDateLayout = "2006-01-02 15:04:05 -0700"
)

type EventKind string

const (
	EventSteps          EventKind = "steps"
	EventActiveEnergy   EventKind = "active_energy_kcal"
	EventExerciseMinute EventKind = "exercise_minute"
	EventStandHour      EventKind = "stand_hour"
	EventWeightKg       EventKind = "weight_kg"
)

type Event struct {
	Time  time.Time
	Kind  EventKind
	Value float64
}

type Bucket struct {
	Start time.Time
	End   time.Time

	Samples int

	Steps            float64
	StandHours       float64
	ExerciseMinutes  float64
	ActiveEnergyKcal float64
}

type DaySummary struct {
	DayStart time.Time

	Steps            float64
	StandHours       float64
	ExerciseMinutes  float64
	ActiveEnergyKcal float64
	WeightKg         *float64
}

func DayFilePath(exportDir string, day time.Time) string {
	day = day.Local()
	return filepath.Join(exportDir, fmt.Sprintf("%s%04d-%02d-%02d%s", exportFilePrefix, day.Year(), int(day.Month()), day.Day(), exportFileSuffix))
}

func LoadEvents(exportDir string, from, to time.Time) ([]Event, error) {
	exportDir = strings.TrimSpace(exportDir)
	if exportDir == "" {
		return nil, nil
	}
	from = from.Local()
	to = to.Local()
	if !to.After(from) {
		return nil, nil
	}

	loc := from.Location()
	startDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	endDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)

	var out []Event
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		fn := DayFilePath(exportDir, day)
		ev, ok, err := loadEventsFromFile(fn)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, e := range ev {
			if !e.Time.Before(from) && e.Time.Before(to) {
				out = append(out, e)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func ReadDaySummary(exportDir string, dayStart time.Time) (DaySummary, bool, error) {
	exportDir = strings.TrimSpace(exportDir)
	if exportDir == "" {
		return DaySummary{}, false, nil
	}
	dayStart = time.Date(dayStart.Local().Year(), dayStart.Local().Month(), dayStart.Local().Day(), 0, 0, 0, 0, dayStart.Local().Location())

	fn := DayFilePath(exportDir, dayStart)
	events, ok, err := loadEventsFromFile(fn)
	if err != nil || !ok {
		return DaySummary{}, ok, err
	}

	var (
		steps            float64
		standHours       float64
		exerciseMinutes  float64
		activeEnergyKcal float64
		latestWeight     *Event
	)
	for i := range events {
		e := events[i]
		switch e.Kind {
		case EventSteps:
			steps += e.Value
		case EventStandHour:
			standHours += e.Value
		case EventExerciseMinute:
			exerciseMinutes += e.Value
		case EventActiveEnergy:
			activeEnergyKcal += e.Value
		case EventWeightKg:
			if latestWeight == nil || latestWeight.Time.Before(e.Time) {
				cp := e
				latestWeight = &cp
			}
		}
	}

	var weightKg *float64
	if latestWeight != nil {
		v := latestWeight.Value
		weightKg = &v
	}

	return DaySummary{
		DayStart:         dayStart,
		Steps:            steps,
		StandHours:       standHours,
		ExerciseMinutes:  exerciseMinutes,
		ActiveEnergyKcal: activeEnergyKcal,
		WeightKg:         weightKg,
	}, true, nil
}

func Bucketize(events []Event, from, to time.Time, dur time.Duration) ([]Bucket, error) {
	from = from.Local()
	to = to.Local()
	if !to.After(from) {
		return nil, nil
	}

	start := floorTo(from, dur)
	bucketByStart := make(map[time.Time]int, 256)
	var buckets []Bucket
	for t := start; t.Before(to); t = t.Add(dur) {
		i := len(buckets)
		buckets = append(buckets, Bucket{
			Start: t,
			End:   t.Add(dur),
		})
		bucketByStart[t] = i
	}

	for _, e := range events {
		if e.Kind == EventWeightKg {
			continue
		}
		if e.Time.Before(from) || !e.Time.Before(to) {
			continue
		}
		key := floorTo(e.Time, dur)
		i, ok := bucketByStart[key]
		if !ok {
			continue
		}
		switch e.Kind {
		case EventSteps:
			buckets[i].Steps += e.Value
			buckets[i].Samples++
		case EventStandHour:
			buckets[i].StandHours += e.Value
			buckets[i].Samples++
		case EventExerciseMinute:
			buckets[i].ExerciseMinutes += e.Value
			buckets[i].Samples++
		case EventActiveEnergy:
			buckets[i].ActiveEnergyKcal += e.Value
			buckets[i].Samples++
		}
	}

	return buckets, nil
}

func RoundInt(x float64) int {
	return int(math.Round(x))
}

func Round1(x float64) float64 {
	return math.Round(x*10) / 10
}

func floorTo(t time.Time, d time.Duration) time.Time {
	t = t.Local()
	loc := t.Location()
	switch d {
	case time.Hour:
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
	case 15 * time.Minute:
		m := (t.Minute() / 15) * 15
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), m, 0, 0, loc)
	default:
		return t.Truncate(d)
	}
}

type exportDoc struct {
	Data struct {
		Metrics []exportMetric `json:"metrics"`
	} `json:"data"`
}

type exportMetric struct {
	Name  string        `json:"name"`
	Units string        `json:"units"`
	Data  []exportPoint `json:"data"`
}

type exportPoint struct {
	Date   string  `json:"date"`
	Qty    float64 `json:"qty"`
	Source string  `json:"source,omitempty"`
}

func loadEventsFromFile(fn string) ([]Event, bool, error) {
	b, err := os.ReadFile(fn)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var doc exportDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", filepath.Base(fn), err)
	}

	var out []Event
	for _, m := range doc.Data.Metrics {
		name := strings.TrimSpace(m.Name)
		units := strings.TrimSpace(m.Units)
		switch name {
		case "step_count":
			for _, p := range m.Data {
				tm, ok, err := parseExportTime(p.Date)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					continue
				}
				out = append(out, Event{Time: tm, Kind: EventSteps, Value: p.Qty})
			}
		case "active_energy":
			for _, p := range m.Data {
				tm, ok, err := parseExportTime(p.Date)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					continue
				}
				out = append(out, Event{Time: tm, Kind: EventActiveEnergy, Value: p.Qty})
			}
		case "apple_exercise_time":
			for _, p := range m.Data {
				tm, ok, err := parseExportTime(p.Date)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					continue
				}
				out = append(out, Event{Time: tm, Kind: EventExerciseMinute, Value: p.Qty})
			}
		case "apple_stand_hour":
			for _, p := range m.Data {
				tm, ok, err := parseExportTime(p.Date)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					continue
				}
				out = append(out, Event{Time: tm, Kind: EventStandHour, Value: p.Qty})
			}
		case "weight_body_mass":
			for _, p := range m.Data {
				tm, ok, err := parseExportTime(p.Date)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					continue
				}
				kg, ok := weightToKg(p.Qty, units)
				if !ok {
					continue
				}
				out = append(out, Event{Time: tm, Kind: EventWeightKg, Value: kg})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, true, nil
}

func parseExportTime(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	tm, err := time.Parse(exportDateLayout, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse time %q: %w", s, err)
	}
	return tm.Local(), true, nil
}

func weightToKg(qty float64, units string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(units)) {
	case "kg":
		return qty, true
	case "g":
		return qty / 1000, true
	case "lb", "lbs":
		return qty * 0.45359237, true
	default:
		return 0, false
	}
}

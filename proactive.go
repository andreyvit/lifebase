package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
)

var (
	proactiveModelRunner    = runProactiveModel
	proactiveTelegramSender = sendTelegramText
	proactiveHistoryWriter  = appendProactiveRecent
	proactiveNow            = func() time.Time { return time.Now().Local() }
)

// runProactiveByName runs a proactive item selected by a flexible name key
// (e.g. "evening", "evening brief", or "evening.md").
func runProactiveByName(ctx context.Context, key string) error {
	orig := strings.TrimSpace(key)
	key = normalizeKey(key)

	prompts, err := parsePrompts()
	if err != nil {
		log.Printf("Proactive: prompt parse warning: %v", err)
	}
	for _, p := range prompts {
		if p == nil {
			continue
		}
		if strings.TrimSpace(p.Key) != "" && p.Key == key {
			return runProactivePrompt(ctx, p, proactiveRunKey(p))
		}
	}
	return fmt.Errorf("unknown proactive prompt: %q", orig)
}

func runProactivePrompt(ctx context.Context, p *Prompt, lastRunKey string) error {
	log.Printf("Proactive: running %q via %s", p.Name, p.FileName)
	prompt := strings.TrimSpace(p.Body)

	out, err := proactiveModelRunner(ctx, prompt, proactiveHistorySuffix(p), "")
	if err != nil {
		return err
	}
	out = strings.TrimSpace(out)
	ranAt := proactiveNow()
	if out == "" {
		log.Printf("Proactive %q produced empty output; nothing to send", p.Name)
		markProactiveRun(lastRunKey, ranAt)
		return nil
	}
	if err := proactiveTelegramSender(ctx, out); err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	log.Printf("Proactive %q sent (%d chars)", p.Name, len(out))
	proactiveHistoryWriter(p.Name, ranAt, out)

	markProactiveRun(lastRunKey, ranAt)
	return nil
}

func markProactiveRun(lastRunKey string, at time.Time) {
	if strings.TrimSpace(lastRunKey) == "" {
		return
	}
	at = at.Local()
	UpdateState(func(s *State) {
		if s.ProactiveLastRun == nil {
			s.ProactiveLastRun = make(map[string]time.Time)
		}
		s.ProactiveLastRun[lastRunKey] = at
	})
}

func proactiveHistorySuffix(p *Prompt) string {
	if p != nil && strings.TrimSpace(p.Key) != "" {
		return p.Key
	}
	if p != nil {
		return normalizeKey(p.Name)
	}
	return ""
}

func proactiveRunKey(p *Prompt) string {
	if p != nil && strings.TrimSpace(p.Key) != "" {
		return p.Key
	}
	return "prompt"
}

func lifebaseDayStart(t time.Time) time.Time {
	t = t.Local()
	b := time.Date(t.Year(), t.Month(), t.Day(), config.DayBoundaryHour, 0, 0, 0, t.Location())
	if t.Before(b) {
		b = b.AddDate(0, 0, -1)
	}
	return b
}

func scheduledTarget(dayStart time.Time, hm HourMinute) time.Time {
	boundaryMinutes := config.DayBoundaryHour * 60
	atMinutes := hm.Hour*60 + hm.Minute
	delta := atMinutes - boundaryMinutes
	if delta < 0 {
		delta += 24 * 60
	}
	return dayStart.Add(time.Duration(delta) * time.Minute)
}

// shouldTriggerProactive determines if the proactive prompt should run now,
// based on schedule slots, last run timestamp, and current time. It returns
// the target timestamp of the slot being satisfied (latest due slot).
//
// Rules:
//   - A slot is due within [target, target+60m).
//   - If multiple slots are due (daemon was down), only the latest due slot is considered.
//   - A slot is considered satisfied if lastRun >= target.
//   - Additionally, if lastRun happened within the hour before target and there
//     are no other scheduled slots in [target-60m, target), the slot is suppressed.
func shouldTriggerProactive(schedule []HourMinute, lastRun time.Time, now time.Time) (target time.Time, ok bool) {
	if len(schedule) == 0 {
		return time.Time{}, false
	}
	now = now.Local()
	dayStart := lifebaseDayStart(now)

	var bestTarget time.Time
	for _, hm := range schedule {
		tgt := scheduledTarget(dayStart, hm)
		if now.Before(tgt) || !now.Before(tgt.Add(60*time.Minute)) {
			continue
		}
		if bestTarget.IsZero() || tgt.After(bestTarget) {
			bestTarget = tgt
		}
	}
	if bestTarget.IsZero() {
		return time.Time{}, false
	}

	if lastRun.IsZero() {
		return bestTarget, true
	}

	// Already ran at/after this slot.
	if !lastRun.Before(bestTarget) {
		return time.Time{}, false
	}

	// If it ran within the hour before the slot, suppress the slot unless
	// there were other scheduled slots within that hour window.
	windowStart := bestTarget.Add(-60 * time.Minute)
	if !lastRun.Before(windowStart) {
		for _, hm := range schedule {
			tgt := scheduledTarget(dayStart, hm)
			if !tgt.Before(windowStart) && tgt.Before(bestTarget) {
				return bestTarget, true
			}
		}
		return time.Time{}, false
	}

	return bestTarget, true
}

// proactiveRunner ticks hourly and triggers check-ins at the specified hour.
func proactiveRunner(ctx context.Context) {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			now = now.Local()

			prompts, err := parsePrompts()
			if err != nil {
				log.Printf("Proactive: prompt parse warning: %v", err)
			}
			for _, p := range prompts {
				if p == nil || len(p.Schedule) == 0 {
					continue
				}
				runKey := proactiveRunKey(p)

				var last time.Time
				ReadState(func(s *State) {
					if s.ProactiveLastRun != nil {
						last = s.ProactiveLastRun[runKey]
					}
				})
				if _, ok := shouldTriggerProactive(p.Schedule, last, now); !ok {
					continue
				}
				if err := runProactivePrompt(ctx, p, runKey); err != nil {
					log.Printf("Proactive %q error: %v", p.Name, err)
					_ = sendTelegramText(ctx, fmt.Sprintf("Proactive %q failed: %v", p.Name, err))
				}
			}
		}
	}
}

func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, filepath.Ext(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

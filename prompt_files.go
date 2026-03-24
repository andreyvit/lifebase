package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/andreyvit/jsonfix"
	"github.com/andreyvit/naml"
)

type HourMinute struct {
	Hour   int
	Minute int
}

func parseHourMinute(s string) (HourMinute, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return HourMinute{}, fmt.Errorf("empty time")
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return HourMinute{}, fmt.Errorf("invalid time %q (want H:MM)", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return HourMinute{}, fmt.Errorf("invalid hour in %q", s)
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return HourMinute{}, fmt.Errorf("invalid minute in %q", s)
	}
	if h < 0 || h > 23 {
		return HourMinute{}, fmt.Errorf("hour out of range in %q", s)
	}
	if m < 0 || m > 59 {
		return HourMinute{}, fmt.Errorf("minute out of range in %q", s)
	}
	return HourMinute{Hour: h, Minute: m}, nil
}

type Prompt struct {
	FileName string
	Name     string
	Key      string
	Schedule []HourMinute
	Body     string
}

type promptFrontmatter struct {
	Schedule any `json:"schedule"`
}

func parsePromptMarkdown(raw string) (schedule []HourMinute, body string, _ error) {
	fmText, bodyText, hasFM, err := splitMarkdownFrontmatter(raw)
	if err != nil {
		return nil, "", err
	}

	var fm promptFrontmatter
	if hasFM && strings.TrimSpace(fmText) != "" {
		if err := decodePromptFrontmatter([]byte(fmText), &fm); err != nil {
			return nil, "", err
		}
	}

	schedule, err = parseScheduleField(fm.Schedule)
	if err != nil {
		return nil, "", err
	}

	return schedule, strings.TrimSpace(bodyText), nil
}

func parsePrompts() ([]*Prompt, error) {
	if strings.TrimSpace(promptsDir) == "" {
		return nil, fmt.Errorf("promptsDir is not set")
	}

	var (
		prompts []*Prompt
		errs    []error
	)
	seenKeys := make(map[string]string) // key -> rel file
	walkErr := filepath.WalkDir(promptsDir, func(fullPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if strings.ToLower(filepath.Ext(d.Name())) != ".md" {
			return nil
		}

		nameNoExt := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if strings.HasPrefix(strings.ToLower(nameNoExt), "system") {
			return nil
		}

		key := normalizeKey(nameNoExt)
		if key == "" {
			errs = append(errs, fmt.Errorf("invalid prompt name %q", nameNoExt))
			return nil
		}

		rel, relErr := filepath.Rel(promptsDir, fullPath)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if prev, ok := seenKeys[key]; ok {
			errs = append(errs, fmt.Errorf("duplicate prompt key %q: %s and %s", key, prev, rel))
			return nil
		}
		seenKeys[key] = rel

		b, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", rel, readErr))
			return nil
		}

		schedule, body, parseErr := parsePromptMarkdown(string(b))
		if parseErr != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", rel, parseErr))
			return nil
		}

		prompts = append(prompts, &Prompt{
			FileName: rel,
			Name:     nameNoExt,
			Key:      key,
			Schedule: schedule,
			Body:     body,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(prompts, func(i, j int) bool { return prompts[i].FileName < prompts[j].FileName })
	return prompts, errors.Join(errs...)
}

func splitMarkdownFrontmatter(s string) (frontmatter string, body string, hasFrontmatter bool, err error) {
	if strings.HasPrefix(s, "\ufeff") {
		s = strings.TrimPrefix(s, "\ufeff")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")

	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return "", "", false, nil
	}
	if strings.TrimSpace(lines[0]) != "---" {
		return "", s, false, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", true, fmt.Errorf("frontmatter starts with --- but no closing --- found")
	}

	frontmatter = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return frontmatter, body, true, nil
}

func decodePromptFrontmatter(raw []byte, out any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}

	if converted, err := naml.Convert(raw); err == nil {
		raw = converted
	} else if !looksLikeJSON(raw) {
		return fmt.Errorf("naml convert: %w", err)
	}

	raw = jsonfix.Bytes(raw)

	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing content")
		}
		return err
	}
	return nil
}

func looksLikeJSON(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
}

func parseScheduleField(v any) ([]HourMinute, error) {
	if v == nil {
		return nil, nil
	}

	var times []string
	switch vv := v.(type) {
	case string:
		if strings.TrimSpace(vv) != "" {
			times = append(times, vv)
		}
	case []any:
		for _, it := range vv {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("schedule contains non-string value")
			}
			if strings.TrimSpace(s) != "" {
				times = append(times, s)
			}
		}
	default:
		return nil, fmt.Errorf("schedule must be a string or a list of strings")
	}

	out := make([]HourMinute, 0, len(times))
	seen := make(map[HourMinute]bool, len(times))
	for _, s := range times {
		hm, err := parseHourMinute(s)
		if err != nil {
			return nil, err
		}
		if seen[hm] {
			continue
		}
		seen[hm] = true
		out = append(out, hm)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hour != out[j].Hour {
			return out[i].Hour < out[j].Hour
		}
		return out[i].Minute < out[j].Minute
	})
	return out, nil
}

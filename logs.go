package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LogSpec describes a simple timestamped Markdown list log under Logs/.
type LogSpec struct {
	FileBasename   string // file base, e.g. "SexLog" -> Logs/SexLog-YYYY-MM.md
	DirRelPath     string // slash-separated dir relative to rootDir, "." for root
	PrefaceRelPath string // optional slash-separated path to FooLog.md
}

var logFileNamePattern = regexp.MustCompile(`^([A-Za-z0-9_]+Log)(?:-(\d{4})-(\d{2}))?\.md$`)

const (
	logEntriesBeforeInput = 5
	logEntriesAfterWrite  = 2
)

// command returns the Telegram slash command name for the log, e.g. "sexlog".
func (ls LogSpec) command() string {
	// Prefer FileBasename to ensure stability and uniqueness
	return strings.ToLower(ls.FileBasename)
}

func (ls LogSpec) monthlyRelPath(t time.Time) string {
	t = t.Local()
	name := fmt.Sprintf("%s-%04d-%02d.md", ls.FileBasename, t.Year(), int(t.Month()))
	dir := strings.TrimSpace(ls.DirRelPath)
	if dir == "" || dir == "." {
		return name
	}
	return path.Join(dir, name)
}

func (ls LogSpec) prefacePath() string {
	p := strings.TrimSpace(ls.PrefaceRelPath)
	if p == "" {
		return ""
	}
	return filepath.Join(rootDir, filepath.FromSlash(p))
}

// logFilePath returns <Dir>/<Base>-YYYY-MM.md for local time month.
func (ls LogSpec) logFilePath(t time.Time) string {
	return filepath.Join(rootDir, filepath.FromSlash(ls.monthlyRelPath(t)))
}

type logCandidate struct {
	FileBasename string
	DirRelPath   string
	RelPath      string
	HasPreface   bool
}

func discoverLogSpecs() ([]LogSpec, error) {
	var candidates []logCandidate
	if err := walkVisibleFiles(maxDiscoveryDepth, func(relPath string, entry fs.DirEntry) error {
		base := path.Base(relPath)
		m := logFileNamePattern.FindStringSubmatch(base)
		if m == nil {
			return nil
		}
		dir := path.Dir(relPath)
		if dir == "" {
			dir = "."
		}
		candidates = append(candidates, logCandidate{
			FileBasename: m[1],
			DirRelPath:   dir,
			RelPath:      relPath,
			HasPreface:   m[2] == "",
		})
		return nil
	}); err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RelPath < candidates[j].RelPath })

	byBase := make(map[string]*LogSpec)
	conflictLogged := make(map[string]bool)
	for _, c := range candidates {
		spec, ok := byBase[c.FileBasename]
		if !ok {
			spec = &LogSpec{
				FileBasename: c.FileBasename,
				DirRelPath:   c.DirRelPath,
			}
			byBase[c.FileBasename] = spec
		} else if spec.DirRelPath != c.DirRelPath {
			if !conflictLogged[c.FileBasename] {
				log.Printf("Log discovery: %s found in multiple directories (%s, %s); using %s", c.FileBasename, spec.DirRelPath, c.DirRelPath, spec.DirRelPath)
				conflictLogged[c.FileBasename] = true
			}
			continue
		}
		if c.HasPreface {
			spec.PrefaceRelPath = c.RelPath
		}
	}

	specs := make([]LogSpec, 0, len(byBase))
	for _, spec := range byBase {
		specs = append(specs, *spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].FileBasename < specs[j].FileBasename })
	return specs, nil
}

func findLogSpec(fileBasename string) (*LogSpec, error) {
	specs, err := discoverLogSpecs()
	if err != nil {
		return nil, err
	}
	for i := range specs {
		if strings.EqualFold(specs[i].FileBasename, fileBasename) {
			return &specs[i], nil
		}
	}
	return nil, nil
}

// addLogEntry appends a single list item line to the month file.
func addLogEntry(ctx context.Context, spec LogSpec, when time.Time, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty entry")
	}
	when = when.Local()
	// Example: - 2025-10-05 23:48 Sun - text
	ts := when.Format("2006-01-02 15:04 Mon")
	line := fmt.Sprintf("- %s - %s\n", ts, text)

	fn := spec.logFilePath(when)
	if err := os.MkdirAll(filepath.Dir(fn), 0o777); err != nil {
		return "", err
	}
	f, err := os.OpenFile(fn, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return "", err
	}
	return fn, nil
}

type monthLogChunk struct {
	monthStart time.Time
	lines      []string
}

func readLogFileNonEmptyLines(fn string) ([]string, error) {
	f, err := os.Open(fn)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		ln := strings.TrimRight(s.Text(), " \t")
		if ln == "" {
			continue
		}
		lines = append(lines, ln)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// readLastLogEntries returns up to n most recent lines across month log files.
func readLastLogEntries(spec LogSpec, now time.Time, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	now = now.Local()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	const maxLookbackMonths = 36
	var chunks []monthLogChunk
	var total int
	for i := 0; i < maxLookbackMonths; i++ {
		fn := spec.logFilePath(monthStart)
		lines, err := readLogFileNonEmptyLines(fn)
		if err != nil {
			return nil, err
		}
		if len(lines) > 0 {
			chunks = append(chunks, monthLogChunk{monthStart: monthStart, lines: lines})
			total += len(lines)
			if total >= n {
				break
			}
		}
		monthStart = monthStart.AddDate(0, -1, 0)
	}

	var lines []string
	for i := len(chunks) - 1; i >= 0; i-- {
		lines = append(lines, chunks[i].lines...)
	}
	// Take last n
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// renderLastLogEntries composes a reply message with last N lines.
func renderLastLogEntries(spec LogSpec, now time.Time, n int) (string, error) {
	lines, err := readLastLogEntries(spec, now, n)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — last %d:\n\n", spec.FileBasename, n)
	if len(lines) == 0 {
		b.WriteString("(no entries yet)\n")
		return b.String(), nil
	}
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

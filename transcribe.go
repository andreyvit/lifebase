package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Observed server limit error: "audio duration ... is longer than 1400 seconds"
	maxTranscribeSeconds  = 1400
	defaultSegmentSeconds = 1380 // 23 minutes, just under the limit
)

func transcribe(ctx context.Context, fn string) (string, error) {
	if secrets.OpenAIKey == "" {
		return "", fmt.Errorf("OpenAI key not set")
	}

	// Some formats (e.g., Telegram .oga/.ogg Opus) may not be supported directly.
	// Convert to a widely supported format when needed.
	prepFn, cleanup, err := prepareAudioForTranscription(ctx, fn)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}

	fields := map[string]string{
		"model":             "gpt-4o-transcribe",
		"response_format":   "text",
		"temperature":       "0",
		"chunking_strategy": "auto",
		"language":          "en",
	}
	dur, derr := probeAudioDurationSeconds(ctx, prepFn)
	if derr != nil {
		return "", derr
	}

	var segs []string
	var cleanupSegs func()
	if dur > float64(maxTranscribeSeconds-1) {
		segs, cleanupSegs, err = splitAudioIntoSegments(ctx, prepFn, defaultSegmentSeconds)
		if err != nil {
			return "", fmt.Errorf("chunking failed: %w", err)
		}
	} else {
		segs = []string{prepFn}
	}

	if cleanupSegs != nil {
		defer cleanupSegs()
	}

	var out strings.Builder
	for i, seg := range segs {
		if len(segs) > 1 {
			log.Printf("OpenAI (segment %d/%d)...", i+1, len(segs))
		} else {
			log.Printf("OpenAI...")
		}
		var segText string
		err = retry(func() error {
			fh := must(os.Open(seg))
			defer fh.Close()
			var e error
			segText, e = postMultipart(ctx, "https://api.openai.com/v1/audio/transcriptions", secrets.OpenAIKey, fields, "file", filepath.Base(seg), fh)
			return e
		})
		if err != nil {
			return "", fmt.Errorf("segment %d: %w", i+1, err)
		}
		segText = strings.TrimSpace(segText)
		if segText != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString(segText)
		}
	}
	return out.String(), nil
}

// prepareAudioForTranscription converts unsupported formats (e.g. .oga/.ogg/.opus)
// into a supported one using ffmpeg, returning the path to a temporary file.
// If no conversion is necessary, returns the original path and nil cleanup.
func prepareAudioForTranscription(ctx context.Context, fn string) (string, func(), error) {
	ext := strings.ToLower(filepath.Ext(fn))
	switch ext {
	case ".mp3", ".mp4", ".mpeg", ".mpga", ".m4a", ".wav", ".webm":
		return fn, nil, nil
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", nil, fmt.Errorf("audio format %s unsupported by OpenAI; please install ffmpeg to enable conversion (e.g. brew install ffmpeg)", ext)
	}

	// Convert to MP3 for size/speed.
	// mp3: ffmpeg -nostdin -hide_banner -loglevel error -y -i <in> -vn -ac 1 -ar 16000 -c:a libmp3lame -q:a 2 <out.mp3>
	mp3File, err := os.CreateTemp("", "lifebase-audio-*.mp3")
	if err != nil {
		return "", nil, err
	}
	mp3Path := mp3File.Name()
	mp3File.Close()
	log.Printf("ffmpeg...")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", fn, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-q:a", "2", mp3Path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(mp3Path)
		return "", nil, fmt.Errorf("ffmpeg mp3 convert failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { _ = os.Remove(mp3Path) }
	return mp3Path, cleanup, nil
}

// probeAudioDurationSeconds returns the duration (seconds) via ffprobe.
func probeAudioDurationSeconds(ctx context.Context, fn string) (float64, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return 0, fmt.Errorf("ffprobe not found")
	}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", fn)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return f, nil
}

// splitAudioIntoSegments splits src into roughly segSeconds MP3 chunks using ffmpeg.
func splitAudioIntoSegments(ctx context.Context, src string, segSeconds int) ([]string, func(), error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, nil, fmt.Errorf("ffmpeg not found")
	}
	dir, err := os.MkdirTemp("", "lifebase-chunks-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	pattern := filepath.Join(dir, "part-%03d.mp3")

	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		// normalize for consistency and ASR quality
		"-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-q:a", "2",
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%d", segSeconds),
		"-reset_timestamps", "1",
		pattern,
	}
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("ffmpeg segment failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []string
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if strings.HasSuffix(strings.ToLower(name), ".mp3") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	if len(out) == 0 {
		cleanup()
		return nil, nil, fmt.Errorf("no segments produced")
	}
	return out, cleanup, nil
}

func postMultipart(ctx context.Context, url string, bearerToken string, fields map[string]string, fileField, fileName string, file io.Reader) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// text fields
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}

	// file field
	fw, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, file); err != nil {
		return "", err
	}
	ensure(w.Close())

	req := must(http.NewRequestWithContext(ctx, http.MethodPost, url, &body))
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	cli := &http.Client{Timeout: 5 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	respBody, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, string(respBody))
	}
	return string(respBody), nil
}

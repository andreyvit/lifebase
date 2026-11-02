package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// Codex exec emits JSONL snapshots. Only assistant messages belong in previews,
// never reasoning, tool output, or the surrounding event envelope.
type codexMessageStream struct {
	pending   bytes.Buffer
	onMessage func(string)
}

func (s *codexMessageStream) Write(p []byte) (int, error) {
	s.pending.Write(p)
	for {
		b := s.pending.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		s.emit(b[:i])
		s.pending.Next(i + 1)
	}
	return len(p), nil
}

func (s *codexMessageStream) flush() {
	if s.pending.Len() > 0 {
		s.emit(s.pending.Bytes())
		s.pending.Reset()
	}
}

func (s *codexMessageStream) emit(line []byte) {
	var e struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Channel string `json:"channel"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	if e.Type != "item.started" && e.Type != "item.updated" && e.Type != "item.completed" {
		return
	}
	if e.Item.Type != "agent_message" || e.Item.Channel == "analysis" || e.Item.Text == "" {
		return
	}
	text := strings.TrimSpace(e.Item.Text)
	// Keep the useful previous preview while an initial fragment is incomplete.
	if e.Type != "item.completed" && len([]rune(text)) < 8 {
		return
	}
	if strings.Contains(text, "<<<LIFEBASE:") || text == "SKIP" {
		text = ""
	}
	s.onMessage(text)
}

const telegramThinkingDraft = "<tg-thinking>Thinking…</tg-thinking>"
const telegramDraftInterval = 250 * time.Millisecond

type telegramDraft struct {
	mu     sync.Mutex
	latest string
	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func startTelegramDraft(ctx context.Context) *telegramDraft {
	ctx, cancel := context.WithCancel(ctx)
	d := &telegramDraft{wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	id := time.Now().UnixNano()
	var previous string
	go d.run(ctx, func(ctx context.Context, text string) error {
		// These are complete snapshots, not token deltas. A fresh ID replaces
		// them immediately instead of animating unrelated messages letter by letter.
		if text != previous {
			id++
			previous = text
		}
		return telegramPostMessage(ctx, secrets.TelegramBotToken, "sendRichMessageDraft", map[string]any{
			"chat_id":      secrets.TelegramChatID,
			"draft_id":     id,
			"rich_message": map[string]any{"markdown": text},
		})
	})
	d.update(telegramThinkingDraft)
	return d
}

func (d *telegramDraft) update(text string) {
	// Keep within Telegram's rich-message limit even for an oversized response.
	runes := []rune(text)
	if len(runes) > 32000 {
		text = string(runes[:32000]) + "…"
	}
	d.mu.Lock()
	d.latest = text
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *telegramDraft) stop() {
	d.cancel()
	<-d.done // No draft request may overtake the final persistent message.
}

func (d *telegramDraft) run(ctx context.Context, send func(context.Context, string) error) {
	defer close(d.done)
	ticker := time.NewTicker(telegramDraftInterval)
	defer ticker.Stop()
	var last string
	var next, refresh time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		d.mu.Lock()
		text := d.latest
		d.mu.Unlock()
		now := time.Now()
		if text == "" || now.Before(next) || (text == last && now.Before(refresh)) {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := send(requestCtx, text)
		cancel()
		now = time.Now()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Telegram draft skipped: %v", err)
			next = now.Add(30 * time.Second)
			continue
		}
		last = text
		next = now.Add(telegramDraftInterval)
		refresh = now.Add(20 * time.Second) // Draft previews expire after 30 seconds.
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexMessageStream(t *testing.T) {
	var got []string
	s := &codexMessageStream{onMessage: func(text string) { got = append(got, text) }}
	input := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"reasoning","text":"private"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","text":"secret"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","channel":"analysis","text":"private"}}`,
		`not json`,
		`{"type":"item.started","item":{"type":"agent_message","text":"Hello"}}`,
		`{"type":"item.updated","item":{"type":"agent_message","text":"Hello 🍵"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Latest message"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"<<<LIFEBASE:RESTART>>>"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"SKIP"}}`,
	}, "\n")
	// Split even UTF-8 codepoints and JSON tokens across writes.
	for _, b := range []byte(input) {
		s.Write([]byte{b})
	}
	s.flush()
	want := []string{"Hello", "Hello 🍵", "Latest message", "", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCodexCommandPreviewsBeforeExit(t *testing.T) {
	old := rootDir
	rootDir = t.TempDir()
	t.Cleanup(func() { rootDir = old })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got []string
	script := `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"Working"}}'
while [ ! -e ack ]; do sleep 0.01; done
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"Done"}}'`
	_, out, err := runAgentCommand(ctx, agentSpec{Kind: agentCodex, Bin: "sh"}, []string{"-c", script}, func(text string) {
		got = append(got, text)
		if err := os.WriteFile(filepath.Join(rootDir, "ack"), nil, 0600); err != nil {
			t.Error(err)
		}
	})
	if err != nil || out != "Done" || !reflect.DeepEqual(got, []string{"Working", "Done"}) {
		t.Fatalf("output %q, previews %q, error %v", out, got, err)
	}
}

func TestTelegramDraftCoalescesAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &telegramDraft{wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	sent := make(chan string, 10)
	first := make(chan struct{})
	go d.run(ctx, func(ctx context.Context, text string) error {
		sent <- text
		if text == "First" {
			select {
			case <-first:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		<-ctx.Done()
		return ctx.Err()
	})
	defer d.stop()
	wait := func(want string) {
		t.Helper()
		select {
		case got := <-sent:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("no draft")
		}
	}
	d.update("First")
	wait("First")
	for i := 0; i < 100; i++ {
		d.update("Superseded")
	}
	d.update("Latest")
	close(first)
	wait("Latest")
	cancel()
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("in-flight draft did not cancel")
	}
	select {
	case text := <-sent:
		t.Fatalf("unexpected draft %q", text)
	default:
	}
}

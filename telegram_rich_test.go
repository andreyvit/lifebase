package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramRichDelivery(t *testing.T) {
	oldLogOutput := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(oldLogOutput) })
	for _, tc := range []struct {
		name      string
		text      string
		status    int
		wantPlain bool
		wantError bool
	}{
		{"table and Unicode stay together", "**Breakfast 🍵**\n\n| Food | kcal |\n|---|---:|\n| Овсянка | 228 |\n\n" + strings.Repeat("я", 4000), 200, false, false},
		{"rejected formatting falls back", "**Breakfast**", 400, true, false},
		{"forbidden does not fall back", "Breakfast", 403, false, true},
		{"oversized document uses plain chunks", strings.Repeat("я", 32769), 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var methods, plain []string
			var markups []bool
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				method := strings.TrimPrefix(r.URL.Path, "/bottest/")
				methods = append(methods, method)
				markups = append(markups, payload["reply_markup"] != nil)
				if method == "sendRichMessage" {
					var rich struct {
						Markdown string `json:"markdown"`
					}
					if err := json.Unmarshal(payload["rich_message"], &rich); err != nil {
						t.Error(err)
					}
					if rich.Markdown != tc.text {
						t.Error("rich Markdown changed or split")
					}
					if payload["text"] != nil || payload["parse_mode"] != nil {
						t.Error("legacy formatting fields in rich request")
					}
					w.WriteHeader(tc.status)
				} else if method == "sendMessage" {
					var text string
					if err := json.Unmarshal(payload["text"], &text); err != nil {
						t.Error(err)
					}
					if len([]rune(text)) > 3500 {
						t.Error("plain chunk too long")
					}
					plain = append(plain, text)
				} else {
					t.Errorf("unexpected method %s", method)
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
			}))
			defer srv.Close()
			oldTransport, oldSecrets := sharedHTTPTransport, secrets
			transport := srv.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "example.com"
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			}
			sharedHTTPTransport = transport
			secrets.TelegramBotToken, secrets.TelegramChatID = "test", "123"
			t.Cleanup(func() { sharedHTTPTransport, secrets = oldTransport, oldSecrets; transport.CloseIdleConnections() })
			err := sendTelegramTextRaw(context.Background(), tc.text, tgReplyKeyboardRemove{RemoveKeyboard: true})
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if tc.wantPlain {
				if strings.Join(plain, "") != tc.text {
					t.Error("plain fallback lost content")
				}
				for i, method := range methods {
					if method == "sendMessage" && markups[i] != (i == len(methods)-1) {
						t.Error("keyboard not on final plain chunk only")
					}
				}
			} else if len(methods) != 1 || methods[0] != "sendRichMessage" || !markups[0] {
				t.Errorf("requests = %v, markups = %v", methods, markups)
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func logPromptOutputTag(fileBasename string) string {
	fileBasename = strings.TrimSpace(fileBasename)
	if fileBasename == "" {
		return ""
	}
	return fileBasename + "/prompt"
}

func logWriteOutputTag(fileBasename string) string {
	fileBasename = strings.TrimSpace(fileBasename)
	if fileBasename == "" {
		return ""
	}
	return fileBasename + "/write"
}

func sendTelegramText(ctx context.Context, text string) error {
	return sendTelegramTextTagged(ctx, "ai", text)
}

func sendTelegramTextTagged(ctx context.Context, outputTag, text string) error {
	if err := sendTelegramTextRaw(ctx, text); err != nil {
		return err
	}
	outputTag = strings.TrimSpace(outputTag)
	if outputTag != "" {
		UpdateState(func(st *State) { st.LastTGOutput = outputTag })
	}
	return nil
}

func sendTelegramTextRaw(ctx context.Context, text string) error {
	log.Printf("Sending Telegram message:\n%s\n\n", text)

	if strings.TrimSpace(secrets.TelegramBotToken) == "" || strings.TrimSpace(secrets.TelegramChatID) == "" {
		return fmt.Errorf("telegram not configured: set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID in secrets file")
	}

	chunks := splitTelegramChunks(text, 3500)
	for i, ch := range chunks {
		if err := retry(func() error { return telegramSendMessage(ctx, secrets.TelegramBotToken, secrets.TelegramChatID, ch) }); err != nil {
			if len(chunks) > 1 {
				return fmt.Errorf("sending chunk %d/%d: %w", i+1, len(chunks), err)
			}
			return err
		}
	}
	return nil
}

func sendTelegramChatAction(ctx context.Context, action string) error {
	if strings.TrimSpace(secrets.TelegramBotToken) == "" || strings.TrimSpace(secrets.TelegramChatID) == "" {
		return fmt.Errorf("telegram not configured: set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID in secrets file")
	}
	return telegramSendChatAction(ctx, secrets.TelegramBotToken, secrets.TelegramChatID, action)
}

func telegramSendMessage(ctx context.Context, botToken, chatID, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 2 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := string(respBody)
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return fmt.Errorf("telegram API HTTP %d: %s", res.StatusCode, strings.TrimSpace(msg))
	}
	return nil
}

func telegramSendChatAction(ctx context.Context, botToken, chatID, action string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", botToken)
	payload := map[string]any{
		"chat_id": chatID,
		"action":  action,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 30 * time.Second}
	res, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := string(respBody)
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return fmt.Errorf("telegram API HTTP %d: %s", res.StatusCode, strings.TrimSpace(msg))
	}
	return nil
}

// pollTelegram long-polls Telegram getUpdates for new messages and ingests them.
func pollTelegram(ctx context.Context, tasks chan<- ingestTask) {
	if strings.TrimSpace(secrets.TelegramBotToken) == "" || strings.TrimSpace(secrets.TelegramChatID) == "" {
		log.Printf("Telegram not configured; skipping Telegram polling")
		// Sleep loop to avoid tight error loop; keep daemon alive
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}

	// Security: restrict intake to a specific numeric chat ID
	allowedChatID, err := strconv.ParseInt(strings.TrimSpace(secrets.TelegramChatID), 10, 64)
	if err != nil {
		log.Printf("Telegram: TELEGRAM_CHAT_ID is not a numeric user ID; for security, ignoring all inbound messages. Configure your numeric user ID to enable intake.")
		allowedChatID = 0
	}

	base := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", secrets.TelegramBotToken)
	cli := &http.Client{Timeout: 70 * time.Second}
	var offset int64 = 0
	mediaGroups := map[string]*pendingTelegramMediaGroup{}
	log.Printf("Polling Telegram updates…")

	// Ensure bot commands are set (pause/resume and proactive items)
	if err := updateTelegramCommands(ctx); err != nil {
		log.Printf("Telegram: setMyCommands failed: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := processTelegramImageBatches(ctx, tasks, takeReadyTelegramMediaGroups(mediaGroups, time.Now(), false)); err != nil {
			log.Printf("Telegram: media group flush failed: %v", err)
		}
		q := url.Values{}
		q.Set("timeout", strconv.Itoa(telegramPollTimeoutSeconds(mediaGroups, time.Now())))
		if offset > 0 {
			q.Set("offset", strconv.FormatInt(offset, 10))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			log.Printf("Telegram: build request error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		res, err := cli.Do(req)
		if err != nil {
			log.Printf("Telegram: request error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		var upd tgUpdates
		if err := json.NewDecoder(res.Body).Decode(&upd); err != nil {
			res.Body.Close()
			log.Printf("Telegram: decode error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		res.Body.Close()
		if !upd.Ok {
			log.Printf("Telegram: API returned not ok")
			time.Sleep(5 * time.Second)
			continue
		}
		for _, u := range upd.Result {
			if u.UpdateID >= int(offset) {
				offset = int64(u.UpdateID) + 1
			}
			if u.Message == nil {
				continue
			}
			msg := u.Message
			if allowedChatID == 0 || msg.Chat.ID != allowedChatID {
				// Ignore messages from other chats/users (or all, if not configured correctly)
				continue
			}
			imageBatch, err := telegramSaveImageBatch(ctx, msg)
			if err != nil {
				log.Printf("Telegram: image intake failed: %v", err)
				_ = sendTelegramText(ctx, fmt.Sprintf("Telegram image intake failed: %v", err))
				continue
			}
			if imageBatch != nil {
				if imageBatch.MediaGroupID != "" {
					if err := processTelegramImageBatches(ctx, tasks, takeOtherTelegramMediaGroups(mediaGroups, imageBatch.MediaGroupID)); err != nil {
						log.Printf("Telegram: media group flush failed: %v", err)
					}
					addTelegramMediaGroupBatch(mediaGroups, *imageBatch)
				} else {
					if err := processTelegramImageBatches(ctx, tasks, takeAllTelegramMediaGroups(mediaGroups)); err != nil {
						log.Printf("Telegram: media group flush failed: %v", err)
					}
					if err := processTelegramImageBatch(ctx, tasks, *imageBatch); err != nil {
						log.Printf("Telegram: image batch failed: %v", err)
						_ = sendTelegramText(ctx, fmt.Sprintf("Telegram image batch failed: %v", err))
					}
				}
				continue
			}
			if err := processTelegramImageBatches(ctx, tasks, takeAllTelegramMediaGroups(mediaGroups)); err != nil {
				log.Printf("Telegram: media group flush failed: %v", err)
			}
			// Audio messages
			if msg.Voice != nil {
				filePath, err := telegramGetFilePath(ctx, secrets.TelegramBotToken, msg.Voice.FileID)
				if err != nil {
					log.Printf("Telegram: getFile failed: %v", err)
					_ = sendTelegramText(ctx, fmt.Sprintf("Telegram getFile failed: %v", err))
					continue
				}
				tmp, err := telegramDownloadToTemp(ctx, secrets.TelegramBotToken, filePath)
				if err != nil {
					log.Printf("Telegram: download failed: %v", err)
					_ = sendTelegramText(ctx, fmt.Sprintf("Telegram download failed: %v", err))
					continue
				}
				// Update last incoming time using Telegram message date
				updateLastIncoming(time.Unix(msg.Date, 0))
				log.Printf("New Telegram voice -> %s", filepath.Base(tmp))
				task := ingestTask{typ: taskAudioFile, path: tmp, displayName: "voice message", deleteAfter: true}
				if imagePaths := takePendingTelegramImagePaths(); len(imagePaths) > 0 {
					task.typ = taskTelegramAudio
					task.imagePaths = imagePaths
				}
				tasks <- task
				continue
			}
			if msg.Audio != nil {
				filePath, err := telegramGetFilePath(ctx, secrets.TelegramBotToken, msg.Audio.FileID)
				if err != nil {
					log.Printf("Telegram: getFile failed: %v", err)
					_ = sendTelegramText(ctx, fmt.Sprintf("Telegram getFile failed: %v", err))
					continue
				}
				tmp, err := telegramDownloadToTemp(ctx, secrets.TelegramBotToken, filePath)
				if err != nil {
					log.Printf("Telegram: download failed: %v", err)
					_ = sendTelegramText(ctx, fmt.Sprintf("Telegram download failed: %v", err))
					continue
				}
				disp := msg.Audio.FileName
				if disp == "" {
					disp = "audio" + filepath.Ext(tmp)
				}
				// Update last incoming time using Telegram message date
				updateLastIncoming(time.Unix(msg.Date, 0))
				log.Printf("New Telegram audio -> %s", filepath.Base(tmp))
				task := ingestTask{typ: taskAudioFile, path: tmp, displayName: disp, deleteAfter: true}
				if imagePaths := takePendingTelegramImagePaths(); len(imagePaths) > 0 {
					task.typ = taskTelegramAudio
					task.imagePaths = imagePaths
				}
				tasks <- task
				continue
			}
			// Text messages
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}

			// Handle slash commands
			if strings.HasPrefix(text, "/") {
				// Use Telegram-provided message time
				msgTime := time.Unix(msg.Date, 0).Local()
				if handled := handleTelegramCommand(ctx, text, msgTime, msg.Chat.ID); handled {
					continue
				}
			}

			// If there is a pending log input, consume this message as its text
			var pending *PendingLogInput
			ReadState(func(st *State) {
				if st.PendingLog != nil && time.Now().Before(st.PendingLog.ExpiresAt) {
					pending = st.PendingLog
				}
			})
			if pending != nil && pending.ChatID == msg.Chat.ID {
				spec, err := findLogSpec(pending.FileBasename)
				if err != nil {
					log.Printf("Telegram: log discovery failed: %v", err)
					spec = nil
				}
				if spec != nil {
					tm := time.Unix(msg.Date, 0).Local()
					updateLastIncoming(tm)
					if _, err := addLogEntry(ctx, *spec, tm, text); err != nil {
						_ = sendTelegramText(ctx, fmt.Sprintf("Failed to add to %s: %v", pending.Title, err))
						// keep pending to allow retry
					} else {
						// clear pending
						UpdateState(func(st *State) { st.PendingLog = nil })
						if out, err := renderLastLogEntries(*spec, tm, logEntriesAfterWrite); err == nil {
							_ = sendTelegramTextTagged(ctx, logWriteOutputTag(spec.FileBasename), out)
						} else {
							_ = sendTelegramTextTagged(ctx, logWriteOutputTag(spec.FileBasename), fmt.Sprintf("Added to %s, but failed to read recent entries: %v", pending.Title, err))
						}
					}
					continue
				}
			}
			// Write to RawInputs as a timestamped .md and queue for ingestion
			tm := time.Unix(msg.Date, 0).Local()
			updateLastIncoming(tm)
			fn := uniqueRawInputPath(tm, "tg")
			if err := os.WriteFile(fn, []byte(text+"\n"), 0666); err != nil {
				log.Printf("Telegram: write rawinput failed: %v", err)
				_ = sendTelegramText(ctx, fmt.Sprintf("Telegram ingest failed writing raw input: %v", err))
				continue
			}
			task := ingestTask{typ: taskTelegramText, path: fn, displayName: filepath.Base(fn)}
			if imagePaths := takePendingTelegramImagePaths(); len(imagePaths) > 0 {
				task.imagePaths = imagePaths
				log.Printf("New Telegram message with %d queued images -> %s", len(imagePaths), filepath.Base(fn))
			} else {
				log.Printf("New Telegram message -> %s", filepath.Base(fn))
			}
			tasks <- task
		}
	}
}

// handleTelegramCommand processes command messages like /pause, /resume, or
// proactive triggers (e.g., /morning). Returns true if handled.
func handleTelegramCommand(ctx context.Context, msg string, msgTime time.Time, chatID int64) bool {
	// Parse command preserving the rest (arguments). Command may contain bot username.
	raw := strings.TrimSpace(msg)
	if raw == "" || raw[0] != '/' {
		return false
	}
	raw = raw[1:]
	var cmd, rest string
	if sp := strings.IndexFunc(raw, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' }); sp >= 0 {
		cmd = raw[:sp]
		rest = strings.TrimSpace(raw[sp:])
	} else {
		cmd = raw
		rest = ""
	}
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	cmd = strings.ToLower(cmd)

	switch cmd {
	case "pause":
		var paused bool
		ReadState(func(st *State) { paused = st.Paused })
		if paused {
			_ = sendTelegramText(ctx, "Already paused.")
			return true
		}
		UpdateState(func(st *State) { st.Paused = true })
		_ = sendTelegramText(ctx, "Paused auto-processing.")
		_ = updateTelegramCommands(ctx)
		return true
	case "resume":
		var paused bool
		ReadState(func(st *State) { paused = st.Paused })
		if !paused {
			_ = sendTelegramText(ctx, "Already running.")
			return true
		}
		UpdateState(func(st *State) { st.Paused = false })
		_ = sendTelegramText(ctx, "Resumed auto-processing.")
		_ = updateTelegramCommands(ctx)
		return true
	case "new":
		if strings.TrimSpace(rest) != "" {
			_ = sendTelegramText(ctx, "Usage: /new (no arguments).")
			return true
		}
		UpdateState(func(st *State) { st.ResetSession() })
		_ = sendTelegramText(ctx, "OK. Next run will start a new session.")
		return true
	case "commit":
		commitMsg := strings.TrimSpace(rest)
		if commitMsg == "" {
			commitMsg = "changes"
		}
		res, err := commitAndPushAllChanges(ctx, commitMsg)
		if err != nil {
			_ = sendTelegramText(ctx, fmt.Sprintf("Commit failed: %v", err))
			return true
		}
		if !res.DidCommit {
			_ = sendTelegramText(ctx, "No changes to commit.")
			return true
		}
		if res.CommitSHA != "" {
			_ = sendTelegramText(ctx, fmt.Sprintf("Committed and pushed: %s", res.CommitSHA))
		} else {
			_ = sendTelegramText(ctx, "Committed and pushed.")
		}
		return true
	case "cancel":
		var hadPending bool
		ReadState(func(st *State) {
			hadPending = st.PendingLog != nil || len(st.PendingTelegramImages) > 0
		})
		UpdateState(func(st *State) {
			st.PendingLog = nil
			st.PendingTelegramImages = nil
		})
		if hadPending {
			_ = sendTelegramText(ctx, "Canceled pending input.")
		} else {
			_ = sendTelegramText(ctx, "Nothing to cancel.")
		}
		return true
	case "health":
		if strings.TrimSpace(appleHealthExportDir) == "" {
			_ = sendTelegramText(ctx, "Apple Health is disabled (configure apple_health_export_dir in lifebase config).")
			return true
		}
		out, err := renderAppleHealthLast48Hours(msgTime)
		if err != nil {
			_ = sendTelegramText(ctx, fmt.Sprintf("Apple Health failed: %v", err))
			return true
		}
		_ = sendTelegramText(ctx, out)
		return true
	}

	// Proactive prompt triggers (e.g., /morning)
	prompts, err := parsePrompts()
	if err != nil {
		log.Printf("Telegram: prompt parse warning: %v", err)
	}
	for _, p := range prompts {
		if p == nil {
			continue
		}
		key := strings.TrimSpace(p.Key)
		if key != "" && cmd == key {
			_ = sendTelegramText(ctx, fmt.Sprintf("Running %s…", p.Name))
			if err := runProactivePrompt(ctx, p, proactiveRunKey(p)); err != nil {
				_ = sendTelegramText(ctx, fmt.Sprintf("Failed: %v", err))
			}
			return true
		}
	}

	// Lightweight log commands (e.g., /sexlog <text>)
	logSpecs, err := discoverLogSpecs()
	if err != nil {
		log.Printf("Telegram: log discovery failed: %v", err)
		logSpecs = nil
	}
	for _, ls := range logSpecs {
		if cmd == ls.command() || cmd == strings.ReplaceAll(strings.ToLower(ls.FileBasename), " ", "") {
			if strings.TrimSpace(rest) == "" {
				// If the previous command was the same log prompt (no intervening output),
				// skip printing the "recent entries" context to avoid chat spam when retrying the command.
				var showRecent bool = true
				ReadState(func(st *State) {
					if st.LastTGOutput == logPromptOutputTag(ls.FileBasename) {
						showRecent = false
					}
				})

				// Start pending input flow instead of forcing inline argument
				until := time.Now().Add(15 * time.Minute)
				UpdateState(func(st *State) {
					st.PendingLog = &PendingLogInput{
						FileBasename: ls.FileBasename,
						Title:        ls.FileBasename,
						ChatID:       chatID,
						ExpiresAt:    until,
					}
				})
				_ = sendTelegramTextTagged(ctx, logPromptOutputTag(ls.FileBasename), fmt.Sprintf("%s — waiting for entry text. Send a message now (or /cancel).", ls.FileBasename))
				// If a preface file FooLog.md exists, show its content while awaiting input.
				if prefaceFn := ls.prefacePath(); prefaceFn != "" {
					if b, err := os.ReadFile(prefaceFn); err == nil {
						pref := strings.TrimRight(string(b), "\n\r \t")
						if pref != "" {
							_ = sendTelegramTextTagged(ctx, logPromptOutputTag(ls.FileBasename), pref)
						}
					}
				}
				// Also show recent entries for context
				if showRecent {
					if out, err := renderLastLogEntries(ls, msgTime, logEntriesBeforeInput); err == nil {
						_ = sendTelegramTextTagged(ctx, logPromptOutputTag(ls.FileBasename), out)
					}
				}
				return true
			}
			// Add entry at current local time
			if _, err := addLogEntry(ctx, ls, msgTime, rest); err != nil {
				_ = sendTelegramTextTagged(ctx, logWriteOutputTag(ls.FileBasename), fmt.Sprintf("Failed to add to %s: %v", ls.FileBasename, err))
				return true
			}
			if out, err := renderLastLogEntries(ls, msgTime, logEntriesAfterWrite); err == nil {
				_ = sendTelegramTextTagged(ctx, logWriteOutputTag(ls.FileBasename), out)
			} else {
				_ = sendTelegramTextTagged(ctx, logWriteOutputTag(ls.FileBasename), fmt.Sprintf("Added to %s, but failed to read recent entries: %v", ls.FileBasename, err))
			}
			return true
		}
	}

	_ = sendTelegramText(ctx, "Unknown command.")
	return true
}

// updateTelegramCommands sets the bot's command list to include pause/resume
// (opposite of current state) and all prompt commands.
func updateTelegramCommands(ctx context.Context) error {
	var paused bool
	ReadState(func(s *State) { paused = s.Paused })

	var cmds []tgBotCommand
	seen := make(map[string]bool, 64)
	logSpecs, err := discoverLogSpecs()
	if err != nil {
		log.Printf("Telegram: log discovery failed: %v", err)
		logSpecs = nil
	}
	for _, ls := range logSpecs {
		desc := fmt.Sprintf("Add to %s", ls.FileBasename)
		c := ls.command()
		if c != "" && !seen[c] {
			seen[c] = true
			cmds = append(cmds, tgBotCommand{Command: c, Description: desc})
		}
	}
	if strings.TrimSpace(appleHealthExportDir) != "" {
		if _, err := os.Stat(appleHealthExportDir); err == nil {
			if !seen["health"] {
				seen["health"] = true
				cmds = append(cmds, tgBotCommand{Command: "health", Description: "Show Apple Health (last 48h)"})
			}
		}
	}
	if !seen["new"] {
		seen["new"] = true
		cmds = append(cmds, tgBotCommand{Command: "new", Description: "Reset session"})
	}
	if !seen["commit"] {
		seen["commit"] = true
		cmds = append(cmds, tgBotCommand{Command: "commit", Description: "Commit and push all changes"})
	}
	if paused {
		if !seen["resume"] {
			seen["resume"] = true
			cmds = append(cmds, tgBotCommand{Command: "resume", Description: "Resume auto-processing"})
		}
	} else {
		if !seen["pause"] {
			seen["pause"] = true
			cmds = append(cmds, tgBotCommand{Command: "pause", Description: "Pause auto-processing"})
		}
	}

	prompts, err := parsePrompts()
	if err != nil {
		log.Printf("Telegram: prompt parse warning: %v", err)
	}
	for _, p := range prompts {
		if p == nil {
			continue
		}
		key := strings.TrimSpace(p.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		desc := fmt.Sprintf("Trigger %s", p.Name)
		cmds = append(cmds, tgBotCommand{Command: key, Description: desc})
	}
	return telegramSetMyCommands(ctx, secrets.TelegramBotToken, cmds)
}

type tgBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

func telegramSetMyCommands(ctx context.Context, botToken string, commands []tgBotCommand) error {
	if strings.TrimSpace(botToken) == "" {
		return fmt.Errorf("telegram not configured")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", botToken)
	payload := map[string]any{
		"commands": commands,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 30 * time.Second}
	res, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var resp struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return err
	}
	if !resp.Ok {
		if resp.Description == "" {
			resp.Description = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return fmt.Errorf("setMyCommands failed: %s", resp.Description)
	}
	return nil
}

const (
	defaultTelegramLongPollTimeoutSeconds = 50
	telegramMediaGroupFlushAfter          = 1500 * time.Millisecond
)

type telegramImageBatch struct {
	ReceivedAt   time.Time
	MediaGroupID string
	Caption      string
	ImagePaths   []string
}

type pendingTelegramMediaGroup struct {
	Batch    telegramImageBatch
	LastSeen time.Time
}

type tgUpdates struct {
	Ok     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message,omitempty"`
}

type tgMessage struct {
	MessageID    int           `json:"message_id"`
	Date         int64         `json:"date"`
	Chat         tgChat        `json:"chat"`
	Text         string        `json:"text"`
	Caption      string        `json:"caption,omitempty"`
	MediaGroupID string        `json:"media_group_id,omitempty"`
	Photo        []tgPhotoSize `json:"photo,omitempty"`
	Voice        *tgVoice      `json:"voice,omitempty"`
	Audio        *tgAudio      `json:"audio,omitempty"`
	Document     *tgDocument   `json:"document,omitempty"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type tgVoice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type tgAudio struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type tgDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type tgGetFileResp struct {
	Ok          bool       `json:"ok"`
	Result      tgFileInfo `json:"result"`
	Description string     `json:"description,omitempty"`
}

type tgFileInfo struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int    `json:"file_size"`
}

func telegramPollTimeoutSeconds(groups map[string]*pendingTelegramMediaGroup, now time.Time) int {
	timeout := defaultTelegramLongPollTimeoutSeconds
	var wait time.Duration
	var havePending bool
	for _, group := range groups {
		untilFlush := group.LastSeen.Add(telegramMediaGroupFlushAfter).Sub(now)
		if untilFlush <= 0 {
			return 1
		}
		if !havePending || untilFlush < wait {
			wait = untilFlush
			havePending = true
		}
	}
	if !havePending {
		return timeout
	}
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	if secs < timeout {
		timeout = secs
	}
	return timeout
}

func addTelegramMediaGroupBatch(groups map[string]*pendingTelegramMediaGroup, batch telegramImageBatch) {
	groupID := strings.TrimSpace(batch.MediaGroupID)
	if groupID == "" {
		return
	}
	existing := groups[groupID]
	if existing == nil {
		groups[groupID] = &pendingTelegramMediaGroup{
			Batch: telegramImageBatch{
				ReceivedAt:   batch.ReceivedAt,
				MediaGroupID: groupID,
				Caption:      strings.TrimSpace(batch.Caption),
				ImagePaths:   append([]string(nil), batch.ImagePaths...),
			},
			LastSeen: batch.ReceivedAt,
		}
		return
	}
	if existing.Batch.ReceivedAt.IsZero() || batch.ReceivedAt.Before(existing.Batch.ReceivedAt) {
		existing.Batch.ReceivedAt = batch.ReceivedAt
	}
	if existing.Batch.Caption == "" && strings.TrimSpace(batch.Caption) != "" {
		existing.Batch.Caption = strings.TrimSpace(batch.Caption)
	}
	existing.Batch.ImagePaths = append(existing.Batch.ImagePaths, batch.ImagePaths...)
	existing.LastSeen = batch.ReceivedAt
}

func takeReadyTelegramMediaGroups(groups map[string]*pendingTelegramMediaGroup, now time.Time, forceAll bool) []telegramImageBatch {
	var batches []telegramImageBatch
	for groupID, group := range groups {
		if forceAll || !group.LastSeen.Add(telegramMediaGroupFlushAfter).After(now) {
			batches = append(batches, group.Batch)
			delete(groups, groupID)
		}
	}
	sortTelegramImageBatches(batches)
	return batches
}

func takeAllTelegramMediaGroups(groups map[string]*pendingTelegramMediaGroup) []telegramImageBatch {
	return takeReadyTelegramMediaGroups(groups, time.Now(), true)
}

func takeOtherTelegramMediaGroups(groups map[string]*pendingTelegramMediaGroup, keepGroupID string) []telegramImageBatch {
	var batches []telegramImageBatch
	for groupID, group := range groups {
		if groupID == keepGroupID {
			continue
		}
		batches = append(batches, group.Batch)
		delete(groups, groupID)
	}
	sortTelegramImageBatches(batches)
	return batches
}

func sortTelegramImageBatches(batches []telegramImageBatch) {
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].ReceivedAt.Equal(batches[j].ReceivedAt) {
			return batches[i].MediaGroupID < batches[j].MediaGroupID
		}
		return batches[i].ReceivedAt.Before(batches[j].ReceivedAt)
	})
}

func processTelegramImageBatches(ctx context.Context, tasks chan<- ingestTask, batches []telegramImageBatch) error {
	var firstErr error
	for _, batch := range batches {
		if err := processTelegramImageBatch(ctx, tasks, batch); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func processTelegramImageBatch(ctx context.Context, tasks chan<- ingestTask, batch telegramImageBatch) error {
	if len(batch.ImagePaths) == 0 {
		return nil
	}
	tm := batch.ReceivedAt.Local()
	updateLastIncoming(tm)

	caption := strings.TrimSpace(batch.Caption)
	if caption == "" {
		pending := make([]PendingTelegramImage, 0, len(batch.ImagePaths))
		for _, path := range batch.ImagePaths {
			if p := strings.TrimSpace(path); p != "" {
				pending = append(pending, PendingTelegramImage{Path: p, ReceivedAt: tm})
			}
		}
		appendPendingTelegramImages(pending)
		log.Printf("Queued Telegram image batch (%d images)", len(pending))
		return nil
	}

	fn := uniqueRawInputPath(tm, "tg")
	if err := os.WriteFile(fn, []byte(caption+"\n"), 0666); err != nil {
		return fmt.Errorf("write telegram caption raw input: %w", err)
	}
	imagePaths := append(takePendingTelegramImagePaths(), batch.ImagePaths...)
	log.Printf("New Telegram captioned image batch -> %s (%d images)", filepath.Base(fn), len(imagePaths))
	tasks <- ingestTask{typ: taskTelegramText, path: fn, displayName: filepath.Base(fn), imagePaths: imagePaths}
	return nil
}

func telegramSaveImageBatch(ctx context.Context, msg *tgMessage) (*telegramImageBatch, error) {
	var fileID string
	var fallbackName string
	var mimeType string

	if photo := largestTelegramPhoto(msg.Photo); photo != nil {
		fileID = photo.FileID
	} else if telegramDocumentIsImage(msg.Document) {
		fileID = msg.Document.FileID
		fallbackName = msg.Document.FileName
		mimeType = msg.Document.MimeType
	} else {
		return nil, nil
	}

	filePath, err := telegramGetFilePath(ctx, secrets.TelegramBotToken, fileID)
	if err != nil {
		return nil, err
	}

	tm := time.Unix(msg.Date, 0).Local()
	dest := uniqueRawPath(tm, "tg-img", telegramImageExt(filePath, fallbackName, mimeType))
	if err := telegramDownloadToPath(ctx, secrets.TelegramBotToken, filePath, dest); err != nil {
		return nil, err
	}

	return &telegramImageBatch{
		ReceivedAt:   tm,
		MediaGroupID: strings.TrimSpace(msg.MediaGroupID),
		Caption:      strings.TrimSpace(msg.Caption),
		ImagePaths:   []string{dest},
	}, nil
}

func largestTelegramPhoto(photos []tgPhotoSize) *tgPhotoSize {
	if len(photos) == 0 {
		return nil
	}
	best := &photos[0]
	bestScore := best.Width * best.Height
	for i := 1; i < len(photos); i++ {
		score := photos[i].Width * photos[i].Height
		if score > bestScore || (score == bestScore && photos[i].FileSize > best.FileSize) {
			best = &photos[i]
			bestScore = score
		}
	}
	return best
}

func telegramDocumentIsImage(doc *tgDocument) bool {
	if doc == nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(doc.MimeType)), "image/") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(filepath.Ext(doc.FileName))) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".heic", ".heif", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func telegramImageExt(filePath, fallbackName, mimeType string) string {
	for _, candidate := range []string{filePath, fallbackName} {
		if ext := strings.ToLower(strings.TrimSpace(filepath.Ext(candidate))); ext != "" {
			return ext
		}
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	case "image/heif":
		return ".heif"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}

func telegramGetFilePath(ctx context.Context, token, fileID string) (string, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	cli := &http.Client{Timeout: 30 * time.Second}
	res, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var resp tgGetFileResp
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return "", err
	}
	if !resp.Ok || resp.Result.FilePath == "" {
		desc := resp.Description
		if desc == "" {
			desc = "unknown error"
		}
		// Telegram Bot API has a 20 MB file size limit
		if strings.Contains(strings.ToLower(desc), "file is too big") {
			return "", fmt.Errorf("%s (Telegram Bot API limit is 20 MB; use -add with a local file for large recordings)", desc)
		}
		return "", fmt.Errorf("getFile failed: %s", desc)
	}
	return resp.Result.FilePath, nil
}

func telegramDownloadToPath(ctx context.Context, token, filePath, destPath string) error {
	u := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	cli := &http.Client{Timeout: 5 * time.Minute}
	res, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("download HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o777); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(destPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	return nil
}

func telegramDownloadToTemp(ctx context.Context, token, filePath string) (string, error) {
	ext := filepath.Ext(filePath)
	if ext == "" {
		ext = ".ogg"
	}
	f, err := os.CreateTemp("", "tg-download-*"+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := telegramDownloadToPath(ctx, token, filePath, path); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func uniqueRawInputPath(t time.Time, suffix string) string {
	return uniqueRawPath(t, suffix, ".md")
}

func uniqueRawPath(t time.Time, suffix, ext string) string {
	base := t.Format(rawFileTimeFormat)
	if suffix != "" {
		base = base + "-" + suffix
	}
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".bin"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	// ensure unique by appending -1, -2 if needed
	candidate := filepath.Join(rawInputsDir, base+ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(rawInputsDir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
}

func splitTelegramChunks(s string, max int) []string {
	r := []rune(s)
	if len(r) <= max {
		return []string{s}
	}
	var out []string
	for len(r) > 0 {
		if len(r) <= max {
			out = append(out, string(r))
			break
		}
		// try to split on paragraph boundary within max
		cut := findSplitPoint(r, max)
		out = append(out, string(r[:cut]))
		r = r[cut:]
		// trim leading newlines/spaces for the next chunk
		for len(r) > 0 && (r[0] == '\n' || r[0] == ' ') {
			r = r[1:]
		}
	}
	return out
}

func findSplitPoint(r []rune, max int) int {
	// hard limit
	if len(r) <= max {
		return len(r)
	}
	// search for "\n\n", then "\n", then space, within the window
	window := r[:max]
	// double newline
	for i := len(window) - 2; i >= 0; i-- {
		if window[i] == '\n' && window[i+1] == '\n' {
			return i + 1
		}
	}
	// single newline
	for i := len(window) - 1; i >= 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	// space
	for i := len(window) - 1; i >= 0; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	// nothing found, hard cut
	return max
}

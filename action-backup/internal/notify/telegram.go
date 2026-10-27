package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"action-backup/internal/config"
)

// Telegram Bot 推送渠道。API：POST {base}/bot{token}/sendMessage，JSON 体 chat_id / text。
// 请求 URL 含 token，所有错误信息都会把 token 替换成 ***。
type Telegram struct {
	base   string
	token  string
	chatID string
	client *http.Client
}

// NewTelegram 创建 Telegram 渠道；proxy_url 非空时经代理（http / https / socks5）。
func NewTelegram(cfg config.TelegramConfig, timeout time.Duration) (*Telegram, error) {
	token := strings.TrimSpace(cfg.BotToken)
	chat := strings.TrimSpace(cfg.ChatID)
	if token == "" || token == config.Masked {
		return nil, fmt.Errorf("%w: telegram bot_token is empty", ErrChannelNotConfigured)
	}
	if chat == "" {
		return nil, fmt.Errorf("%w: telegram chat_id is empty", ErrChannelNotConfigured)
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = config.DefaultTelegramBaseURL
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if p := strings.TrimSpace(cfg.ProxyURL); p != "" {
		u, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("telegram proxy_url: %w", err)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &Telegram{base: base, token: token, chatID: chat, client: &http.Client{Timeout: timeout, Transport: transport}}, nil
}

// Name 渠道名。
func (t *Telegram) Name() string { return ChannelTelegram }

type telegramRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// Send 推送。
func (t *Telegram) Send(ctx context.Context, msg Message) error {
	text := msg.Title + "\n" + msg.Body
	if msg.URL != "" {
		text += "\n" + msg.URL
	}
	body, err := json.Marshal(telegramRequest{ChatID: t.chatID, Text: text, DisableWebPagePreview: true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return t.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", t.redact(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r telegramResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram: http %d: %s", resp.StatusCode, t.redactString(truncate(string(raw), 200)))
	}
	if !r.OK {
		return fmt.Errorf("telegram: error %d: %s", r.ErrorCode, t.redactString(r.Description))
	}
	return nil
}

type redactedError struct{ msg string }

func (e redactedError) Error() string { return e.msg }

func (t *Telegram) redact(err error) error {
	return redactedError{t.redactString(err.Error())}
}

func (t *Telegram) redactString(s string) string {
	return strings.ReplaceAll(s, t.token, config.Masked)
}

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

// MeoW 推送渠道。API：POST {base}/{昵称}，JSON 体 title / msg / url；HTTP 永远 200，业务结果在 JSON status。
// 文档：https://www.chuckfang.com/MeoW/api_doc.html
type MeoW struct {
	base     string
	nickname string
	client   *http.Client
}

// NewMeoW 创建 MeoW 渠道。
func NewMeoW(cfg config.MeoWConfig, timeout time.Duration) (*MeoW, error) {
	nick := strings.TrimSpace(cfg.Nickname)
	if nick == "" {
		return nil, fmt.Errorf("%w: meow nickname is empty", ErrChannelNotConfigured)
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = config.DefaultMeoWBaseURL
	}
	return &MeoW{base: base, nickname: nick, client: &http.Client{Timeout: timeout}}, nil
}

// Name 渠道名。
func (m *MeoW) Name() string { return ChannelMeoW }

type meowRequest struct {
	Title string `json:"title,omitempty"`
	Msg   string `json:"msg"`
	URL   string `json:"url,omitempty"`
}

type meowResponse struct {
	Status  int    `json:"status"`
	Msg     string `json:"msg"`
	Message string `json:"message"`
}

// Send 推送。
func (m *MeoW) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(meowRequest{Title: msg.Title, Msg: msg.Body, URL: msg.URL})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/"+url.PathEscape(m.nickname), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("meow: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("meow: http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var r meowResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("meow: unexpected response: %s", truncate(string(raw), 200))
	}
	if r.Status != 200 {
		desc := r.Msg
		if desc == "" {
			desc = r.Message
		}
		return fmt.Errorf("meow: status %d: %s", r.Status, desc)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

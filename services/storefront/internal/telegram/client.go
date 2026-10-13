package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Client struct {
	Token, Username, Secret string
	Groups                  []int64
	HTTP                    *http.Client
	RegistrationClosed      bool
}

func NewClient(token, username, secret string, groups []int64) (*Client, error) {
	if !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`).MatchString(token) || !regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`).MatchString(username) || !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(secret) || len(groups) < 1 || len(groups) > 20 {
		return nil, errors.New("invalid Telegram configuration")
	}
	for _, g := range groups {
		if g >= 0 || g < -9007199254740991 {
			return nil, errors.New("invalid Telegram group")
		}
	}
	return &Client{Token: token, Username: username, Secret: secret, Groups: groups, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Call(ctx context.Context, method string, payload, out any) error {
	if !slices.Contains([]string{"getChatMember", "getChat", "getMe", "getWebhookInfo", "setWebhook", "setMyCommands", "setChatPermissions", "deleteMessages", "sendMessage", "answerCallbackQuery"}, method) {
		return errors.New("invalid bot operation")
	}
	b, _ := json.Marshal(payload)
	r, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+c.Token+"/"+method, strings.NewReader(string(b)))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(r)
	if e != nil {
		return errors.New("bot service unavailable")
	}
	defer res.Body.Close()
	b, e = io.ReadAll(io.LimitReader(res.Body, 32769))
	if e != nil || len(b) > 32768 || res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("bot service unavailable")
	}
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &response) != nil || !response.OK {
		return errors.New("bot operation rejected")
	}
	if out != nil && json.Unmarshal(response.Result, out) != nil {
		return errors.New("invalid bot response")
	}
	return nil
}
func (c *Client) member(ctx context.Context, group, user int64) (bool, error) {
	if !slices.Contains(c.Groups, group) {
		return false, nil
	}
	var r struct {
		Status string `json:"status"`
		Member bool   `json:"is_member"`
	}
	e := c.Call(ctx, "getChatMember", map[string]any{"chat_id": group, "user_id": user}, &r)
	return slices.Contains([]string{"creator", "administrator", "member"}, r.Status) || (r.Status == "restricted" && r.Member), e
}
func (c *Client) applicant(ctx context.Context, user int64, preferred *int64) (*int64, error) {
	groups := append([]int64(nil), c.Groups...)
	if preferred != nil {
		if i := slices.Index(groups, *preferred); i >= 0 {
			groups[0], groups[i] = groups[i], groups[0]
		}
	}
	var unavailable error
	for offset := 0; offset < len(groups); offset += 5 {
		batch := groups[offset:min(offset+5, len(groups))]
		type result struct {
			group  int64
			member bool
			e      error
		}
		ch := make(chan result, len(batch))
		for _, g := range batch {
			go func() { m, e := c.member(ctx, g, user); ch <- result{g, m, e} }()
		}
		var selected *int64
		for range batch {
			r := <-ch
			if r.e != nil {
				unavailable = r.e
			}
			if r.member {
				g := r.group
				selected = &g
			}
		}
		if selected != nil {
			return selected, nil
		}
	}
	return nil, unavailable
}

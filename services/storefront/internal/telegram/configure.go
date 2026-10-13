package telegram

import (
	"context"
	"errors"
	"slices"
)

// ConfigureModeration updates the existing production webhook and administrator
// menu without changing credentials, dropping updates or changing group permissions.
func (c *Client) ConfigureModeration(ctx context.Context, url string) error {
	var webhook struct {
		URL         string   `json:"url"`
		Allowed     []string `json:"allowed_updates"`
		Certificate bool     `json:"has_custom_certificate"`
	}
	if err := c.Call(ctx, "getWebhookInfo", struct{}{}, &webhook); err != nil {
		return err
	}
	if webhook.URL != url || webhook.Certificate {
		return errors.New("existing webhook does not match this deployment")
	}
	if webhook.Allowed == nil {
		webhook.Allowed = []string{}
	}
	for _, kind := range []string{"message", "edited_message", "callback_query"} {
		if len(webhook.Allowed) > 0 && !slices.Contains(webhook.Allowed, kind) {
			webhook.Allowed = append(webhook.Allowed, kind)
		}
	}
	// An empty allowed_updates list already subscribes to messages and edits.
	if err := c.Call(ctx, "setWebhook", map[string]any{"url": url, "secret_token": c.Secret, "allowed_updates": webhook.Allowed, "max_connections": 1, "drop_pending_updates": false}, nil); err != nil {
		return err
	}
	commands := []map[string]string{
		{"command": "apply", "description": "打开代理申请入口"},
		{"command": "groupid", "description": "查看本群 ID"},
		{"command": "mute", "description": "开启全群禁言并自动删除漏发消息"},
		{"command": "unmute", "description": "解除禁言，恢复原群权限"},
		{"command": "mutestatus", "description": "查看全群禁言状态"},
		{"command": "blockword", "description": "添加屏蔽词：/blockword 词语"},
		{"command": "unblockword", "description": "移除屏蔽词：/unblockword 词语"},
		{"command": "blockwords", "description": "查看本群屏蔽词"},
	}
	// Per-group scopes leave other groups and the private applicant menu alone.
	for _, group := range c.Groups {
		if err := c.Call(ctx, "setMyCommands", map[string]any{"scope": map[string]any{"type": "chat_administrators", "chat_id": group}, "commands": commands}, nil); err != nil {
			return err
		}
	}
	return nil
}

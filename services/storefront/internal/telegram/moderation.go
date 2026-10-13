package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

var moderationCommand = regexp.MustCompile(`(?i)^/(mute|unmute|mutestatus)(?:@([A-Za-z0-9_]+))?$`)

func (c *Client) administrator(ctx context.Context, m *Message) (bool, error) {
	// Telegram marks anonymous administrators with the group's own sender_chat.
	// A linked channel or a user's channel must never inherit from's placeholder identity.
	if m.SenderChat != nil {
		return m.SenderChat.ID == m.Chat.ID, nil
	}
	if m.From == nil || m.From.ID <= 0 || m.From.ID > 9007199254740991 {
		return false, nil
	}
	var member struct {
		Status string `json:"status"`
	}
	err := c.Call(ctx, "getChatMember", map[string]any{"chat_id": m.Chat.ID, "user_id": m.From.ID}, &member)
	return member.Status == "creator" || member.Status == "administrator", err
}

// Runs before applicant filtering, including for media, long text, sender_chat and edits.
func (s *Service) moderate(ctx context.Context, u *Update, body []byte) (bool, error) {
	m := u.Message
	if m == nil {
		m = u.Edited
	}
	if m == nil || !slices.Contains([]string{"group", "supergroup"}, m.Chat.Type) || !slices.Contains(s.Client.Groups, m.Chat.ID) {
		return false, nil
	}
	ownID, _ := strconv.ParseInt(strings.SplitN(s.Client.Token, ":", 2)[0], 10, 64)
	if m.SenderChat == nil && m.From != nil && m.From.Bot && m.From.ID == ownID {
		return true, nil
	}
	command := moderationCommand.FindStringSubmatch(strings.TrimSpace(m.Text))
	addressed := command != nil && (command[2] == "" || strings.EqualFold(command[2], s.Client.Username)) && u.Message != nil
	wordCommand := blockwordCommand.FindStringSubmatch(strings.TrimSpace(m.Text))
	wordAddressed := wordCommand != nil && (wordCommand[2] == "" || strings.EqualFold(wordCommand[2], s.Client.Username)) && u.Message != nil
	if wordAddressed {
		admin, err := s.Client.administrator(ctx, m)
		if err != nil {
			return true, err
		}
		if admin {
			return true, s.blockwordControl(ctx, u, m, strings.ToLower(wordCommand[1]), wordCommand[3], body)
		}
	}
	var muted bool
	var words []string
	if err := s.Store.DB.QueryRow(ctx, `SELECT COALESCE((SELECT muted OR target_muted FROM telegram_group_moderation WHERE group_id=$1),false), COALESCE((SELECT blocked_words FROM telegram_group_moderation WHERE group_id=$1),'{}'::text[])`, m.Chat.ID).Scan(&muted, &words); err != nil {
		return true, err
	}
	// Matching content is removed silently, including administrators and captions.
	// Only authenticated configuration commands and this bot's own replies are exempt.
	if containsBlockedWord(m.Text, words) || containsBlockedWord(m.Caption, words) {
		return true, s.deleteGroupMessage(ctx, m)
	}
	if !addressed && !muted {
		if wordAddressed {
			return true, s.Client.Call(ctx, "sendMessage", reply(m.Chat.ID, "仅群主或管理员可以管理屏蔽词。"), nil)
		}
		return u.Edited != nil || m.SenderChat != nil, nil
	}
	admin, err := s.Client.administrator(ctx, m)
	if err != nil {
		return true, err
	} // Retry if role lookup fails; never grant control on API failure.
	if !admin {
		if muted {
			return true, s.deleteGroupMessage(ctx, m)
		}
		if addressed {
			return true, s.Client.Call(ctx, "sendMessage", reply(m.Chat.ID, "仅群主或管理员可以操作全群禁言。"), nil)
		}
		return true, nil
	}
	if !addressed {
		return u.Edited != nil || m.SenderChat != nil, nil
	}
	return true, s.moderationControl(ctx, u, m, strings.ToLower(command[1]), body)
}

func (s *Service) deleteGroupMessage(ctx context.Context, m *Message) error {
	if m.ID <= 0 || m.SupergroupCreated || m.ChannelCreated || len(m.TopicCreated) > 0 {
		return nil
	}
	// deleteMessages skips already deleted IDs, so webhook redelivery is safe.
	return s.Client.Call(ctx, "deleteMessages", map[string]any{"chat_id": m.Chat.ID, "message_ids": []int64{m.ID}}, nil)
}

func (s *Service) moderationControl(ctx context.Context, u *Update, m *Message, command string, body []byte) error {
	// Session advisory lock serializes permission calls across API replicas. State is
	// committed BEFORE Telegram, preserving the original permissions through crashes.
	conn, err := s.Store.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(903494,hashtext($1))`, strconv.FormatInt(m.Chat.ID, 10)); err != nil {
		// Cancellation can race with lock acquisition; never pool that session.
		_ = conn.Conn().Close(context.Background())
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock(903494,hashtext($1))`, strconv.FormatInt(m.Chat.ID, 10)); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	digest := security.Hash(string(body))
	inserted, err := tx.Exec(ctx, `INSERT INTO telegram_partner_updates(update_id,created_at,body_hash) VALUES($1,$2,$3) ON CONFLICT(update_id) DO NOTHING`, *u.ID, time.Now().UnixMilli(), digest)
	if err != nil {
		return err
	}
	var saved, original *string
	if err = tx.QueryRow(ctx, `SELECT reply,body_hash FROM telegram_partner_updates WHERE update_id=$1 FOR UPDATE`, *u.ID).Scan(&saved, &original); err != nil {
		return err
	}
	if original == nil || *original != digest {
		return fault.New(409, "更新编号已绑定其他内容。")
	}
	if saved != nil {
		var response Reply
		if err = json.Unmarshal([]byte(*saved), &response); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return s.Client.Call(ctx, "sendMessage", &response, nil)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO telegram_group_moderation(group_id) VALUES($1) ON CONFLICT(group_id) DO NOTHING`, m.Chat.ID); err != nil {
		return err
	}
	var muted, target bool
	var previous map[string]bool
	var last *int64
	if err = tx.QueryRow(ctx, `SELECT muted,target_muted,previous_permissions,last_update_id FROM telegram_group_moderation WHERE group_id=$1`, m.Chat.ID).Scan(&muted, &target, &previous, &last); err != nil {
		return err
	}
	var response *Reply
	apply := false
	if command == "mutestatus" {
		text := "本群未开启机器人全群禁言。"
		if muted || target {
			text = "本群已开启全群禁言，普通成员漏发的消息会自动删除。"
		}
		response = reply(m.Chat.ID, text)
	} else {
		// A receipt without a reply is a durable pending operation. A newer command
		// supersedes it; retrying the old update must not reverse the newer setting.
		if inserted.RowsAffected() == 0 && (last == nil || *last != *u.ID) {
			response = reply(m.Chat.ID, "此操作已被后续禁言设置替代，请用 /mutestatus 查看当前状态。")
		} else if inserted.RowsAffected() == 0 && last != nil && *last == *u.ID {
			apply = true
		} else {
			target = command == "mute"
			if target && !muted {
				var me struct {
					ID int64 `json:"id"`
				}
				var rights struct {
					Status   string `json:"status"`
					Restrict bool   `json:"can_restrict_members"`
					Delete   bool   `json:"can_delete_messages"`
				}
				if err = s.Client.Call(ctx, "getMe", struct{}{}, &me); err != nil {
					return err
				}
				if err = s.Client.Call(ctx, "getChatMember", map[string]any{"chat_id": m.Chat.ID, "user_id": me.ID}, &rights); err != nil {
					return err
				}
				if rights.Status != "administrator" || !rights.Restrict || !rights.Delete {
					response = reply(m.Chat.ID, "无法开启：请授予机器人“封禁用户”和“删除消息”管理员权限，再发送 /mute。")
				} else {
					var chat struct {
						Permissions map[string]bool `json:"permissions"`
					}
					if err = s.Client.Call(ctx, "getChat", map[string]any{"chat_id": m.Chat.ID}, &chat); err != nil {
						return err
					}
					if len(chat.Permissions) == 0 {
						return errors.New("chat permissions unavailable")
					}
					previous = chat.Permissions
					apply = true
				}
			} else if muted {
				apply = true
			} else {
				response = reply(m.Chat.ID, "本群未开启机器人全群禁言。")
			}
			if apply {
				data, e := json.Marshal(previous)
				if e != nil {
					return e
				}
				if _, err = tx.Exec(ctx, `UPDATE telegram_group_moderation SET muted=muted OR $1,target_muted=$1,previous_permissions=$2,last_update_id=$3,updated_at=now() WHERE group_id=$4`, target, data, *u.ID, m.Chat.ID); err != nil {
					return err
				}
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if apply {
		permissions := make(map[string]bool, len(previous))
		for key, value := range previous {
			permissions[key] = value
		}
		if target {
			for _, key := range []string{"can_send_messages", "can_send_audios", "can_send_documents", "can_send_photos", "can_send_videos", "can_send_video_notes", "can_send_voice_notes", "can_send_polls", "can_send_other_messages", "can_add_web_page_previews", "can_react_to_messages"} {
				permissions[key] = false
			}
		}
		if err = s.Client.Call(ctx, "setChatPermissions", map[string]any{"chat_id": m.Chat.ID, "permissions": permissions, "use_independent_chat_permissions": true}, nil); err != nil {
			return err
		}
		if _, err = conn.Exec(ctx, `UPDATE telegram_group_moderation SET muted=target_muted,updated_at=now() WHERE group_id=$1 AND last_update_id=$2`, m.Chat.ID, *u.ID); err != nil {
			return err
		}
		text := "已解除全群禁言，恢复开启前的群权限。"
		if target {
			text = "已开启全群禁言。普通成员不能发言，漏发消息会自动删除；管理员仍可发布通知。\n/unmute 解除，/mutestatus 查看状态。"
		}
		response = reply(m.Chat.ID, text)
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err = conn.Exec(ctx, `UPDATE telegram_partner_updates SET reply=$1 WHERE update_id=$2`, string(data), *u.ID); err != nil {
		return err
	}
	return s.Client.Call(ctx, "sendMessage", response, nil)
}

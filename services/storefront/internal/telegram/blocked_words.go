package telegram

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

var blockwordCommand = regexp.MustCompile(`(?is)^/(blockword|unblockword|blockwords)(?:@([A-Za-z0-9_]+))?(?:\s+(.*))?$`)

func normalizeWord(text string) string {
	return strings.ToLower(norm.NFKC.String(text))
}

func wordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}

func containsBlockedWord(text string, words []string) bool {
	text = normalizeWord(text)
	for _, word := range words {
		word = normalizeWord(word)
		if word == "" {
			continue
		}
		whole := true
		for i := 0; i < len(word); i++ {
			whole = whole && wordByte(word[i])
		}
		for offset := 0; offset <= len(text)-len(word); {
			index := strings.Index(text[offset:], word)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(word)
			if !whole || (start == 0 || !wordByte(text[start-1])) && (end == len(text) || !wordByte(text[end])) {
				return true
			}
			offset = start + 1
		}
	}
	return false
}

func (s *Service) blockwordControl(ctx context.Context, u *Update, m *Message, command, argument string, body []byte) error {
	word := strings.TrimSpace(normalizeWord(argument))
	// Telegram limits messages in UTF-16 units. This also keeps a full 100-word
	// list, including emoji, below its 4096-unit reply limit.
	if command != "blockwords" && (word == "" || len(utf16.Encode([]rune(word))) > 32 || strings.ContainsFunc(word, unicode.IsControl)) || command == "blockwords" && word != "" {
		return s.Client.Call(ctx, "sendMessage", reply(m.Chat.ID, "用法：\n/blockword 词语 添加\n/unblockword 词语 移除\n/blockwords 查看\n每次一个词或短语，最多 32 字；每群最多 100 个。"), nil)
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize with mute controls, preserving both independent settings.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(903494,hashtext($1))`, strconv.FormatInt(m.Chat.ID, 10)); err != nil {
		return err
	}
	digest := security.Hash(string(body))
	if _, err = tx.Exec(ctx, `INSERT INTO telegram_partner_updates(update_id,created_at,body_hash) VALUES($1,$2,$3) ON CONFLICT(update_id) DO NOTHING`, *u.ID, time.Now().UnixMilli(), digest); err != nil {
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
	var words []string
	var last *int64
	if err = tx.QueryRow(ctx, `SELECT blocked_words,words_last_update_id FROM telegram_group_moderation WHERE group_id=$1 FOR UPDATE`, m.Chat.ID).Scan(&words, &last); err != nil {
		return err
	}
	text := ""
	change := false
	switch {
	case command == "blockwords":
		text = "本群暂无屏蔽词。使用 /blockword 词语 添加。"
		if len(words) > 0 {
			text = "本群屏蔽词（" + strconv.Itoa(len(words)) + " 个）：\n" + strings.Join(words, "\n") + "\n\n命中直接删消息；英文完整词匹配，忽略大小写和全半角。"
		}
	case last != nil && *u.ID <= *last:
		text = "此命令早于当前屏蔽词设置，请用 /blockwords 查看。"
	case command == "unblockword":
		words = slices.DeleteFunc(words, func(v string) bool { return v == word })
		text, change = "已移除屏蔽词："+word, true
	case slices.Contains(words, word):
		text, change = "已存在屏蔽词："+word, true
	case len(words) >= 100:
		text = "本群已有 100 个屏蔽词，请先用 /unblockword 词语 移除。"
	default:
		var me struct {
			ID int64 `json:"id"`
		}
		var rights struct {
			Status string `json:"status"`
			Delete bool   `json:"can_delete_messages"`
		}
		if err = s.Client.Call(ctx, "getMe", struct{}{}, &me); err != nil {
			return err
		}
		if err = s.Client.Call(ctx, "getChatMember", map[string]any{"chat_id": m.Chat.ID, "user_id": me.ID}, &rights); err != nil {
			return err
		}
		if rights.Status != "administrator" || !rights.Delete {
			text = "请先授予机器人“删除消息”管理员权限，再添加屏蔽词。"
		} else {
			words = append(words, word)
			text, change = "已添加屏蔽词："+word+"。命中后直接删除消息。", true
		}
	}
	if change {
		if _, err = tx.Exec(ctx, `UPDATE telegram_group_moderation SET blocked_words=$1,words_last_update_id=$2,updated_at=now() WHERE group_id=$3`, words, *u.ID, m.Chat.ID); err != nil {
			return err
		}
		actor := "telegram-group:" + strconv.FormatInt(m.Chat.ID, 10)
		if m.SenderChat == nil && m.From != nil {
			actor = "telegram:" + strconv.FormatInt(m.From.ID, 10)
		}
		detail, _ := json.Marshal(map[string]any{"command": command, "word": word, "update_id": *u.ID})
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail) VALUES($1,'system',$2,'telegram-blockword',$3,$4)`, security.ID(), actor, strconv.FormatInt(m.Chat.ID, 10), json.RawMessage(detail)); err != nil {
			return err
		}
	}
	response := reply(m.Chat.ID, text)
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE telegram_partner_updates SET reply=$1 WHERE update_id=$2`, string(data), *u.ID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.Client.Call(ctx, "sendMessage", response, nil)
}

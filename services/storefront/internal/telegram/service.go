package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/management"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Store  *store.Store
	Auth   *auth.Service
	Client *Client
	Sender mailer.Sender
	Origin string
}
type session struct {
	User                    int64
	Group                   *int64
	Step, Wechat, QQ, Email string
	Expires, Last           int64
}
type Reply struct {
	Chat    int64           `json:"chat_id"`
	Text    string          `json:"text"`
	Preview map[string]bool `json:"link_preview_options"`
	Markup  any             `json:"reply_markup,omitempty"`
}

func reply(user int64, text string, buttons ...[]map[string]string) *Reply {
	r := &Reply{Chat: user, Text: text, Preview: map[string]bool{"is_disabled": true}}
	if len(buttons) > 0 {
		r.Markup = map[string]any{"inline_keyboard": buttons}
	}
	return r
}
func buttons(s *session) [][]map[string]string {
	return [][]map[string]string{{{"text": "确认并开通", "callback_data": "apply_confirm:" + strconv.FormatInt(s.Expires, 10)}}, {{"text": "重新填写", "callback_data": "apply_restart"}, {"text": "取消", "callback_data": "apply_cancel"}}}
}
func reset(s *session) { s.Step = "idle"; s.Wechat = ""; s.QQ = ""; s.Email = ""; s.Expires = 0 }

const registrationPaused = "机器人已暂停新代理注册，暂不接受新的申请。已有代理仍可正常登录工作台。"

func (s *Service) status(status string) string {
	prefix := "代理已开通，邀请邮件正在发送，完成后会在这里通知你。"
	if status == "sent" {
		prefix = "代理已开通，邀请邮件已发送，请检查收件箱及垃圾邮件。"
	} else if status == "failed" {
		prefix = "代理已开通，但邀请邮件暂未确认发送成功。请联系管理员重发邀请，也可使用申请邮箱获取验证码登录。"
	}
	return prefix + "\n工作台：" + s.Origin + "/join"
}
func (s *Service) plan(ctx context.Context, tx pgx.Tx, v *session, text string, callback bool) (*Reply, error) {
	command := strings.ToLower(strings.Fields(text)[0])
	var status string
	e := tx.QueryRow(ctx, `SELECT mail_status FROM telegram_partner_applications WHERE user_id=$1`, v.User).Scan(&status)
	if e != nil && e != pgx.ErrNoRows {
		return nil, e
	}
	// Check before every application step, including previously issued confirmation
	// buttons. Existing registrations may still query their invitation status.
	if s.Client.RegistrationClosed {
		if status != "" {
			return reply(v.User, s.status(status)), nil
		}
		reset(v)
		return reply(v.User, registrationPaused), nil
	}
	if command == "/help" {
		return reply(v.User, "加入申请群后发送 /apply 或 /start，依次填写微信、QQ 和邮箱，确认后自动开通并发送邀请邮件。\n/status 查询结果，/cancel 取消填写，/restart 重新填写。"), nil
	}
	if command == "/status" || status != "" {
		if status != "" {
			return reply(v.User, s.status(status)), nil
		}
		return reply(v.User, "还没有提交申请，发送 /apply 开始填写。"), nil
	}
	if command == "/cancel" || text == "apply_cancel" {
		reset(v)
		return reply(v.User, "已取消，未提交的资料已清除。需要时发送 /apply 重新申请。"), nil
	}
	if slices.Contains([]string{"/start", "/apply", "/restart"}, command) || text == "apply_restart" {
		if e = s.Auth.LimitIn(ctx, tx, "tg-start:"+strconv.FormatInt(v.User, 10), 10, 3600); e != nil {
			return nil, e
		}
		var group *int64
		match := regexp.MustCompile(`(?i)^/start\s+apply_(-[0-9]+)$`).FindStringSubmatch(text)
		if match != nil {
			g, err := strconv.ParseInt(match[1], 10, 64)
			if err == nil {
				ok, err := s.Client.member(ctx, g, v.User)
				if err != nil {
					return nil, fault.New(503, "暂时无法验证群成员资格，请稍后发送 /apply 重试。")
				}
				if ok {
					group = &g
				}
			}
		} else {
			group, e = s.Client.applicant(ctx, v.User, v.Group)
			if e != nil {
				return nil, fault.New(503, "暂时无法验证群成员资格，请稍后发送 /apply 重试。")
			}
		}
		if group == nil {
			return reply(v.User, "请先加入申请群，再发送 /apply 申请，也可点击群里的“申请代理”按钮。"), nil
		}
		reset(v)
		v.Group = group
		v.Step = "wechat"
		v.Expires = time.Now().UnixMilli() + 3600000
		return reply(v.User, "申请 shop.example.invalid 代理\n微信和 QQ 会保存为商店联系方式并向客户展示；邮箱用于开户、邀请邮件和登录。管理员可查看申请资料。\n\n第 1/3 步：请输入微信号（不是昵称）。\n可用 /cancel 取消；1 小时未完成需重新申请。"), nil
	}
	if v.Expires <= time.Now().UnixMilli() || v.Step == "idle" {
		reset(v)
		return reply(v.User, "发送 /apply 开始申请；填写已过期时可用 /restart 重填。"), nil
	}
	if callback && text != "apply_confirm:"+strconv.FormatInt(v.Expires, 10) {
		return reply(v.User, "这个按钮已失效，请使用最新的确认按钮。"), nil
	}
	switch v.Step {
	case "wechat":
		if callback || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{5,19}$`).MatchString(text) {
			return reply(v.User, "请输入有效微信号：6–20 位，以字母开头，支持字母、数字、下划线和减号。"), nil
		}
		v.Wechat = text
		v.Step = "qq"
		return reply(v.User, "第 2/3 步：请输入 QQ 号（5–12 位数字）。"), nil
	case "qq":
		if callback || !regexp.MustCompile(`^[1-9][0-9]{4,11}$`).MatchString(text) {
			return reply(v.User, "QQ 号需为 5–12 位数字，不能以 0 开头。"), nil
		}
		v.QQ = text
		v.Step = "email"
		return reply(v.User, "第 3/3 步：请输入收件邮箱，也是以后登录代理工作台的邮箱。"), nil
	case "email":
		email, err := auth.Email(text)
		if err != nil || callback {
			return reply(v.User, "邮箱格式不正确，请重新输入。"), nil
		}
		v.Email = email
		v.Step = "confirm"
		return reply(v.User, "请核对申请资料：\n微信："+v.Wechat+"\nQQ："+v.QQ+"\n邮箱："+v.Email+"\n\n点击“确认并开通”，同意使用以上资料开户，在商店公开展示微信和 QQ，并向该邮箱发送邀请邮件。", buttons(v)...), nil
	case "confirm":
		if !callback || text != "apply_confirm:"+strconv.FormatInt(v.Expires, 10) {
			return reply(v.User, "请点击“确认并开通”，需要修改可点“重新填写”。", buttons(v)...), nil
		}
		if v.Group == nil {
			return nil, errors.New("missing applicant group")
		}
		ok, e := s.Client.member(ctx, *v.Group, v.User)
		if e != nil {
			return nil, fault.New(503, "暂时无法验证群成员资格，请稍后确认。")
		}
		if !ok {
			return reply(v.User, "你已不在申请群，暂不能开通。请加入群后重新确认。", buttons(v)...), nil
		}
		if s.Sender == nil {
			return nil, fault.New(503, "邮件服务尚未启用，请稍后申请。")
		}
		for _, limit := range []struct {
			key string
			max int
		}{{"tg-confirm:" + strconv.FormatInt(v.User, 10), 5}, {"tg-email:" + v.Email, 3}, {"tg-create-global", 100}} {
			if e = s.Auth.LimitIn(ctx, tx, limit.key, limit.max, 3600); e != nil {
				return nil, e
			}
		}
		id, _, e := management.ProvisionPartner(ctx, tx, auth.Principal{Role: "system", Subject: "telegram:" + strconv.FormatInt(v.User, 10)}, v.Email, "代理商店")
		if e != nil {
			if fault.Status(e) == 409 {
				reset(v)
				return reply(v.User, fault.Message(e)), nil
			}
			return nil, e
		}
		now := time.Now().UnixMilli()
		if _, e = tx.Exec(ctx, `INSERT INTO telegram_partner_applications(id,user_id,group_id,partner_id,wechat,qq,email,created_at,next_attempt_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, security.ID(), v.User, *v.Group, id, v.Wechat, v.QQ, v.Email, now); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO support_contacts(owner,wechat,qq,email,version) VALUES($1,$2,$3,'',1)`, id, v.Wechat, v.QQ); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail) VALUES($1,'system',$2,'support-contacts',$3,$4)`, security.ID(), "telegram:"+strconv.FormatInt(v.User, 10), id, json.RawMessage(`{"source":"telegram-application"}`)); e != nil {
			return nil, e
		}
		reset(v)
		v.Step = "submitted"
		return reply(v.User, "代理已开通，邀请邮件正在发送。稍后会在这里通知你。\n可用 /status 查询结果。"), nil
	}
	return reply(v.User, "请使用 /status 查询申请结果。"), nil
}

type User struct {
	ID  int64 `json:"id"`
	Bot bool  `json:"is_bot"`
}
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type Message struct {
	ID                int64           `json:"message_id"`
	Text              string          `json:"text"`
	Caption           string          `json:"caption"`
	From              *User           `json:"from"`
	Chat              Chat            `json:"chat"`
	SenderChat        *Chat           `json:"sender_chat"`
	SupergroupCreated bool            `json:"supergroup_chat_created"`
	ChannelCreated    bool            `json:"channel_chat_created"`
	TopicCreated      json.RawMessage `json:"forum_topic_created"`
}
type Update struct {
	ID       *int64   `json:"update_id"`
	Message  *Message `json:"message"`
	Edited   *Message `json:"edited_message"`
	Callback *struct {
		ID      string   `json:"id"`
		Data    string   `json:"data"`
		From    User     `json:"from"`
		Message *Message `json:"message"`
	} `json:"callback_query"`
}

func (s *Service) Webhook(ctx context.Context, r *http.Request) (any, error) {
	if s.Client == nil {
		return nil, fault.New(503, "机器人尚未配置。")
	}
	supplied := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if len(r.Header.Values("X-Telegram-Bot-Api-Secret-Token")) != 1 || !security.Equal(supplied, s.Client.Secret) {
		return nil, fault.New(403, "验证失败。")
	}
	if r.Method == "GET" {
		return map[string]any{"ready": s.Sender != nil, "groups": s.Client.Groups, "registration_enabled": !s.Client.RegistrationClosed}, nil
	}
	if r.Method != "POST" {
		return nil, fault.New(405, "不支持此方法。")
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return nil, fault.New(415, "格式不正确。")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 262145))
	var u Update
	if e != nil || len(b) > 262144 || !utf8.Valid(b) || json.Unmarshal(b, &u) != nil || u.ID == nil || *u.ID < 0 || *u.ID > 9007199254740991 {
		return nil, fault.New(400, "更新格式无效或过长。")
	}
	ok := map[string]any{"ok": true}
	if handled, err := s.moderate(ctx, &u, b); handled || err != nil {
		return ok, err
	}
	if len(b) > 16384 {
		return nil, fault.New(400, "更新格式无效或过长。")
	}
	message := u.Message
	var from *User
	text := ""
	if message != nil {
		from = message.From
		text = message.Text
	} else if u.Callback != nil {
		message = u.Callback.Message
		from = &u.Callback.From
		text = u.Callback.Data
	}
	if message == nil || from == nil || from.Bot || from.ID <= 0 || from.ID > 9007199254740991 || message.Chat.ID < -9007199254740991 || message.Chat.ID > 9007199254740991 {
		return ok, nil
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 512 {
		return ok, nil
	}
	chat := message.Chat
	if chat.Type == "private" && chat.ID != from.ID {
		return ok, nil
	}
	command := regexp.MustCompile(`(?i)^/(apply|start|help|status|cancel|restart|groupid)(?:@([A-Za-z0-9_]+))?(?:\s|$)`).FindStringSubmatch(text)
	if command != nil && command[2] != "" && !strings.EqualFold(command[2], s.Client.Username) {
		return ok, nil
	}
	if chat.Type != "private" && (!slices.Contains([]string{"group", "supergroup"}, chat.Type) || !slices.Contains(s.Client.Groups, chat.ID) || command == nil || !slices.Contains([]string{"apply", "start", "help", "groupid"}, strings.ToLower(command[1]))) {
		return ok, nil
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SET LOCAL lock_timeout='1000ms'`); e != nil {
		return nil, e
	}
	now := time.Now().UnixMilli()
	digest := security.Hash(string(b))
	if _, e = tx.Exec(ctx, `INSERT INTO telegram_partner_updates(update_id,created_at,body_hash) VALUES($1,$2,$3) ON CONFLICT(update_id) DO NOTHING`, *u.ID, now, digest); e != nil {
		return nil, e
	}
	var saved, original *string
	if e = tx.QueryRow(ctx, `SELECT reply,body_hash FROM telegram_partner_updates WHERE update_id=$1 FOR UPDATE`, *u.ID).Scan(&saved, &original); e != nil {
		return nil, e
	}
	if original != nil && *original != digest {
		return nil, fault.New(409, "更新编号已绑定其他内容。")
	}
	var response *Reply
	if saved != nil {
		if e = json.Unmarshal([]byte(*saved), &response); e != nil {
			return nil, e
		}
	} else {
		if chat.Type != "private" {
			if strings.EqualFold(command[1], "groupid") {
				response = reply(chat.ID, "本群 ID："+strconv.FormatInt(chat.ID, 10))
			} else if s.Client.RegistrationClosed {
				response = reply(chat.ID, registrationPaused)
			} else {
				response = reply(chat.ID, "申请 shop.example.invalid 代理：点击按钮，在私聊中填写资料并确认。", []map[string]string{{"text": "申请代理", "url": "https://t.me/" + s.Client.Username + "?start=apply_" + strconv.FormatInt(chat.ID, 10)}})
			}
		} else {
			if _, e = tx.Exec(ctx, `INSERT INTO telegram_partner_sessions(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING`, from.ID); e != nil {
				return nil, e
			}
			v := session{User: from.ID}
			if e = tx.QueryRow(ctx, `SELECT group_id,step,wechat,qq,email,expires_at,last_update_id FROM telegram_partner_sessions WHERE user_id=$1 FOR UPDATE`, from.ID).Scan(&v.Group, &v.Step, &v.Wechat, &v.QQ, &v.Email, &v.Expires, &v.Last); e != nil {
				return nil, e
			}
			if *u.ID > v.Last {
				if command != nil && command[2] != "" {
					text = regexp.MustCompile(`@[A-Za-z0-9_]+(\s|$)`).ReplaceAllString(text, "$1")
				}
				e = s.Auth.LimitIn(ctx, tx, "tg-message:"+strconv.FormatInt(from.ID, 10), 30, 60)
				if e == nil {
					response, e = s.plan(ctx, tx, &v, text, u.Callback != nil)
				}
				if e != nil {
					var expected *fault.Error
					if errors.As(e, &expected) && (expected.Status == 429 || expected.Status == 503) {
						response = reply(from.ID, fault.Message(e))
					} else {
						return nil, e
					}
				}
				if _, e = tx.Exec(ctx, `UPDATE telegram_partner_sessions SET group_id=$1,step=$2,wechat=$3,qq=$4,email=$5,expires_at=$6,last_update_id=$7,lock_token=NULL,lock_until=0 WHERE user_id=$8`, v.Group, v.Step, v.Wechat, v.QQ, v.Email, v.Expires, *u.ID, from.ID); e != nil {
					return nil, e
				}
			}
		}
		data, e := json.Marshal(response)
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE telegram_partner_updates SET reply=$1,lock_token=NULL,lock_until=0 WHERE update_id=$2`, string(data), *u.ID); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if u.Callback != nil && len(u.Callback.ID) <= 256 {
		_ = s.Client.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, nil)
	}
	if response != nil {
		if e = s.Client.Call(ctx, "sendMessage", response, nil); e != nil {
			return nil, e
		}
	}
	return ok, nil
}

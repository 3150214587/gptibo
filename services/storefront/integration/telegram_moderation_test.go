package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/telegram"
)

type botTransport func(*http.Request) (*http.Response, error)

func (f botTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type moderationFixture struct {
	service telegram.Service
	calls   []struct {
		method string
		data   map[string]any
	}
	permissions map[string]bool
	fail        string
	rights      bool
}

func botFixture(t *testing.T) *moderationFixture {
	_, app := fixture(t, true)
	client, err := telegram.NewClient("999:test-only", "ExampleBot", strings.Repeat("a", 64), []int64{-10042, -10043})
	if err != nil {
		t.Fatal(err)
	}
	f := &moderationFixture{rights: true, permissions: map[string]bool{"can_send_messages": true, "can_send_photos": true, "can_send_polls": false, "can_invite_users": true, "can_pin_messages": false}}
	client.HTTP = &http.Client{Transport: botTransport(func(r *http.Request) (*http.Response, error) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.calls = append(f.calls, struct {
			method string
			data   map[string]any
		}{method, data})
		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{"id": 999}
		case "getChatMember":
			status := "member"
			if data["user_id"] == float64(101) || data["user_id"] == float64(999) {
				status = "administrator"
			}
			if data["user_id"] == float64(102) {
				status = "creator"
			}
			result = map[string]any{"status": status, "can_restrict_members": f.rights, "can_delete_messages": f.rights}
		case "getChat":
			result = map[string]any{"permissions": f.permissions}
		case "setChatPermissions":
			f.permissions = map[string]bool{}
			for k, v := range data["permissions"].(map[string]any) {
				f.permissions[k] = v.(bool)
			}
		}
		// setChatPermissions can succeed remotely even when its response is lost.
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", "application/json")
		if f.fail == method {
			response.WriteHeader(503)
			response.WriteString(`{"ok":false}`)
		} else {
			_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "result": result})
		}
		return response.Result(), nil
	})}
	f.service = telegram.Service{Store: app, Client: client, Auth: &auth.Service{Store: app}, Origin: "https://example.test"}
	return f
}
func (f *moderationFixture) send(id int64, kind string, message map[string]any) error {
	data, _ := json.Marshal(map[string]any{"update_id": id, kind: message})
	request := httptest.NewRequest("POST", "/api/telegram/webhook", strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", f.service.Client.Secret)
	_, err := f.service.Webhook(context.Background(), request)
	return err
}
func groupMessage(id int64, text string, user int64) map[string]any {
	return map[string]any{"message_id": id, "text": text, "from": map[string]any{"id": user}, "chat": map[string]any{"id": -10042, "type": "supergroup"}}
}
func (f *moderationFixture) count(method string) int {
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}
func (f *moderationFixture) active(t *testing.T) bool {
	t.Helper()
	var active bool
	err := f.service.Store.DB.QueryRow(context.Background(), `SELECT COALESCE((SELECT muted OR target_muted FROM telegram_group_moderation WHERE group_id=-10042),false)`).Scan(&active)
	if err != nil {
		t.Fatal(err)
	}
	return active
}
func TestTelegramMuteRestoresPermissionsAndDeduplicatesCommands(t *testing.T) {
	f := botFixture(t)
	original := map[string]bool{}
	for k, v := range f.permissions {
		original[k] = v
	}
	for _, id := range []int64{1, 1, 2} {
		if err := f.send(id, "message", groupMessage(id, "/mute@ExampleBot", 101)); err != nil {
			t.Fatal(err)
		}
	}
	if !f.active(t) || f.count("setChatPermissions") != 2 || f.count("getChat") != 1 {
		t.Fatal("mute did not persist or duplicate overwrote original permissions")
	}
	for _, field := range []string{"can_send_messages", "can_send_photos", "can_send_other_messages", "can_send_voice_notes", "can_send_polls"} {
		if f.permissions[field] {
			t.Fatal("unmuted content type", field)
		}
	}
	if !f.permissions["can_invite_users"] {
		t.Fatal("unrelated permissions changed")
	}
	if err := f.send(3, "message", groupMessage(3, "/unmute", 102)); err != nil {
		t.Fatal(err)
	}
	if f.active(t) || !reflect.DeepEqual(f.permissions, original) {
		t.Fatal("original permissions were not restored", f.permissions)
	}
	// A redelivered completed mute must never undo a later unmute.
	if err := f.send(1, "message", groupMessage(1, "/mute@ExampleBot", 101)); err != nil {
		t.Fatal(err)
	}
	if f.active(t) || f.count("setChatPermissions") != 3 {
		t.Fatal("old command reversed new state")
	}
	for _, c := range f.calls {
		if c.method == "setChatPermissions" && c.data["use_independent_chat_permissions"] != true {
			t.Fatal("implicit permission expansion")
		}
	}
}
func TestTelegramMuteDeletesBypassesIncludingMediaEditsChannelsAndLongBodies(t *testing.T) {
	f := botFixture(t)
	if err := f.send(1, "message", groupMessage(1, "/mute", 101)); err != nil {
		t.Fatal(err)
	}
	cases := []map[string]any{groupMessage(2, "bypass", 202), groupMessage(3, "/unmute", 202), groupMessage(4, "/apply@OtherBot", 202), groupMessage(5, strings.Repeat("字", 10000), 202)}
	for _, field := range []string{"photo", "sticker", "voice", "video", "document", "poll", "dice", "rich_message"} {
		m := groupMessage(int64(len(cases)+2), "", 202)
		delete(m, "text")
		m[field] = map[string]any{"test": true}
		cases = append(cases, m)
	}
	channel := groupMessage(20, "/unmute", 101)
	channel["sender_chat"] = map[string]any{"id": -900, "type": "channel"}
	cases = append(cases, channel)
	bot := groupMessage(21, "other bot", 333)
	bot["from"] = map[string]any{"id": 333, "is_bot": true}
	cases = append(cases, bot)
	missing := groupMessage(22, "no from", 202)
	delete(missing, "from")
	cases = append(cases, missing)
	for i, m := range cases {
		kind := "message"
		if i%2 == 1 {
			kind = "edited_message"
		}
		if err := f.send(int64(100+i), kind, m); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("deleteMessages") != len(cases) || !f.active(t) {
		t.Fatal("bypass escaped moderation")
	}
	allowed := []map[string]any{groupMessage(30, "notice", 101), groupMessage(31, "owner notice", 102)}
	anon := groupMessage(32, "notice", 1087968824)
	anon["sender_chat"] = map[string]any{"id": -10042, "type": "supergroup"}
	allowed = append(allowed, anon)
	own := groupMessage(33, "notice", 999)
	own["from"] = map[string]any{"id": 999, "is_bot": true}
	allowed = append(allowed, own)
	other := groupMessage(34, "other group", 202)
	other["chat"] = map[string]any{"id": -123, "type": "supergroup"}
	allowed = append(allowed, other)
	unmuted := groupMessage(35, "second group", 202)
	unmuted["chat"] = map[string]any{"id": -10043, "type": "supergroup"}
	allowed = append(allowed, unmuted)
	private := groupMessage(36, "/help", 202)
	private["chat"] = map[string]any{"id": 202, "type": "private"}
	allowed = append(allowed, private)
	for i, m := range allowed {
		if err := f.send(int64(200+i), "message", m); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("deleteMessages") != len(cases) {
		t.Fatal("administrator, private chat or unrelated group message deleted")
	}
	for _, c := range f.calls {
		if c.method == "deleteMessages" && c.data["chat_id"] != float64(-10042) {
			t.Fatal("cross-group deletion")
		}
	}
}
func TestTelegramMuteChecksRolesAndBotRightsBeforeChangingPermissions(t *testing.T) {
	f := botFixture(t)
	for i, user := range []int64{202, 101} {
		f.rights = false
		if err := f.send(int64(i+1), "message", groupMessage(int64(i+1), "/mute", user)); err != nil {
			t.Fatal(err)
		}
		if f.active(t) || f.count("setChatPermissions") != 0 {
			t.Fatal("unauthorized mute")
		}
	}
	f.rights = true
	f.fail = "getChatMember"
	if err := f.send(3, "message", groupMessage(3, "/mute", 101)); err == nil {
		t.Fatal("lookup failure acknowledged")
	}
	if f.active(t) {
		t.Fatal("role API failure granted control")
	}
	f.fail = ""
	if err := f.send(4, "message", groupMessage(4, "/mute", 101)); err != nil {
		t.Fatal(err)
	}
	f.fail = "deleteMessages"
	message := groupMessage(5, "leak", 202)
	if err := f.send(5, "message", message); err == nil {
		t.Fatal("failed deletion acknowledged")
	}
	f.fail = ""
	if err := f.send(5, "message", message); err != nil {
		t.Fatal(err)
	}
	if f.count("deleteMessages") != 2 {
		t.Fatal("deletion did not retry")
	}
}
func TestTelegramMuteSurvivesUncertainAPICallsAndSupersededRetries(t *testing.T) {
	f := botFixture(t)
	original := map[string]bool{}
	for k, v := range f.permissions {
		original[k] = v
	}
	f.fail = "setChatPermissions"
	if err := f.send(1, "message", groupMessage(1, "/mute", 101)); err == nil {
		t.Fatal("uncertain mutation acknowledged")
	}
	if !f.active(t) {
		t.Fatal("durable deletion guard lost")
	}
	f.fail = ""
	if err := f.send(1, "message", groupMessage(1, "/mute", 101)); err != nil {
		t.Fatal(err)
	}
	if f.count("getChat") != 1 {
		t.Fatal("retry captured muted permissions as original")
	}
	f.fail = "setChatPermissions"
	if err := f.send(2, "message", groupMessage(2, "/unmute", 101)); err == nil {
		t.Fatal("uncertain unmute acknowledged")
	}
	if !f.active(t) {
		t.Fatal("deletion guard disabled before successful unmute")
	}
	f.fail = ""
	if err := f.send(3, "message", groupMessage(3, "/mute", 101)); err != nil {
		t.Fatal(err)
	}
	before := f.count("setChatPermissions")
	if err := f.send(2, "message", groupMessage(2, "/unmute", 101)); err != nil {
		t.Fatal(err)
	}
	if !f.active(t) || f.count("setChatPermissions") != before {
		t.Fatal("superseded failed unmute changed newer setting")
	}
	if err := f.send(4, "message", groupMessage(4, "/unmute", 101)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.permissions, original) {
		t.Fatal("snapshot lost through uncertain requests")
	}
	f.fail = "sendMessage"
	if err := f.send(5, "message", groupMessage(5, "/mute", 101)); err == nil {
		t.Fatal("failed confirmation acknowledged")
	}
	before = f.count("setChatPermissions")
	f.fail = ""
	if err := f.send(5, "message", groupMessage(5, "/mute", 101)); err != nil {
		t.Fatal(err)
	}
	if f.count("setChatPermissions") != before {
		t.Fatal("reply retry repeated mutation")
	}
	// Ensure test transport never leaves unread network data or uses real Telegram.
	for _, c := range f.calls {
		if c.method == "sendMessage" && c.data["chat_id"] != float64(-10042) {
			t.Fatal("unexpected outbound chat")
		}
	}
}

package integration_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func (f *moderationFixture) blockedWords(t *testing.T) []string {
	t.Helper()
	var words []string
	if err := f.service.Store.DB.QueryRow(context.Background(), `SELECT COALESCE((SELECT blocked_words FROM telegram_group_moderation WHERE group_id=-10042),'{}'::text[])`).Scan(&words); err != nil {
		t.Fatal(err)
	}
	return words
}

func TestTelegramBlockedWordsCommandScopeAndLimits(t *testing.T) {
	f := botFixture(t)
	commands := []string{"/blockword", "/blockword " + strings.Repeat("字", 33), "/blockword nft\nsol", "/blockword@OtherBot sol", "/blockwords sol"}
	for i, command := range commands {
		if err := f.send(int64(i+1), "message", groupMessage(int64(i+1), command, 101)); err != nil || len(f.blockedWords(t)) != 0 {
			t.Fatal("invalid command changed words", command, err)
		}
	}
	m := groupMessage(10, "/blockword nft", 101)
	m["sender_chat"] = map[string]any{"id": -900, "type": "channel"}
	if err := f.send(10, "message", m); err != nil || len(f.blockedWords(t)) != 0 {
		t.Fatal("channel inherited admin identity", err)
	}
	if err := f.send(11, "edited_message", groupMessage(11, "/blockword nft", 101)); err != nil || len(f.blockedWords(t)) != 0 {
		t.Fatal("edited command changed words", err)
	}
	m = groupMessage(12, "/blockword NFT", 1087968824)
	m["sender_chat"] = map[string]any{"id": -10042, "type": "supergroup"}
	if err := f.send(12, "message", m); err != nil || !reflect.DeepEqual(f.blockedWords(t), []string{"nft"}) {
		t.Fatal("anonymous administrator could not configure", err)
	}
	if err := f.send(13, "message", groupMessage(13, "/unblockword nft", 202)); err != nil || f.count("deleteMessages") != 1 || len(f.blockedWords(t)) != 1 {
		t.Fatal("member command bypassed filter", err)
	}
	words := []string{}
	for i := range 100 {
		words = append(words, fmt.Sprintf("%02d%s", i, strings.Repeat("😀", 15)))
	}
	if _, err := f.service.Store.DB.Exec(context.Background(), `UPDATE telegram_group_moderation SET blocked_words=$1 WHERE group_id=-10042`, words); err != nil {
		t.Fatal(err)
	}
	if err := f.send(14, "message", groupMessage(14, "/blockword sol", 101)); err != nil || len(f.blockedWords(t)) != 100 {
		t.Fatal("word limit not enforced", err)
	}
	if err := f.send(15, "message", groupMessage(15, "/blockwords", 101)); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if last.method != "sendMessage" || len(utf16.Encode([]rune(last.data["text"].(string)))) > 4096 {
		t.Fatal("word list exceeds Telegram message limit")
	}
}

func TestTelegramBlockedWordsDeleteTextCaptionsEditsAndChannelMessages(t *testing.T) {
	f := botFixture(t)
	for i, word := range []string{"nft", "solana", "mint", "sol"} {
		if err := f.send(int64(i+1), "message", groupMessage(int64(i+1), "/blockword "+word, 101)); err != nil {
			t.Fatal(err)
		}
	}
	f.calls = nil
	for i := range 6 {
		m := groupMessage(int64(20+i), "buy $SOL", 202)
		kind := "message"
		switch i {
		case 0:
			m["text"] = "ＭＩＮＴ now"
		case 1:
			delete(m, "text")
			m["caption"] = "new NFT"
		case 2:
			kind = "edited_message"
		case 3:
			m["sender_chat"] = map[string]any{"id": -900, "type": "channel"}
			delete(m, "from")
		case 4:
			m["from"] = map[string]any{"id": 101}
		case 5:
			m["text"] = strings.Repeat("字", 10000) + " Solana"
		}
		if err := f.send(int64(20+i), kind, m); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("deleteMessages") != 6 || f.count("sendMessage") != 0 || f.count("getChatMember") != 0 {
		t.Fatal("matching messages were not silently deleted", f.calls)
	}
	for i := range 4 {
		m := groupMessage(int64(40+i), "NFT", 202)
		switch i {
		case 0:
			m["text"] = "solution console minting"
		case 1:
			m["chat"] = map[string]any{"id": -10043, "type": "supergroup"}
		case 2:
			m["chat"] = map[string]any{"id": -9999, "type": "supergroup"}
		case 3:
			m["from"] = map[string]any{"id": 999, "is_bot": true}
		}
		if err := f.send(int64(40+i), "message", m); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("deleteMessages") != 6 {
		t.Fatal("deleted a nonmatching or out-of-scope message")
	}
	if err := f.send(50, "message", groupMessage(50, "/unblockword SOL", 101)); err != nil {
		t.Fatal(err)
	}
	if err := f.send(51, "message", groupMessage(51, "SOL", 202)); err != nil || f.count("deleteMessages") != 6 {
		t.Fatal("removed word still applied", err)
	}
}

func TestTelegramBlockedWordsAuthorizationPersistenceAndRetries(t *testing.T) {
	f := botFixture(t)
	for i, user := range []int64{202, 101} {
		f.rights = false
		if err := f.send(int64(i+1), "message", groupMessage(int64(i+1), "/blockword sol", user)); err != nil || len(f.blockedWords(t)) != 0 {
			t.Fatal("unauthorized change", err)
		}
	}
	f.rights = true
	f.fail = "getChatMember"
	if err := f.send(3, "message", groupMessage(3, "/blockword sol", 101)); err == nil {
		t.Fatal("role lookup failure allowed configuration")
	}
	f.fail = "sendMessage"
	m := groupMessage(4, "/blockword@ExampleBot SOL", 101)
	if err := f.send(4, "message", m); err == nil || !reflect.DeepEqual(f.blockedWords(t), []string{"sol"}) {
		t.Fatal("configuration not durable before reply", err)
	}
	f.fail = ""
	if err := f.send(4, "message", m); err != nil {
		t.Fatal(err)
	}
	if err := f.send(4, "message", groupMessage(4, "/blockword nft", 101)); err == nil {
		t.Fatal("update ID reused with another body")
	}
	if err := f.send(6, "message", groupMessage(6, "/unblockword sol", 102)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{4, 5} {
		msg := m
		if id == 5 {
			msg = groupMessage(5, "/blockword sol", 101)
		}
		if err := f.send(id, "message", msg); err != nil || len(f.blockedWords(t)) != 0 {
			t.Fatal("old delivery restored removed word", err)
		}
	}
	if err := f.send(7, "message", groupMessage(7, "/blockword nft", 101)); err != nil {
		t.Fatal(err)
	}
	f.fail = "deleteMessages"
	m = groupMessage(8, "NFT", 202)
	if err := f.send(8, "edited_message", m); err == nil {
		t.Fatal("failed delete acknowledged")
	}
	f.fail = ""
	if err := f.send(8, "edited_message", m); err != nil || f.count("deleteMessages") != 2 {
		t.Fatal("delete not retried", err)
	}
	for i, text := range []string{"/mute", "/blockwords", "/unmute"} {
		if err := f.send(int64(10+i), "message", groupMessage(int64(10+i), text, 101)); err != nil {
			t.Fatal(err)
		}
	}
	if f.active(t) || !reflect.DeepEqual(f.blockedWords(t), []string{"nft"}) {
		t.Fatal("mute/unmute damaged blocked words")
	}
}

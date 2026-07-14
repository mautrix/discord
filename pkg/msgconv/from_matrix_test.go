package msgconv

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func TestToDiscordRelayedTextKeepsRestrictiveAllowedMentions(t *testing.T) {
	mc := NewMessageConverter(nil)
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "@everyone <@123456789012345678>",
	}
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:      &event.Event{Content: event.Content{Raw: map[string]any{}}},
			Content:    content,
			OrigSender: &bridgev2.OrigSender{},
		},
	}

	got, err := mc.ToDiscord(context.Background(), nil, msg, "channel", nil)
	if err != nil {
		t.Fatalf("ToDiscord() failed: %v", err)
	}
	if got.AllowedMentions == nil {
		t.Fatal("expected relayed text to include restrictive allowed_mentions")
	}
	if len(got.AllowedMentions.Parse) != 0 {
		t.Fatalf("expected no mention parse types, got %#v", got.AllowedMentions.Parse)
	}
	if len(got.AllowedMentions.Users) != 0 {
		t.Fatalf("expected no explicitly allowed users, got %#v", got.AllowedMentions.Users)
	}
}

func TestToDiscordNormalTextLeavesAllowedMentionsUnset(t *testing.T) {
	mc := NewMessageConverter(nil)
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "hello",
	}
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   &event.Event{Content: event.Content{Raw: map[string]any{}}},
			Content: content,
		},
	}

	got, err := mc.ToDiscord(context.Background(), nil, msg, "channel", nil)
	if err != nil {
		t.Fatalf("ToDiscord() failed: %v", err)
	}
	if got.AllowedMentions != nil {
		t.Fatalf("expected normal puppet send to keep old nil allowed_mentions behavior, got %#v", got.AllowedMentions)
	}
}

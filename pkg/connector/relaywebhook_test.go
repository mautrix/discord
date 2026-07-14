package connector

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestChooseRelayWebhookGhostMatch(t *testing.T) {
	defaultAvatar := "https://cdn.discordapp.com/embed/avatars/0.png?size=256"
	realAvatar := "https://cdn.discordapp.com/avatars/325703750469550084/avatar.png?size=256"

	tests := []struct {
		name    string
		matches []relayWebhookGhostMatch
		wantID  string
	}{
		{
			name: "single default avatar match",
			matches: []relayWebhookGhostMatch{{
				userID:    "default",
				name:      "keith",
				avatarURL: defaultAvatar,
			}},
			wantID: "default",
		},
		{
			name: "one real avatar among webhook default avatar duplicates",
			matches: []relayWebhookGhostMatch{
				{userID: "webhook1", name: "keith", avatarURL: defaultAvatar},
				{userID: "real", name: "keith", avatarURL: realAvatar},
				{userID: "webhook2", name: "keith", avatarURL: defaultAvatar},
			},
			wantID: "real",
		},
		{
			name: "ambiguous real avatars",
			matches: []relayWebhookGhostMatch{
				{userID: "real1", name: "keith", avatarURL: realAvatar},
				{userID: "real2", name: "keith", avatarURL: strings.Replace(realAvatar, "avatar", "other", 1)},
			},
		},
		{
			name: "ambiguous default avatars",
			matches: []relayWebhookGhostMatch{
				{userID: "webhook1", name: "keith", avatarURL: defaultAvatar},
				{userID: "webhook2", name: "keith", avatarURL: defaultAvatar},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseRelayWebhookGhostMatch(tc.matches)
			if tc.wantID == "" {
				if got != nil {
					t.Fatalf("expected no match, got %#v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected match %q, got nil", tc.wantID)
			}
			if got.userID != tc.wantID {
				t.Fatalf("unexpected match: got %q, want %q", got.userID, tc.wantID)
			}
		})
	}
}

func TestPrependReplyEmbed(t *testing.T) {
	embeds := []*discordgo.MessageEmbed{{Description: "existing"}}
	got := prependReplyEmbed(embeds, "guild", "channel", "message")
	if len(got) != 2 {
		t.Fatalf("expected 2 embeds, got %d", len(got))
	}
	if !strings.Contains(got[0].Description, "https://discord.com/channels/guild/channel/message") {
		t.Fatalf("reply embed did not contain jump link: %q", got[0].Description)
	}
	if got[1] != embeds[0] {
		t.Fatal("existing embeds were not preserved after reply embed")
	}
}

func TestPrependReplyEmbedSkipsMissingTarget(t *testing.T) {
	embeds := []*discordgo.MessageEmbed{{Description: "existing"}}
	got := prependReplyEmbed(embeds, "guild", "", "message")
	if len(got) != 1 || got[0] != embeds[0] {
		t.Fatalf("expected original embeds unchanged, got %#v", got)
	}
}

func TestRelayWebhookFlagsOnlyAllowsSuppressEmbeds(t *testing.T) {
	flags := int(discordgo.MessageFlagsSuppressEmbeds | discordgo.MessageFlagsIsVoiceMessage | discordgo.MessageFlagsSuppressNotifications)
	got := relayWebhookFlags(&flags)
	if got != discordgo.MessageFlagsSuppressEmbeds {
		t.Fatalf("unexpected flags: got %d, want %d", got, discordgo.MessageFlagsSuppressEmbeds)
	}
}

func TestSanitizeRelayWebhookUsername(t *testing.T) {
	got := sanitizeRelayWebhookUsername("  Discord\nBridge  ", "@user:example.com")
	if strings.Contains(strings.ToLower(got), "discord") {
		t.Fatalf("username still contains discord: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("username still contains control character: %q", got)
	}

	long := strings.Repeat("a", 90)
	got = sanitizeRelayWebhookUsername(long, "")
	if len([]rune(got)) != 80 {
		t.Fatalf("username was not truncated to 80 runes: got %d", len([]rune(got)))
	}

	got = sanitizeRelayWebhookUsername(" \n\t ", "@user:example.com")
	if got != "@user:example.com" {
		t.Fatalf("unexpected fallback username: got %q", got)
	}
}

func TestWebhookMessageThreadURI(t *testing.T) {
	got := webhookMessageThreadURI("webhook", "token", "message", "thread")
	want := discordgo.EndpointWebhookMessage("webhook", "token", "message") + "?thread_id=thread"
	if got != want {
		t.Fatalf("unexpected URI:\n got: %s\nwant: %s", got, want)
	}
}

func TestIsRelayWebhookDiscordMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  *discordgo.Message
		want bool
	}{
		{
			name: "webhook id match",
			msg:  &discordgo.Message{WebhookID: "relay-webhook", Author: &discordgo.User{ID: "other"}},
			want: true,
		},
		{
			name: "author id match with webhook marker",
			msg:  &discordgo.Message{WebhookID: "some-webhook", Author: &discordgo.User{ID: "relay-webhook"}},
			want: true,
		},
		{
			name: "author match without webhook marker",
			msg:  &discordgo.Message{Author: &discordgo.User{ID: "relay-webhook"}},
		},
		{
			name: "different webhook",
			msg:  &discordgo.Message{WebhookID: "other", Author: &discordgo.User{ID: "other"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRelayWebhookDiscordMessage(tc.msg, "relay-webhook"); got != tc.want {
				t.Fatalf("unexpected relay webhook classification: got %t, want %t", got, tc.want)
			}
		})
	}
}

func TestIsStaleWebhookError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "unknown webhook code",
			err: &discordgo.RESTError{
				Message: &discordgo.APIErrorMessage{Code: discordgo.ErrCodeUnknownWebhook},
			},
			want: true,
		},
		{
			name: "invalid webhook token code",
			err: &discordgo.RESTError{
				Message: &discordgo.APIErrorMessage{Code: discordgo.ErrCodeInvalidWebhookTokenProvided},
			},
			want: true,
		},
		{
			name: "not found status",
			err: &discordgo.RESTError{
				Response: &http.Response{StatusCode: http.StatusNotFound},
			},
			want: true,
		},
		{
			name: "other rest error",
			err: &discordgo.RESTError{
				Response: &http.Response{StatusCode: http.StatusBadRequest},
				Message:  &discordgo.APIErrorMessage{Code: discordgo.ErrCodeInvalidFormBody},
			},
		},
		{
			name: "plain error",
			err:  errors.New("nope"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaleWebhookError(tc.err); got != tc.want {
				t.Fatalf("unexpected stale-webhook classification: got %t, want %t", got, tc.want)
			}
		})
	}
}

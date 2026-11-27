package connector

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCommandToken(t *testing.T) {
	for _, tc := range []struct{ kind, input, want string }{
		{"user", "token", "token"},
		{"BOT", "token", "Bot token"},
		{"oauth", "token", "Bearer token"},
		{"other", "token", ""},
		{"user", "", ""},
		{"bot", "Bot token", ""},
		{"user", "token\n", ""},
	} {
		got, err := commandToken(tc.kind, tc.input)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("commandToken(%q, %q) = %q, %v", tc.kind, tc.input, got, err)
		}
	}
}

func TestBotSkipsUserOnlyOperations(t *testing.T) {
	client := &DiscordClient{Session: &discordgo.Session{IsUser: false}}
	client.refreshSafetyHub(context.Background())
	client.subscribeGuild(context.Background(), "123")
	if err := client.HandleMatrixReadReceipt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

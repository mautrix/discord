package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func TestGuildModeCompatibility(t *testing.T) {
	meta := &discordid.UserLoginMetadata{
		BridgedGuildIDs: map[string]bool{"old": true, "disabled": true},
		GuildBridgingModes: map[string]discordid.GuildBridgingMode{
			"disabled":   discordid.GuildBridgeNothing,
			"configured": discordid.GuildBridgeIfPortalExists,
			"lazy":       discordid.GuildBridgeCreateOnMessage,
		},
	}
	client := &DiscordClient{connector: &DiscordConnector{}, UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: meta}}}
	client.connector.Config.Guilds.BridgingGuildIDs = []string{"configured", "disabled", "config-only"}
	for guild, want := range map[string]discordid.GuildBridgingMode{
		"old": discordid.GuildBridgeEverything, "disabled": discordid.GuildBridgeNothing,
		"configured": discordid.GuildBridgeIfPortalExists, "lazy": discordid.GuildBridgeCreateOnMessage,
		"config-only": discordid.GuildBridgeEverything, "unknown": discordid.GuildBridgeNothing,
	} {
		if got := client.guildBridgingMode(guild); got != want {
			t.Errorf("%s: got %s, want %s", guild, got, want)
		}
		_, bridged := client.bridgedGuildIDs()[guild]
		if bridged != (want != discordid.GuildBridgeNothing) {
			t.Errorf("%s: inconsistent enabled state", guild)
		}
	}
}

func TestLegacyBridgeFlags(t *testing.T) {
	for _, tc := range []struct {
		args                      []string
		replace, deleteOld, valid bool
	}{
		{[]string{"123"}, false, false, true},
		{[]string{"--replace", "123"}, true, false, true},
		{[]string{"123", "--replace=delete"}, true, true, true},
		{[]string{"123", "456"}, false, false, false},
		{[]string{"--replace"}, false, false, false},
		{[]string{"--bogus", "123"}, false, false, false},
	} {
		channel, replace, deleteOld, err := parseBridgeArgs(tc.args)
		if (err == nil) != tc.valid {
			t.Errorf("%v: unexpected error %v", tc.args, err)
		}
		if tc.valid && (channel != "123" || replace != tc.replace || deleteOld != tc.deleteOld) {
			t.Errorf("%v: wrong replacement behavior", tc.args)
		}
	}
}

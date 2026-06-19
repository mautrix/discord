// Connector-level ID helpers (ar M3). These are separate from the pure
// pkg/discordid codec: they operate on discordgo types and produce bridgev2
// networkid values, following the mautrix-slack convention.
//
// Group 1 scaffolding: stubs returning zero values.
package connector

import (
	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// makePortalKey builds the PortalKey for a discordgo channel, setting the
// receiver only for DMs/group DMs.
func makePortalKey(ch *discordgo.Channel) networkid.PortalKey {
	// TODO(group3)
	return networkid.PortalKey{}
}

// makeGuildPortalKey builds the PortalKey for a guild-space portal.
func makeGuildPortalKey(guildID string) networkid.PortalKey {
	// TODO(group3)
	return networkid.PortalKey{}
}

// makeEventSender builds an EventSender for a Discord user snowflake.
func makeEventSender(userID string) bridgev2.EventSender {
	// TODO(group3)
	return bridgev2.EventSender{}
}

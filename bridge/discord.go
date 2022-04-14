package bridge

import (
	"github.com/bwmarrin/discordgo"
)

func channelIsBridgeable(channel *discordgo.Channel) bool {
	switch channel.Type {
	case discordgo.ChannelTypeGuildText:
		fallthrough
	case discordgo.ChannelTypeGuildNews:
		return true
	}

	return false
}

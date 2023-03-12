package main

import (
	"errors"

	"github.com/bwmarrin/discordgo"
)

func (user *User) channelIsBridgeable(channel *discordgo.Channel) bool {
	switch channel.Type {
	case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews:
		// allowed
	case discordgo.ChannelTypeDM, discordgo.ChannelTypeGroupDM:
		// DMs are always bridgeable, no need for permission checks
		return true
	default:
		// everything else is not allowed
		return false
	}

	log := user.log.With().Str("guild_id", channel.GuildID).Str("channel_id", channel.ID).Logger()

	member, err := user.Session.State.Member(channel.GuildID, user.DiscordID)
	if errors.Is(err, discordgo.ErrStateNotFound) {
		log.Debug().Msg("Fetching own membership in guild to check roles")
		member, err = user.Session.GuildMember(channel.GuildID, user.DiscordID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get own membership in guild from server")
		} else {
			err = user.Session.State.MemberAdd(member)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to add own membership in guild to cache")
			}
		}
	} else if err != nil {
		log.Warn().Err(err).Msg("Failed to get own membership in guild from cache")
	}
	err = user.Session.State.ChannelAdd(channel)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to add channel to cache")
	}
	perms, err := user.Session.State.UserChannelPermissions(user.DiscordID, channel.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get permissions in channel to determine if it's bridgeable")
		return true
	}
	log.Debug().
		Int64("permissions", perms).
		Bool("view_channel", perms&discordgo.PermissionViewChannel > 0).
		Msg("Computed permissions in channel")
	return perms&discordgo.PermissionViewChannel > 0
}

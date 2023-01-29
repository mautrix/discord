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

	member, err := user.Session.State.Member(channel.GuildID, user.DiscordID)
	if errors.Is(err, discordgo.ErrStateNotFound) {
		user.log.Debugfln("Fetching own membership in %s to check own roles", channel.GuildID)
		member, err = user.Session.GuildMember(channel.GuildID, user.DiscordID)
		if err != nil {
			user.log.Warnfln("Failed to get own membership in %s from server to determine own roles for bridging %s: %v", channel.GuildID, channel.ID, err)
		} else {
			err = user.Session.State.MemberAdd(member)
			if err != nil {
				user.log.Warnfln("Failed to add own membership in %s to cache: %v", channel.GuildID, err)
			}
		}
	} else if err != nil {
		user.log.Warnfln("Failed to get own membership in %s from cache to determine own roles for bridging %s: %v", channel.GuildID, channel.ID, err)
	}
	err = user.Session.State.ChannelAdd(channel)
	if err != nil {
		user.log.Warnfln("Failed to add channel %s/%s to cache: %v", channel.GuildID, channel.ID, err)
	}
	perms, err := user.Session.State.UserChannelPermissions(user.DiscordID, channel.ID)
	if err != nil {
		user.log.Warnfln("Failed to get permissions in %s/%s to determine if it's bridgeable: %v", channel.GuildID, channel.ID, err)
		return true
	}
	user.log.Debugfln("Computed permissions in %s/%s: %d (view channel: %t)", channel.GuildID, channel.ID, perms, perms&discordgo.PermissionViewChannel > 0)
	return perms&discordgo.PermissionViewChannel > 0
}

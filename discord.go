package main

import (
	"encoding/json"
	"errors"

	"github.com/bwmarrin/discordgo"
)

func ptrBool(val bool) *bool {
	return &val
}

func (user *User) channelIsBridgeable(channel *discordgo.Channel) bool {
	switch channel.Type {
	case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews:
		// allowed
	default:
		// everything else is not allowed
		return false
	}

	hasRole := map[string]bool{
		channel.GuildID: true,
	}
	var roles []string
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
	if member != nil {
		roles = member.Roles
		for _, role := range member.Roles {
			hasRole[role] = true
		}
	}
	var userAllowed, roleAllowed *bool
	for _, override := range channel.PermissionOverwrites {
		if override.Type == discordgo.PermissionOverwriteTypeMember && override.ID == user.DiscordID {
			if override.Allow&discordgo.PermissionViewChannel > 0 {
				userAllowed = ptrBool(true)
			} else if override.Deny&discordgo.PermissionViewChannel > 0 {
				userAllowed = ptrBool(false)
			}
		} else if override.Type == discordgo.PermissionOverwriteTypeRole && hasRole[override.ID] {
			if override.Allow&discordgo.PermissionViewChannel > 0 {
				roleAllowed = ptrBool(true)
			} else if override.Deny&discordgo.PermissionViewChannel > 0 {
				roleAllowed = ptrBool(false)
			}
		}
	}
	allowed := true
	if userAllowed != nil {
		allowed = *userAllowed
	} else if roleAllowed != nil {
		allowed = *roleAllowed
	}
	if !allowed {
		dat, _ := json.Marshal(channel.PermissionOverwrites)
		user.log.Debugfln("Permission overwrites (%s) resulted in %s/%s not being allowed to bridge with roles %+v", dat, channel.GuildID, channel.ID, roles)
	}
	return allowed
}

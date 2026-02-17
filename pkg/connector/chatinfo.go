// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// getGuildSpaceInfo computes the [bridgev2.ChatInfo] for a guild space.
func (d *DiscordClient) getGuildSpaceInfo(_ctx context.Context, guild *discordgo.Guild) (*bridgev2.ChatInfo, error) {
	selfEvtSender := d.selfEventSender()

	return &bridgev2.ChatInfo{
		Name:  &guild.Name,
		Topic: nil,
		Members: &bridgev2.ChatMemberList{
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				selfEvtSender.Sender: {EventSender: selfEvtSender},
			},
			// As recommended by the spec, prohibit normal events by setting
			// events_default to a suitably high number.
			PowerLevels: &bridgev2.PowerLevelOverrides{EventsDefault: ptr.Ptr(100)},
		},
		Avatar: d.makeAvatarForGuild(guild),
		Type:   ptr.Ptr(database.RoomTypeSpace),
	}, nil
}

func channelIsPrivate(ch *discordgo.Channel) bool {
	return ch.Type == discordgo.ChannelTypeDM || ch.Type == discordgo.ChannelTypeGroupDM
}

func (d *DiscordClient) makeAvatarForChannel(ctx context.Context, ch *discordgo.Channel) *bridgev2.Avatar {
	// TODO make this configurable (ala workspace_avatar_in_rooms)
	if !channelIsPrivate(ch) {
		guild, err := d.Session.State.Guild(ch.GuildID)

		if err != nil || guild == nil {
			zerolog.Ctx(ctx).Err(err).Msg("Couldn't look up guild in cache in order to create room avatar")
			return nil
		}

		return d.makeAvatarForGuild(guild)
	}

	return &bridgev2.Avatar{
		ID: discordid.MakeAvatarID(ch.Icon),
		Get: func(ctx context.Context) ([]byte, error) {
			url := discordgo.EndpointGroupIcon(ch.ID, ch.Icon)
			return httpGet(ctx, d.httpClient, url, "channel/gdm icon")
		},
		Remove: ch.Icon == "",
	}
}

func (d *DiscordClient) getPrivateChannelMemberList(ch *discordgo.Channel) bridgev2.ChatMemberList {
	var members bridgev2.ChatMemberList
	members.IsFull = true
	members.MemberMap = make(bridgev2.ChatMemberMap, len(ch.Recipients))

	if len(ch.Recipients) > 0 {
		selfEventSender := d.selfEventSender()

		// Private channels' array of participants doesn't include ourselves,
		// so inject ourselves as a member.
		members.MemberMap[selfEventSender.Sender] = bridgev2.ChatMember{EventSender: selfEventSender}

		for _, recipient := range ch.Recipients {
			sender := d.makeEventSender(recipient)
			members.MemberMap[sender.Sender] = bridgev2.ChatMember{EventSender: sender}
		}

		members.TotalMemberCount = len(ch.Recipients)
	}

	return members
}

// getChannelChatInfo computes [bridgev2.ChatInfo] for a guild channel or private (DM or group DM) channel.
func (d *DiscordClient) getChannelChatInfo(ctx context.Context, ch *discordgo.Channel) (*bridgev2.ChatInfo, error) {
	var roomType database.RoomType
	switch ch.Type {
	case discordgo.ChannelTypeGuildCategory:
		roomType = database.RoomTypeSpace
	case discordgo.ChannelTypeDM:
		roomType = database.RoomTypeDM
	case discordgo.ChannelTypeGroupDM:
		roomType = database.RoomTypeGroupDM
	default:
		roomType = database.RoomTypeDefault
	}

	var parentPortalID *networkid.PortalID
	if ch.Type == discordgo.ChannelTypeGuildCategory || (ch.ParentID == "" && ch.GuildID != "") {
		// Categories and uncategorized guild channels always have the guild as their parent.
		parentPortalID = ptr.Ptr(discordid.MakeGuildPortalIDWithID(ch.GuildID))
	} else if ch.ParentID != "" {
		// Categorized guild channels.
		parentPortalID = ptr.Ptr(discordid.MakeChannelPortalIDWithID(ch.ParentID))
	}

	var memberList bridgev2.ChatMemberList
	if channelIsPrivate(ch) {
		memberList = d.getPrivateChannelMemberList(ch)
	} else {
		// TODO we're _always_ sending partial member lists for guilds; we can probably
		// do better than that
		selfEventSender := d.selfEventSender()

		memberList = bridgev2.ChatMemberList{
			IsFull: false,
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				selfEventSender.Sender: {EventSender: selfEventSender},
			},
		}
	}

	return &bridgev2.ChatInfo{
		Name:   &ch.Name,
		Topic:  &ch.Topic,
		Avatar: d.makeAvatarForChannel(ctx, ch),

		Members: &memberList,

		Type:     &roomType,
		ParentID: parentPortalID,

		CanBackfill: true,

		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) (changed bool) {
			meta := portal.Metadata.(*discordid.PortalMetadata)
			if meta.GuildID != ch.GuildID {
				meta.GuildID = ch.GuildID
				changed = true
			}

			return
		},
	}, nil
}

func (d *DiscordClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	guildID := discordid.ParseGuildPortalID(portal.ID)
	if guildID != "" {
		// Portal is a space representing a Discord guild.

		guild, err := d.Session.State.Guild(guildID)
		if err != nil {
			return nil, fmt.Errorf("couldn't get guild: %w", err)
		}

		return d.getGuildSpaceInfo(ctx, guild)
	} else {
		// Portal is to a channel of some kind (private or guild).
		channelID := discordid.ParseChannelPortalID(portal.ID)

		ch, err := d.Session.State.Channel(channelID)
		if err != nil {
			return nil, fmt.Errorf("couldn't get channel: %w", err)
		}

		return d.getChannelChatInfo(ctx, ch)
	}
}

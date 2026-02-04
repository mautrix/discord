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

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type DiscordChatResync struct {
	Client    *DiscordClient
	channel   *discordgo.Channel
	portalKey networkid.PortalKey
}

var (
	_ bridgev2.RemoteChatResyncWithInfo       = (*DiscordChatResync)(nil)
	_ bridgev2.RemoteChatResyncBackfill       = (*DiscordChatResync)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*DiscordChatResync)(nil)
)

func (d *DiscordChatResync) AddLogContext(c zerolog.Context) zerolog.Context {
	c = c.Str("channel_id", d.channel.ID).Int("channel_type", int(d.channel.Type))
	return c
}

func (d *DiscordChatResync) GetPortalKey() networkid.PortalKey {
	return d.portalKey
}

func (d *DiscordChatResync) GetSender() bridgev2.EventSender {
	return bridgev2.EventSender{}
}

func (d *DiscordChatResync) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventChatResync
}

func (d *DiscordChatResync) avatar(ctx context.Context) *bridgev2.Avatar {
	ch := d.channel

	// TODO make this configurable (ala workspace_avatar_in_rooms)
	if !d.isPrivate() {
		guild, err := d.Client.Session.State.Guild(ch.GuildID)

		if err != nil || guild == nil {
			zerolog.Ctx(ctx).Err(err).Msg("Couldn't look up guild in cache in order to create room avatar")
			return nil
		}

		return d.Client.makeAvatarForGuild(guild)
	}

	return &bridgev2.Avatar{
		ID: discordid.MakeAvatarID(ch.Icon),
		Get: func(ctx context.Context) ([]byte, error) {
			url := discordgo.EndpointGroupIcon(ch.ID, ch.Icon)
			return d.Client.simpleDownload(ctx, url, "group dm icon")
		},
		Remove: ch.Icon == "",
	}
}

func (d *DiscordChatResync) privateChannelMemberList() bridgev2.ChatMemberList {
	ch := d.channel

	var members bridgev2.ChatMemberList
	members.IsFull = true
	members.MemberMap = make(bridgev2.ChatMemberMap, len(ch.Recipients))
	if len(ch.Recipients) > 0 {
		selfEventSender := d.Client.selfEventSender()

		// Private channels' array of participants doesn't include ourselves,
		// so inject ourselves as a member.
		members.MemberMap[selfEventSender.Sender] = bridgev2.ChatMember{EventSender: selfEventSender}

		for _, recipient := range ch.Recipients {
			sender := d.Client.makeEventSender(recipient)
			members.MemberMap[sender.Sender] = bridgev2.ChatMember{EventSender: sender}
		}

		members.TotalMemberCount = len(ch.Recipients)
	}

	return members
}

func (d *DiscordChatResync) memberList() bridgev2.ChatMemberList {
	if d.isPrivate() {
		return d.privateChannelMemberList()
	}

	// TODO we're _always_ sending partial member lists for guilds; we can probably
	// do better
	selfEventSender := d.Client.selfEventSender()

	return bridgev2.ChatMemberList{
		IsFull: false,
		MemberMap: map[networkid.UserID]bridgev2.ChatMember{
			selfEventSender.Sender: {EventSender: selfEventSender},
		},
	}
}

func (d *DiscordChatResync) isPrivate() bool {
	ch := d.channel
	return ch.Type == discordgo.ChannelTypeDM || ch.Type == discordgo.ChannelTypeGroupDM
}

func (d *DiscordChatResync) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	ch := d.channel

	var roomType database.RoomType

	switch ch.Type {
	case discordgo.ChannelTypeDM:
		roomType = database.RoomTypeDM
	case discordgo.ChannelTypeGroupDM:
		roomType = database.RoomTypeGroupDM
	}

	info := &bridgev2.ChatInfo{
		Name:        &ch.Name,
		Members:     ptr.Ptr(d.memberList()),
		Avatar:      d.avatar(ctx),
		Type:        &roomType,
		CanBackfill: true,
		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) (changed bool) {
			meta := portal.Metadata.(*discordid.PortalMetadata)
			if meta.GuildID != ch.GuildID {
				meta.GuildID = ch.GuildID
				changed = true
			}

			return
		},
	}

	if !d.isPrivate() {
		// Channel belongs to a guild; associate it with the respective space.
		info.ParentID = ptr.Ptr(d.Client.guildPortalKeyFromID(ch.GuildID).ID)
	}

	return info, nil
}

func (d *DiscordChatResync) ShouldCreatePortal() bool {
	return true
}

func (d *DiscordChatResync) CheckNeedsBackfill(ctx context.Context, latestBridged *database.Message) (bool, error) {
	if latestBridged == nil {
		zerolog.Ctx(ctx).Debug().Str("channel_id", d.channel.ID).Msg("Haven't bridged any messages at all, not forward backfilling")
		return false, nil
	}
	return latestBridged.ID < discordid.MakeMessageID(d.channel.LastMessageID), nil
}

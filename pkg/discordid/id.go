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

package discordid

import (
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// DeletedGuildUserID is a magic user ID that is used in place of an actual user
// ID once they have deleted their account. This only applies in non-private
// (i.e. guild) contexts, such as guild channel message authors and mentions.
//
// Note that this user ID can also appear in message content as part of user
// mention markup ("<@456226577798135808>").
const DeletedGuildUserID = "456226577798135808"

// DeletedGuildUser is the user returned from the Discord API as a stand-in for
// users who have since deleted their account. As the name suggests, this only
// applies to fetched entities within guilds.
var DeletedGuildUser = discordgo.User{
	ID:            DeletedGuildUserID,
	Username:      "Deleted User",
	Discriminator: "0000",
}

const DiscordEpochMillis = 1420070400000

// GenerateNonce creates a Discord-style snowflake nonce for message idempotency.
func GenerateNonce() string {
	snowflake := (time.Now().UnixMilli() - DiscordEpochMillis) << 22
	return strconv.FormatInt(snowflake, 10)
}

func MakeUserID(userID string) networkid.UserID {
	return networkid.UserID(userID)
}

func ParseUserID(userID networkid.UserID) string {
	return string(userID)
}

func MakeUserLoginID(userID string) networkid.UserLoginID {
	return networkid.UserLoginID(userID)
}

func ParseUserLoginID(id networkid.UserLoginID) string {
	return string(id)
}

// UserLoginIDToUserID converts a UserLoginID to a UserID. In Discord, both
// are the same underlying snowflake.
func UserLoginIDToUserID(id networkid.UserLoginID) networkid.UserID {
	return networkid.UserID(id)
}

func MakePortalID(channelID string) networkid.PortalID {
	return networkid.PortalID(channelID)
}

func ParsePortalID(portalID networkid.PortalID) string {
	return string(portalID)
}

func MakeMessageID(messageID string) networkid.MessageID {
	return networkid.MessageID(messageID)
}

func ParseMessageID(messageID networkid.MessageID) string {
	return string(messageID)
}

func MakeEmojiID(emojiName string) networkid.EmojiID {
	return networkid.EmojiID(emojiName)
}

func ParseEmojiID(emojiID networkid.EmojiID) string {
	return string(emojiID)
}

func MakeAvatarID(avatar string) networkid.AvatarID {
	return networkid.AvatarID(avatar)
}

// The string prepended to [networkid.PortalKey]s identifying spaces that
// bridge Discord guilds.
//
// Every Discord guild created before August 2017 contained a channel
// having _the same ID as the guild itself_. This channel also functioned as
// the "default channel" in that incoming members would view this channel by
// default. It was also impossible to delete.
//
// After this date, these "default channels" became deletable, and fresh guilds
// were no longer created with a channel that exactly corresponded to the guild
// ID.
//
// To accommodate Discord guilds created before this API change that have also
// never deleted the default channel, we need a way to distinguish between the
// guild and the default channel, as we wouldn't be able to bridge the guild
// as a space otherwise.
//
// "*" was chosen as the asterisk character is used to filter by guilds in
// the quick switcher (in Discord's first-party clients).
//
// For more information, see: https://discord.com/developers/docs/change-log#breaking-change-default-channels:~:text=New%20guilds%20will%20no%20longer.
const GuildPortalKeySigil = "*"

func MakeGuildPortalID(guildID string) networkid.PortalID {
	return networkid.PortalID(GuildPortalKeySigil + guildID)
}

func MakePortalKey(ch *discordgo.Channel, userLoginID networkid.UserLoginID, wantReceiver bool) (key networkid.PortalKey) {
	key.ID = MakePortalID(ch.ID)
	if wantReceiver {
		key.Receiver = userLoginID
	}
	return
}

func MakePortalKeyWithID(channelID string) (key networkid.PortalKey) {
	key.ID = MakePortalID(channelID)
	return
}

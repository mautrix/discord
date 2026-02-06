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
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func (d *DiscordClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	// We define `UserID`s and `UserLoginID`s to be interchangeable, i.e. they map
	// directly to Discord user IDs ("snowflakes"), so we can perform a direct comparison.
	return userID == discordid.UserLoginIDToUserID(d.UserLogin.ID)
}

func (d *DiscordClient) makeUserAvatar(u *discordgo.User) *bridgev2.Avatar {
	url := u.AvatarURL("256")

	return &bridgev2.Avatar{
		ID: discordid.MakeAvatarID(url),
		Get: func(ctx context.Context) ([]byte, error) {
			return d.simpleDownload(ctx, url, "user avatar")
		},
	}
}

func (d *DiscordClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	log := zerolog.Ctx(ctx)

	if ghost.ID == "" {
		log.Warn().Msg("Tried to get user info for ghost with no ID")
		return nil, nil
	}

	discordUserID := discordid.ParseUserID(ghost.ID)
	discordUser := d.userCache.Resolve(ctx, discordUserID)
	if discordUser == nil {
		log.Error().Str("discord_user_id", discordUserID).
			Msg("Failed to resolve user")
		return nil, nil
	}

	return &bridgev2.UserInfo{
		// FIXME clear this for webhooks (stash in ghost metadata)
		Identifiers: []string{fmt.Sprintf("discord:%s", discordUser.String())},
		Name:        ptr.Ptr(discordUser.DisplayName()),
		Avatar:      d.makeUserAvatar(discordUser),
		IsBot:       &discordUser.Bot,
	}, nil
}

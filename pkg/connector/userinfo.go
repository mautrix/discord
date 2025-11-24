// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func (d *DiscordClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	// We define `UserID`s and `UserLoginID`s to be interchangeable, i.e. they map
	// directly to Discord user IDs ("snowflakes"), so we can perform a direct comparison.
	return userID == networkid.UserID(d.UserLogin.ID)
}

func (d *DiscordClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	log := zerolog.Ctx(ctx)

	if ghost.ID == "" {
		log.Warn().Msg("Tried to get user info for ghost with no ID")
		return nil, nil
	}

	// FIXME(skip): This won't work for users in guilds.

	user, ok := d.usersFromReady[string(ghost.ID)]
	if !ok {
		log.Error().Str("ghost_id", string(ghost.ID)).Msg("Couldn't find corresponding user from READY for ghost")
		return nil, nil
	}

	return &bridgev2.UserInfo{
		Identifiers: []string{fmt.Sprintf("discord:%s", user.ID)},
		Name:        ptr.Ptr(user.DisplayName()),
		IsBot:       &user.Bot,
	}, nil
}

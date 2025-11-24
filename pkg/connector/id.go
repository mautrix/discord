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
	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func (d *DiscordClient) makePortalKey(ch *discordgo.Channel, userLoginID networkid.UserLoginID, wantReceiver bool) (key networkid.PortalKey) {
	key.ID = networkid.PortalID(ch.ID)
	if wantReceiver {
		key.Receiver = userLoginID
	}
	return
}

func (d *DiscordClient) makeEventSender(user *discordgo.User) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    user.ID == d.Session.State.User.ID,
		SenderLogin: d.UserLogin.ID,
		Sender:      networkid.UserID(user.ID),
	}
}

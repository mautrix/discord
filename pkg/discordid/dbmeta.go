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
	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/database"
)

type PortalMetadata struct {
	// The ID of the Discord guild that the channel corresponding to this portal
	// belongs to.
	//
	// For private channels (DMs and group DMs), this will be the zero value
	// (an empty string).
	GuildID string `json:"guild_id"`
}

type UserLoginMetadata struct {
	Token            string                     `json:"token"`
	HeartbeatSession discordgo.HeartbeatSession `json:"heartbeat_session"`
	BridgedGuildIDs  map[string]bool            `json:"bridged_guild_ids,omitempty"`
}

var _ database.MetaMerger = (*UserLoginMetadata)(nil)

func (ulm *UserLoginMetadata) CopyFrom(incoming any) {
	incomingMeta, ok := incoming.(*UserLoginMetadata)
	if !ok || incomingMeta == nil {
		return
	}

	if incomingMeta.Token != "" {
		ulm.Token = incomingMeta.Token
	}
	ulm.HeartbeatSession = discordgo.NewHeartbeatSession()

	// Retain the BridgedGuildIDs from the existing login.
}

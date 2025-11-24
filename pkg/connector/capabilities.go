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

	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// Whether to aggressively update user info. Only relevant during initial development
// of this bridge.
var aggressivelyUpdateInfoForBridgeDevelopment = true

var DiscordGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	AggressiveUpdateInfo: aggressivelyUpdateInfoForBridgeDevelopment,
	Provisioning: bridgev2.ProvisioningCapabilities{
		ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{},
		GroupCreation:     map[string]bridgev2.GroupTypeCapabilities{},
	},
}

func (dc *DiscordConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return DiscordGeneralCaps
}

func (wa *DiscordConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 1
}

/*func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}*/

func capID() string {
	base := "fi.mau.discord.capabilities.2025_11_20"
	if ffmpeg.Supported() {
		return base + "+ffmpeg"
	}
	return base
}

var discordCaps = &event.RoomFeatures{
	ID: capID(),
}

func (dc *DiscordClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return discordCaps
}

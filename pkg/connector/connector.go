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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/msgconv"
)

type DiscordConnector struct {
	Bridge  *bridgev2.Bridge
	Config  Config
	MsgConv *msgconv.MessageConverter
}

var (
	_ bridgev2.NetworkConnector               = (*DiscordConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork          = (*DiscordConnector)(nil)
	_ bridgev2.TransactionIDGeneratingNetwork = (*DiscordConnector)(nil)
)

func (d *DiscordConnector) Init(bridge *bridgev2.Bridge) {
	d.Bridge = bridge
	d.MsgConv = msgconv.NewMessageConverter(bridge)
	d.setUpProvisioningAPIs()
}

func (d *DiscordConnector) SetMaxFileSize(maxSize int64) {
	d.MsgConv.MaxFileSize = maxSize
}

func (d *DiscordConnector) Start(ctx context.Context) error {
	return nil
}

func (d *DiscordConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Discord",
		NetworkURL:       "https://discord.com",
		NetworkIcon:      "mxc://maunium.net/nIdEykemnwdisvHbpxflpDlC",
		NetworkID:        "discord",
		BeeperBridgeType: "discordgo",
		DefaultPort:      29334,
	}
}

func (d *DiscordConnector) GenerateTransactionID(_ id.UserID, _ id.RoomID, _ event.Type) networkid.RawTransactionID {
	return networkid.RawTransactionID(discordid.GenerateNonce())
}

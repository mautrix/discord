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

	"maunium.net/go/mautrix/bridgev2"
)

type DiscordClient struct {
}

func (d *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	//TODO implement me
	panic("implement me")
}

var _ bridgev2.NetworkAPI = (*DiscordClient)(nil)

func (d *DiscordClient) Connect(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) Disconnect() {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) IsLoggedIn() bool {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) LogoutRemote(ctx context.Context) {
	//TODO implement me
	panic("implement me")
}

func (d *DiscordClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *bridgev2.NetworkRoomCapabilities {
	//TODO implement me
	panic("implement me")
}

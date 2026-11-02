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
	"slices"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func (d *DiscordClient) guildBridgingMode(guildID string) discordid.GuildBridgingMode {
	d.guildModesLock.RLock()
	defer d.guildModesLock.RUnlock()
	meta := d.userLoginMetadata()
	if mode, ok := meta.GuildBridgingModes[guildID]; ok {
		return mode
	}
	if meta.BridgedGuildIDs[guildID] || slices.Contains(d.connector.Config.Guilds.BridgingGuildIDs, guildID) {
		return discordid.GuildBridgeEverything
	}
	return discordid.GuildBridgeNothing
}

func (d *DiscordClient) setGuildBridgingMode(ctx context.Context, guildID string, mode discordid.GuildBridgingMode) error {
	d.guildModesLock.Lock()
	defer d.guildModesLock.Unlock()
	meta := d.userLoginMetadata()
	if meta.GuildBridgingModes == nil {
		meta.GuildBridgingModes = make(map[string]discordid.GuildBridgingMode)
	}
	previous, existed := meta.GuildBridgingModes[guildID]
	meta.GuildBridgingModes[guildID] = mode
	if err := d.UserLogin.Save(ctx); err != nil {
		if existed {
			meta.GuildBridgingModes[guildID] = previous
		} else {
			delete(meta.GuildBridgingModes, guildID)
		}
		return err
	}
	return nil
}

func (d *DiscordClient) unbridgeGuild(ctx context.Context, guildID string) (int, error) {
	if err := d.setGuildBridgingMode(ctx, guildID, discordid.GuildBridgeNothing); err != nil {
		return 0, err
	}
	guild, err := d.connector.Bridge.GetExistingPortalByKey(ctx, d.guildPortalKey(guildID))
	if err != nil {
		return 0, err
	}
	if guild == nil || guild.MXID == "" {
		return 0, nil
	}
	portals, err := d.connector.collectAllGuildPortals(ctx, guild)
	if err != nil {
		return 0, err
	}
	var deleteErr error
	bridgev2.DeleteManyPortals(ctx, portals, func(portal *bridgev2.Portal, deleting bool, err error) {
		if err != nil {
			deleteErr = fmt.Errorf("failed to delete %s: %w", portal.ID, err)
		}
	})
	return len(portals), deleteErr
}

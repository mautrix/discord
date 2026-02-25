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

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
)

// (Used by formatter_tag.go via an interface.)
func (d *DiscordConnector) GetRoleByID(ctx context.Context, guildID, roleID string) (*discorddb.Role, error) {
	return d.DB.Role.GetByID(ctx, guildID, roleID)
}

func guildRoleChanged(oldRole *discorddb.Role, newRole *discordgo.Role) bool {
	return oldRole.Name != newRole.Name ||
		oldRole.Icon != newRole.Icon ||
		oldRole.Mentionable != newRole.Mentionable ||
		oldRole.Managed != newRole.Managed ||
		oldRole.Hoist != newRole.Hoist ||
		oldRole.Color != newRole.Color ||
		oldRole.Position != newRole.Position ||
		oldRole.Permissions != newRole.Permissions
}

func (d *DiscordClient) syncGuildRoles(ctx context.Context, guildID string, roles []*discordgo.Role) error {
	if len(roles) == 0 {
		return nil
	}

	existingRoles, err := d.connector.DB.Role.GetByGuildID(ctx, guildID)
	if err != nil {
		return fmt.Errorf("failed to get existing guild roles: %w", err)
	}

	existingRoleMap := make(map[string]*discorddb.Role, len(existingRoles))
	for _, role := range existingRoles {
		existingRoleMap[role.ID] = role
	}

	err = d.connector.DB.Role.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		for _, role := range roles {
			if role == nil {
				continue
			}

			existingRole := existingRoleMap[role.ID]
			if existingRole == nil || guildRoleChanged(existingRole, role) {
				if err := d.connector.DB.Role.Put(ctx, &discorddb.Role{
					GuildID: guildID,
					Role:    *role,
				}); err != nil {
					return fmt.Errorf("failed to upsert guild role: %w", err)
				}
			}

			delete(existingRoleMap, role.ID)
		}

		for _, removedRole := range existingRoleMap {
			if err := d.connector.DB.Role.DeleteByID(ctx, guildID, removedRole.ID); err != nil {
				return fmt.Errorf("failed to delete removed guild role: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to sync guild roles: %w", err)
	}

	return nil
}

func (d *DiscordClient) upsertGuildRole(ctx context.Context, guildID string, role *discordgo.Role) error {
	if role == nil {
		return nil
	}

	if err := d.connector.DB.Role.Put(ctx, &discorddb.Role{
		GuildID: guildID,
		Role:    *role,
	}); err != nil {
		return fmt.Errorf("failed to upsert guild role: %w", err)
	}

	return nil
}

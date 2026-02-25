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

package discorddb

import (
	"context"
	"database/sql"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/dbutil"
)

type RoleQuery struct {
	*dbutil.QueryHelper[*Role]
}

type Role struct {
	GuildID string
	discordgo.Role
}

func (r *Role) sqlVariables() []any {
	return []any{
		r.GuildID,
		r.ID,
		r.Name,
		dbutil.StrPtr(r.Icon),
		r.Mentionable,
		r.Managed,
		r.Hoist,
		r.Color,
		r.Position,
		r.Permissions,
	}
}

func newRole(_ *dbutil.QueryHelper[*Role]) *Role {
	return &Role{}
}

const (
	getRoleByIDQuery = `
		SELECT discord_guild_id, discord_id, name, icon, mentionable, managed, hoist, color, position, permissions
		FROM role
		WHERE discord_guild_id=$1 AND discord_id=$2
	`
	getRolesByGuildIDQuery = `
		SELECT discord_guild_id, discord_id, name, icon, mentionable, managed, hoist, color, position, permissions
		FROM role
		WHERE discord_guild_id=$1
		ORDER BY position DESC, discord_id
	`
	upsertRoleQuery = `
		INSERT INTO role (discord_guild_id, discord_id, name, icon, mentionable, managed, hoist, color, position, permissions)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (discord_guild_id, discord_id) DO UPDATE
			SET name = excluded.name,
			    icon = excluded.icon,
			    mentionable = excluded.mentionable,
			    managed = excluded.managed,
			    hoist = excluded.hoist,
			    color = excluded.color,
			    position = excluded.position,
			    permissions = excluded.permissions
	`
	deleteRolesByGuildIDQuery = `
		DELETE FROM role WHERE discord_guild_id=$1
	`
	deleteRoleByIDQuery = `
		DELETE FROM role WHERE discord_guild_id=$1 AND discord_id=$2
	`
)

func (rq *RoleQuery) GetByID(ctx context.Context, guildID, roleID string) (*Role, error) {
	return rq.QueryOne(ctx, getRoleByIDQuery, &guildID, &roleID)
}

func (rq *RoleQuery) GetByGuildID(ctx context.Context, guildID string) ([]*Role, error) {
	return rq.QueryMany(ctx, getRolesByGuildIDQuery, &guildID)
}

func (rq *RoleQuery) Put(ctx context.Context, role *Role) error {
	return rq.Exec(ctx, upsertRoleQuery, role.sqlVariables()...)
}

func (rq *RoleQuery) PutMany(ctx context.Context, roles []*Role) error {
	for _, role := range roles {
		if err := rq.Put(ctx, role); err != nil {
			return err
		}
	}
	return nil
}

func (rq *RoleQuery) DeleteByGuildID(ctx context.Context, guildID string) error {
	return rq.Exec(ctx, deleteRolesByGuildIDQuery, &guildID)
}

func (rq *RoleQuery) DeleteByID(ctx context.Context, guildID, roleID string) error {
	return rq.Exec(ctx, deleteRoleByIDQuery, &guildID, &roleID)
}

func (rq *RoleQuery) ReplaceGuildRoles(ctx context.Context, guildID string, roles []*Role) error {
	return rq.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		if err := rq.DeleteByGuildID(ctx, guildID); err != nil {
			return err
		}
		for _, role := range roles {
			role.GuildID = guildID
			if err := rq.Put(ctx, role); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Role) Scan(row dbutil.Scannable) (*Role, error) {
	var icon sql.NullString
	err := row.Scan(
		&r.GuildID,
		&r.ID,
		&r.Name,
		&icon,
		&r.Mentionable,
		&r.Managed,
		&r.Hoist,
		&r.Color,
		&r.Position,
		&r.Permissions,
	)
	if err != nil {
		return nil, err
	}
	r.Icon = icon.String
	return r, nil
}

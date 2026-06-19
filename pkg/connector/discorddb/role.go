package discorddb

import (
	"context"

	"go.mau.fi/util/dbutil"
)

// Role mirrors a Discord role for mention rendering. The in-memory cache is the
// hot path (H10); this table is the cold-start backing store.
type Role struct {
	GuildID     string
	RoleID      string
	Name        string
	Icon        *string
	Mentionable bool
	Managed     bool
	Hoist       bool
	Color       int
	Position    int
	Permissions int64
}

func (r *Role) Scan(row dbutil.Scannable) (*Role, error) {
	err := row.Scan(
		&r.GuildID, &r.RoleID,
		&r.Name, &r.Icon,
		&r.Mentionable, &r.Managed, &r.Hoist,
		&r.Color, &r.Position, &r.Permissions,
	)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Role) sqlVariables() []any {
	return []any{
		r.GuildID, r.RoleID,
		r.Name, r.Icon,
		r.Mentionable, r.Managed, r.Hoist,
		r.Color, r.Position, r.Permissions,
	}
}

// RoleQuery provides CRUD operations on the dc_role table.
type RoleQuery struct {
	*dbutil.QueryHelper[*Role]
}

const (
	getRoleBaseQuery = `
		SELECT dc_guild_id, dcid, name, icon, mentionable, managed, hoist, color, position, permissions
		FROM dc_role
	`
	getRoleByIDQuery        = getRoleBaseQuery + `WHERE dc_guild_id=$1 AND dcid=$2`
	getAllRolesByGuildQuery = getRoleBaseQuery + `WHERE dc_guild_id=$1 ORDER BY position ASC`

	upsertRoleQuery = `
		INSERT INTO dc_role (dc_guild_id, dcid, name, icon, mentionable, managed, hoist, color, position, permissions)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (dc_guild_id, dcid) DO UPDATE
		    SET name=$3, icon=$4, mentionable=$5, managed=$6, hoist=$7, color=$8, position=$9, permissions=$10
	`
	deleteRoleQuery             = `DELETE FROM dc_role WHERE dc_guild_id=$1 AND dcid=$2`
	deleteAllRolesForGuildQuery = `DELETE FROM dc_role WHERE dc_guild_id=$1`
)

func (rq *RoleQuery) Get(ctx context.Context, guildID, roleID string) (*Role, error) {
	return rq.QueryOne(ctx, getRoleByIDQuery, guildID, roleID)
}

func (rq *RoleQuery) GetAllForGuild(ctx context.Context, guildID string) ([]*Role, error) {
	return rq.QueryMany(ctx, getAllRolesByGuildQuery, guildID)
}

func (rq *RoleQuery) Upsert(ctx context.Context, role *Role) error {
	return rq.Exec(ctx, upsertRoleQuery, role.sqlVariables()...)
}

func (rq *RoleQuery) Delete(ctx context.Context, guildID, roleID string) error {
	return rq.Exec(ctx, deleteRoleQuery, guildID, roleID)
}

func (rq *RoleQuery) DeleteAllForGuild(ctx context.Context, guildID string) error {
	return rq.Exec(ctx, deleteAllRolesForGuildQuery, guildID)
}

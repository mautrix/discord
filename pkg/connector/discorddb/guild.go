package discorddb

import (
	"context"

	"go.mau.fi/util/dbutil"
)

// Guild holds minimal guild metadata. Rebuilt from GUILD_CREATE on connect;
// no migration insert per C4 (ar-report).
type Guild struct {
	GuildID     string
	Name        string
	Avatar      string
	MemberCount int
}

func (g *Guild) Scan(row dbutil.Scannable) (*Guild, error) {
	err := row.Scan(&g.GuildID, &g.Name, &g.Avatar, &g.MemberCount)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Guild) sqlVariables() []any {
	return []any{g.GuildID, g.Name, g.Avatar, g.MemberCount}
}

// GuildQuery provides CRUD operations on the dc_guild table.
type GuildQuery struct {
	*dbutil.QueryHelper[*Guild]
}

const (
	getGuildBaseQuerySQL = `
		SELECT dcid, name, avatar, member_count
		FROM dc_guild
	`
	getGuildByIDQuery = getGuildBaseQuerySQL + `WHERE dcid=$1`

	upsertGuildQuery = `
		INSERT INTO dc_guild (dcid, name, avatar, member_count)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (dcid) DO UPDATE
		    SET name=$2, avatar=$3, member_count=$4
	`
	deleteGuildQuery = `DELETE FROM dc_guild WHERE dcid=$1`
)

func (gq *GuildQuery) Get(ctx context.Context, guildID string) (*Guild, error) {
	return gq.QueryOne(ctx, getGuildByIDQuery, guildID)
}

func (gq *GuildQuery) Upsert(ctx context.Context, guild *Guild) error {
	return gq.Exec(ctx, upsertGuildQuery, guild.sqlVariables()...)
}

func (gq *GuildQuery) Delete(ctx context.Context, guildID string) error {
	return gq.Exec(ctx, deleteGuildQuery, guildID)
}

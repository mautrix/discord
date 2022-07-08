package database

import (
	"database/sql"
	"errors"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/util/dbutil"

	"github.com/bwmarrin/discordgo"
)

type RoleQuery struct {
	db  *Database
	log log.Logger
}

// language=postgresql
const (
	roleSelect = "SELECT dc_guild_id, dcid, name, icon, mentionable, managed, hoist, color, position, permissions FROM role"
	roleUpsert = `
		INSERT INTO role (dc_guild_id, dcid, name, icon, mentionable, managed, hoist, color, position, permissions)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (dc_guild_id, dcid) DO UPDATE
		    SET name=excluded.name, icon=excluded.icon, mentionable=excluded.mentionable, managed=excluded.managed,
		        hoist=excluded.hoist, color=excluded.color, position=excluded.position, permissions=excluded.permissions
	`
	roleDelete = "DELETE FROM role WHERE dc_guild_id=$1 AND dcid=$2"
)

func (rq *RoleQuery) New() *Role {
	return &Role{
		db:  rq.db,
		log: rq.log,
	}
}

func (rq *RoleQuery) GetByID(guildID, dcid string) *Role {
	query := roleSelect + " WHERE dc_guild_id=$1 AND dcid=$2"
	return rq.New().Scan(rq.db.QueryRow(query, guildID, dcid))
}

func (rq *RoleQuery) DeleteByID(guildID, dcid string) {
	_, err := rq.db.Exec("DELETE FROM role WHERE dc_guild_id=$1 AND dcid=$2", guildID, dcid)
	if err != nil {
		rq.log.Warnfln("Failed to delete %s/%s: %v", guildID, dcid, err)
		panic(err)
	}
}

func (rq *RoleQuery) GetAll(guildID string) []*Role {
	rows, err := rq.db.Query(roleSelect+" WHERE dc_guild_id=$1", guildID)
	if err != nil {
		rq.log.Errorfln("Failed to query roles of %s: %v", guildID, err)
		return nil
	}

	var roles []*Role
	for rows.Next() {
		role := rq.New().Scan(rows)
		if role != nil {
			roles = append(roles, role)
		}
	}

	return roles
}

type Role struct {
	db  *Database
	log log.Logger

	GuildID string

	discordgo.Role
}

func (r *Role) Scan(row dbutil.Scannable) *Role {
	var icon sql.NullString
	err := row.Scan(&r.GuildID, &r.ID, &r.Name, &icon, &r.Mentionable, &r.Managed, &r.Hoist, &r.Color, &r.Position, &r.Permissions)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.log.Errorln("Database scan failed:", err)
			panic(err)
		}

		return nil
	}
	r.Icon = icon.String
	return r
}

func (r *Role) Upsert(txn dbutil.Execable) {
	if txn == nil {
		txn = r.db
	}
	_, err := txn.Exec(roleUpsert, r.GuildID, r.ID, r.Name, strPtr(r.Icon), r.Mentionable, r.Managed, r.Hoist, r.Color, r.Position, r.Permissions)
	if err != nil {
		r.log.Warnfln("Failed to insert %s/%s: %v", r.GuildID, r.ID, err)
		panic(err)
	}
}

func (r *Role) Delete(txn dbutil.Execable) {
	if txn == nil {
		txn = r.db
	}
	_, err := txn.Exec(roleDelete, r.GuildID, r.Icon)
	if err != nil {
		r.log.Warnfln("Failed to delete %s/%s: %v", r.GuildID, r.ID, err)
		panic(err)
	}
}

package discorddb

import (
	"context"

	"go.mau.fi/util/dbutil"
)

// Emoji is a cached Discord custom emoji. Rebuilt from GUILD_CREATE on connect;
// no migration insert per C4 (ar-report).
type Emoji struct {
	GuildID  string
	EmojiID  string
	Name     string
	Animated bool
}

func (e *Emoji) Scan(row dbutil.Scannable) (*Emoji, error) {
	err := row.Scan(&e.GuildID, &e.EmojiID, &e.Name, &e.Animated)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Emoji) sqlVariables() []any {
	return []any{e.GuildID, e.EmojiID, e.Name, e.Animated}
}

// EmojiQuery provides CRUD operations on the dc_emoji table.
type EmojiQuery struct {
	*dbutil.QueryHelper[*Emoji]
}

const (
	getEmojiBaseQuery = `
		SELECT guild_id, emoji_id, name, animated
		FROM dc_emoji
	`
	getEmojiByIDQuery        = getEmojiBaseQuery + `WHERE guild_id=$1 AND emoji_id=$2`
	getAllEmojisByGuildQuery = getEmojiBaseQuery + `WHERE guild_id=$1`

	upsertEmojiQuery = `
		INSERT INTO dc_emoji (guild_id, emoji_id, name, animated)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (guild_id, emoji_id) DO UPDATE
		    SET name=$3, animated=$4
	`
	deleteEmojiQuery             = `DELETE FROM dc_emoji WHERE guild_id=$1 AND emoji_id=$2`
	deleteAllEmojisForGuildQuery = `DELETE FROM dc_emoji WHERE guild_id=$1`
)

func (eq *EmojiQuery) Get(ctx context.Context, guildID, emojiID string) (*Emoji, error) {
	return eq.QueryOne(ctx, getEmojiByIDQuery, guildID, emojiID)
}

func (eq *EmojiQuery) GetAllForGuild(ctx context.Context, guildID string) ([]*Emoji, error) {
	return eq.QueryMany(ctx, getAllEmojisByGuildQuery, guildID)
}

func (eq *EmojiQuery) Upsert(ctx context.Context, emoji *Emoji) error {
	return eq.Exec(ctx, upsertEmojiQuery, emoji.sqlVariables()...)
}

func (eq *EmojiQuery) Delete(ctx context.Context, guildID, emojiID string) error {
	return eq.Exec(ctx, deleteEmojiQuery, guildID, emojiID)
}

func (eq *EmojiQuery) DeleteAllForGuild(ctx context.Context, guildID string) error {
	return eq.Exec(ctx, deleteAllEmojisForGuildQuery, guildID)
}

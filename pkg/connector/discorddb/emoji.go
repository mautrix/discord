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

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type CustomEmojiQuery struct {
	*dbutil.QueryHelper[*CustomEmoji]
}

type CustomEmoji struct {
	ID       string
	Name     string
	Animated bool
	ImageMXC id.ContentURIString
}

func (ce *CustomEmoji) sqlVariables() []any {
	return []any{ce.ID, ce.Name, ce.Animated, dbutil.StrPtr(ce.ImageMXC)}
}

func newCustomEmoji(_ *dbutil.QueryHelper[*CustomEmoji]) *CustomEmoji {
	return &CustomEmoji{}
}

const (
	getCustomEmojiByMXCQuery = `
		SELECT discord_id, name, animated, mxc FROM custom_emoji WHERE mxc=$1 ORDER BY name
	`
	getCustomEmojiByDiscordIDQuery = `
		SELECT discord_id, name, animated, mxc FROM custom_emoji WHERE discord_id=$1 ORDER BY name
	`
	upsertCustomEmojiQuery = `
		INSERT INTO custom_emoji (discord_id, name, animated, mxc)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (discord_id) DO UPDATE
			SET name = excluded.name, animated = excluded.animated, mxc = excluded.mxc
	`
)

func (ceq *CustomEmojiQuery) GetByDiscordID(ctx context.Context, discordID string) (*CustomEmoji, error) {
	return ceq.QueryOne(ctx, getCustomEmojiByDiscordIDQuery, &discordID)
}

func (ceq *CustomEmojiQuery) GetByMXC(ctx context.Context, mxc string) (*CustomEmoji, error) {
	return ceq.QueryOne(ctx, getCustomEmojiByMXCQuery, &mxc)
}

func (ceq *CustomEmojiQuery) Put(ctx context.Context, emoji *CustomEmoji) error {
	return ceq.Exec(ctx, upsertCustomEmojiQuery, emoji.sqlVariables()...)
}

func (ce *CustomEmoji) Scan(row dbutil.Scannable) (*CustomEmoji, error) {
	var imageURL sql.NullString
	err := row.Scan(&ce.ID, &ce.Name, &ce.Animated, &imageURL)
	if err != nil {
		return nil, err
	}
	ce.ImageMXC = id.ContentURIString(imageURL.String)
	return ce, nil
}

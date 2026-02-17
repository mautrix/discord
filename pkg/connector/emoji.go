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
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
)

func (d *DiscordConnector) getCustomEmojiDownloadURL(emojiID string, animated bool) (string, string) {
	if animated {
		return discordgo.EndpointEmojiAnimated(emojiID), "image/gif"
	}
	// TODO think about using webp for size savings
	return discordgo.EndpointEmoji(emojiID), "image/png"
}

func (d *DiscordConnector) GetCustomEmojiByMXC(ctx context.Context, mxc string) (*discorddb.CustomEmoji, error) {
	return d.DB.CustomEmoji.GetByMXC(ctx, mxc)
}

func (d *DiscordConnector) GetCustomEmojiMXC(ctx context.Context, emojiID, name string, animated bool) (id.ContentURIString, error) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "get discord custom emoji").
		Str("emoji_id", emojiID).
		Str("emoji_name", name).
		Logger()
	ctx = log.WithContext(ctx)

	dbEmoji, err := d.DB.CustomEmoji.GetByDiscordID(ctx, emojiID)
	if err != nil {
		return "", fmt.Errorf("failed to get custom emoji from database: %w", err)
	}

	if dbEmoji != nil && dbEmoji.ImageMXC != "" {
		if dbEmoji.Name != name || dbEmoji.Animated != animated {
			// Make sure to save changed information.
			dbEmoji.Name = name
			dbEmoji.Animated = animated

			err = d.DB.CustomEmoji.Put(ctx, dbEmoji)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to update custom emoji metadata in database")
			}
		}

		return dbEmoji.ImageMXC, nil
	}

	// Custom emoji wasn't in the database or it lacked an MXC, so we have to
	// download it.

	emojiURL, mimeType := d.getCustomEmojiDownloadURL(emojiID, animated)
	data, err := httpGet(ctx, d.httpClient, emojiURL, "emoji")
	if err != nil {
		return "", err
	}

	mxc, _, err := d.Bridge.Bot.UploadMedia(ctx, "", data, "", mimeType)

	log = log.With().Str("image_mxc", string(mxc)).Logger()
	ctx = log.WithContext(ctx)

	if err != nil {
		return "", fmt.Errorf("failed to upload emoji to Matrix: %w", err)
	}

	if dbEmoji == nil {
		dbEmoji = &discorddb.CustomEmoji{
			ID: emojiID,
		}
	}

	dbEmoji.Name = name
	dbEmoji.Animated = animated
	dbEmoji.ImageMXC = mxc

	err = d.DB.CustomEmoji.Put(ctx, dbEmoji)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to save custom emoji")
	}

	return mxc, nil
}

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

package msgconv

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type MessageConverter struct {
	Bridge *bridgev2.Bridge

	HTMLParser *format.HTMLParser

	nextDiscordUploadID atomic.Int32

	MaxFileSize int64
}

func NewMessageConverter(bridge *bridgev2.Bridge) *MessageConverter {
	mc := &MessageConverter{
		Bridge:      bridge,
		MaxFileSize: 50 * 1024 * 1024,
	}
	mc.HTMLParser = &format.HTMLParser{
		TabsToSpaces:   4,
		Newline:        "\n",
		HorizontalLine: "\n---\n",
		PillConverter:  mc.convertPill,
		ItalicConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("*%s*", s)
		},
		UnderlineConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("__%s__", s)
		},
		TextConverter: func(s string, ctx format.Context) string {
			if ctx.TagStack.Has("pre") || ctx.TagStack.Has("code") {
				// If we're in a code block, don't escape markdown
				return s
			}
			return escapeDiscordMarkdown(s)
		},
		SpoilerConverter: func(text, reason string, ctx format.Context) string {
			if reason != "" {
				return fmt.Sprintf("(%s) ||%s||", reason, text)
			}
			return fmt.Sprintf("||%s||", text)
		},
		LinkConverter: func(text, href string, ctx format.Context) string {
			linkPreviews := ctx.ReturnData[formatterContextInputAllowedLinkPreviewsKey].([]string)
			allowPreview := linkPreviews == nil || slices.Contains(linkPreviews, href)
			if text == href {
				if !allowPreview {
					return fmt.Sprintf("<%s>", text)
				}
				return text
			} else if !discordLinkRegexFull.MatchString(href) {
				return fmt.Sprintf("%s (%s)", escapeDiscordMarkdown(text), escapeDiscordMarkdown(href))
			} else if !allowPreview {
				return fmt.Sprintf("[%s](<%s>)", escapeDiscordMarkdown(text), href)
			} else {
				return fmt.Sprintf("[%s](%s)", escapeDiscordMarkdown(text), href)
			}
		},
	}

	mc.nextDiscordUploadID.Store(rand.Int31n(100))

	return mc
}

// resolveMentionedDiscordUserID tries to translate a mentioned user MXID to a
// Discord user ID, regardless of whether a ghost (remote Discord user) or
// Matrix user was mentioned.
func (mc *MessageConverter) resolveMentionedDiscordUserID(
	ctx context.Context,
	portal *bridgev2.Portal,
	mxid id.UserID,
) (string, error) {
	if ghostID, ok := mc.Bridge.Matrix.ParseGhostMXID(mxid); ok {
		// A ghost was mentioned, so we can extract the Discord user ID
		// directly from the MXID.
		return discordid.ParseUserID(ghostID), nil
	}
	// The rest of this method is handling for when a "real" Matrix user was
	// mentioned. This can be the user themselves or someone else in the portal
	// (when split rooms are not in play).

	user, err := mc.Bridge.GetExistingUserByMXID(ctx, mxid)
	if err != nil {
		return "", err
	} else if user == nil {
		return "", nil
	}

	login, _, err := portal.FindPreferredLogin(ctx, user, false)
	if err != nil {
		return "", err
	} else if login == nil {
		return "", nil
	}

	return discordid.ParseUserLoginID(login.ID), nil
}

func (mc *MessageConverter) convertPill(displayname, mxid, eventID string, ctx format.Context) string {
	if len(mxid) == 0 || mxid[0] != '@' {
		// Behave like mautrix-whatsapp.
		return format.DefaultPillConverter(displayname, mxid, eventID, ctx)
	}

	allowedMentions, _ := ctx.ReturnData[formatterContextInputAllowedMentionsKey].([]id.UserID)
	portal := ctx.ReturnData[formatterContextPortalKey].(*bridgev2.Portal)

	mentionedUserID := id.UserID(mxid)
	log := zerolog.Ctx(ctx.Ctx).With().
		Str("mentioned_mxid", mxid).
		Str("mentioned_displayname", displayname).
		Str("event_id", eventID).
		Logger()

	if !slices.Contains(allowedMentions, mentionedUserID) {
		return displayname
	}

	mentionedDiscordUserID, err := mc.resolveMentionedDiscordUserID(ctx.Ctx, portal, mentionedUserID)
	if err != nil {
		log.Err(err).Msg("Failed to resolve the corresponding Discord user ID for the mentioned user, falling back to display name")
		return displayname
	} else if mentionedDiscordUserID == "" {
		log.Error().Msg("Failed to find a corresponding Discord user ID for the mentioned user, falling back to display name")
		return displayname
	}

	return fmt.Sprintf("<@%s>", mentionedDiscordUserID)
}

func (mc *MessageConverter) NextDiscordUploadID() string {
	val := mc.nextDiscordUploadID.Add(2)
	return strconv.Itoa(int(val))
}

// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
)

const discordEpochMillis = 1420070400000

func generateMessageNonce() string {
	snowflake := (time.Now().UnixMilli() - discordEpochMillis) << 22
	// Nonce snowflakes don't have internal IDs or increments
	return strconv.FormatInt(snowflake, 10)
}

func parseAllowedLinkPreviews(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	linkPreviews, ok := raw["com.beeper.linkpreviews"].([]any)
	if !ok {
		return nil
	}
	allowedLinkPreviews := make([]string, 0, len(linkPreviews))
	for _, preview := range linkPreviews {
		previewMap, ok := preview.(map[string]any)
		if !ok {
			continue
		}
		matchedURL, _ := previewMap["matched_url"].(string)
		if matchedURL != "" {
			allowedLinkPreviews = append(allowedLinkPreviews, matchedURL)
		}
	}
	return allowedLinkPreviews
}

// ToDiscord converts a Matrix message into a discordgo.MessageSend that is appropriate
// for bridging the message to Discord.
func (mc *MessageConverter) ToDiscord(
	ctx context.Context,
	msg *bridgev2.MatrixMessage,
) (discordgo.MessageSend, error) {
	var req discordgo.MessageSend
	req.Nonce = generateMessageNonce()

	if msg.ReplyTo != nil {
		req.Reference = &discordgo.MessageReference{
			ChannelID: string(msg.ReplyTo.Room.ID),
			MessageID: string(msg.ReplyTo.ID),
		}
	}

	switch msg.Content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		req.Content, req.AllowedMentions = mc.convertMatrixMessageContent(ctx, msg.Portal, msg.Content, parseAllowedLinkPreviews(msg.Event.Content.Raw))
		if msg.Content.MsgType == event.MsgEmote {
			req.Content = fmt.Sprintf("_%s_", req.Content)
		}
		// TODO: Handle attachments.
	}

	// TODO: Handle (silent) replies and allowed mentions.

	return req, nil
}

func (mc *MessageConverter) convertMatrixMessageContent(ctx context.Context, portal *bridgev2.Portal, content *event.MessageEventContent, allowedLinkPreviews []string) (string, *discordgo.MessageAllowedMentions) {
	allowedMentions := &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		Users:       []string{},
		RepliedUser: true,
	}

	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		ctx := format.NewContext(ctx)
		ctx.ReturnData[formatterContextInputAllowedLinkPreviewsKey] = allowedLinkPreviews
		ctx.ReturnData[formatterContextPortalKey] = portal
		ctx.ReturnData[formatterContextAllowedMentionsKey] = allowedMentions
		if content.Mentions != nil {
			ctx.ReturnData[formatterContextInputAllowedMentionsKey] = content.Mentions.UserIDs
		}
		return variationselector.FullyQualify(matrixHTMLParser.Parse(content.FormattedBody, ctx)), allowedMentions
	} else {
		return variationselector.FullyQualify(escapeDiscordMarkdown(content.Body)), allowedMentions
	}
}

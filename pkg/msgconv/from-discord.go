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
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-discord/pkg/connector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
)

func (mc *MessageConverter) ToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	source *bridgev2.UserLogin,
	msg *discordgo.Message,
) *bridgev2.ConvertedMessage {
	predictedLength := len(msg.Attachments) + len(msg.StickerItems)
	if msg.Content != "" {
		predictedLength++
	}
	parts := make([]*bridgev2.ConvertedMessagePart, 0, predictedLength)
	if textPart := mc.renderDiscordTextMessage(ctx, intent, msg, source); textPart != nil {
		parts = append(parts, textPart)
	}

	log := zerolog.Ctx(ctx)
	handledIDs := make(map[string]struct{})

	for _, att := range msg.Attachments {
		if _, handled := handledIDs[att.ID]; handled {
			continue
		}
		handledIDs[att.ID] = struct{}{}

		log := log.With().Str("attachment_id", att.ID).Logger()
		if part := mc.renderDiscordAttachment(log.WithContext(ctx), intent, msg.ID, att); part != nil {
			parts = append(parts, part)
		}
	}

	for _, sticker := range msg.StickerItems {
		if _, handled := handledIDs[sticker.ID]; handled {
			continue
		}
		handledIDs[sticker.ID] = struct{}{}

		log := log.With().Str("sticker_id", sticker.ID).Logger()
		if part := mc.renderDiscordSticker(log.WithContext(ctx), intent, sticker); part != nil {
			parts = append(parts, part)
		}
	}

	for i, embed := range msg.Embeds {
		// Ignore non-video embeds, they're handled in convertDiscordTextMessage
		if getEmbedType(msg, embed) != EmbedVideo {
			continue
		}
		// Discord deduplicates embeds by URL. It makes things easier for us too.
		if _, handled := handledIDs[embed.URL]; handled {
			continue
		}
		handledIDs[embed.URL] = struct{}{}

		log := log.With().
			Str("computed_embed_type", "video").
			Str("embed_type", string(embed.Type)).
			Int("embed_index", i).
			Logger()
		part := mc.renderDiscordVideoEmbed(log.WithContext(ctx), intent, embed)
		if part != nil {
			parts = append(parts, part)
		}
	}

	if len(parts) == 0 && msg.Thread != nil {
		parts = append(parts, &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    fmt.Sprintf("Created a thread: %s", msg.Thread.Name),
		}})
	}

	// TODO(skip): Add extra metadata.
	// for _, part := range parts {
	// 	puppet.addWebhookMeta(part, msg)
	// 	puppet.addMemberMeta(part, msg)
	// }

	return &bridgev2.ConvertedMessage{Parts: parts}
}

const forwardTemplateHTML = `<blockquote>
<p>↷ Forwarded</p>
%s
<p>%s</p>
</blockquote>`

const msgInteractionTemplateHTML = `<blockquote>
<a href="https://matrix.to/#/%s">%s</a> used <font color="#3771bb">/%s</font>
</blockquote>`

const msgComponentTemplateHTML = `<p>This message contains interactive elements. Use the Discord app to interact with the message.</p>`

func (mc *MessageConverter) renderDiscordTextMessage(ctx context.Context, intent bridgev2.MatrixAPI, msg *discordgo.Message, source *bridgev2.UserLogin) *bridgev2.ConvertedMessagePart {
	log := zerolog.Ctx(ctx)
	if msg.Type == discordgo.MessageTypeCall {
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "started a call",
		}}
	} else if msg.Type == discordgo.MessageTypeGuildMemberJoin {
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "joined the server",
		}}
	}

	var htmlParts []string

	if msg.Interaction != nil {
		ghost, err := mc.bridge.GetGhostByID(ctx, networkid.UserID(msg.Interaction.User.ID))
		// TODO(skip): Try doing ghost.UpdateInfoIfNecessary.
		if err == nil {
			htmlParts = append(htmlParts, fmt.Sprintf(msgInteractionTemplateHTML, ghost.Intent.GetMXID(), ghost.Name, msg.Interaction.Name))
		} else {
			log.Err(err).Msg("Couldn't get ghost by ID while bridging interaction")
		}
	}

	if msg.Content != "" && !isPlainGifMessage(msg) {
		// Bridge basic text messages.
		htmlParts = append(htmlParts, mc.renderDiscordMarkdownOnlyHTML(msg.Content, true))
	} else if msg.MessageReference != nil &&
		msg.MessageReference.Type == discordgo.MessageReferenceTypeForward &&
		len(msg.MessageSnapshots) > 0 &&
		msg.MessageSnapshots[0].Message != nil {
		// Bridge forwarded messages.

		forwardedHTML := mc.renderDiscordMarkdownOnlyHTMLNoUnwrap(msg.MessageSnapshots[0].Message.Content, true)
		msgTSText := msg.MessageSnapshots[0].Message.Timestamp.Format("2006-01-02 15:04 MST")
		origLink := fmt.Sprintf("unknown channel • %s", msgTSText)
		if forwardedFromPortal, err := mc.bridge.DB.Portal.GetByKey(ctx, connector.MakePortalKeyWithID(msg.MessageReference.ChannelID)); err == nil && forwardedFromPortal != nil {
			if origMessage, err := mc.bridge.DB.Message.GetFirstPartByID(ctx, source.ID, networkid.MessageID(msg.MessageReference.MessageID)); err == nil && origMessage != nil {
				// We've bridged the message that was forwarded, so we can link to it directly.
				origLink = fmt.Sprintf(
					`<a href="%s">#%s • %s</a>`,
					forwardedFromPortal.MXID.EventURI(origMessage.MXID, mc.bridge.Matrix.ServerName()),
					forwardedFromPortal.Name,
					msgTSText,
				)
			} else if err != nil {
				log.Err(err).Msg("Couldn't find corresponding message when bridging forwarded message")
			} else if forwardedFromPortal.MXID != "" {
				// We don't have the message but we have the portal, so link to that.
				origLink = fmt.Sprintf(
					`<a href="%s">#%s</a> • %s`,
					forwardedFromPortal.MXID.URI(mc.bridge.Matrix.ServerName()),
					forwardedFromPortal.Name,
					msgTSText,
				)
			} else if forwardedFromPortal.Name != "" {
				// We only have the name of the portal.
				origLink = fmt.Sprintf("%s • %s", forwardedFromPortal.Name, msgTSText)
			}
		} else {
			log.Err(err).Msg("Couldn't find corresponding portal when bridging forwarded message")
		}

		htmlParts = append(htmlParts, fmt.Sprintf(forwardTemplateHTML, forwardedHTML, origLink))
	}

	previews := make([]*event.BeeperLinkPreview, 0)
	for i, embed := range msg.Embeds {
		if i == 0 && msg.MessageReference == nil && isReplyEmbed(embed) {
			continue
		}

		with := log.With().
			Str("embed_type", string(embed.Type)).
			Int("embed_index", i)

		switch getEmbedType(msg, embed) {
		case EmbedRich:
			log := with.Str("computed_embed_type", "rich").Logger()
			htmlParts = append(htmlParts, mc.renderDiscordRichEmbed(log.WithContext(ctx), intent, embed, msg.ID, i))
		case EmbedLinkPreview:
			log := with.Str("computed_embed_type", "link preview").Logger()
			previews = append(previews, mc.renderDiscordLinkEmbed(log.WithContext(ctx), intent, embed))
		case EmbedVideo:
			// Ignore video embeds, they're handled as separate messages.
		default:
			log := with.Logger()
			log.Warn().Msg("Unknown embed type in message")
		}
	}

	if len(msg.Components) > 0 {
		htmlParts = append(htmlParts, msgComponentTemplateHTML)
	}

	if len(htmlParts) == 0 {
		return nil
	}

	fullHTML := strings.Join(htmlParts, "\n")
	if !msg.MentionEveryone {
		fullHTML = strings.ReplaceAll(fullHTML, "@room", "@\u2063ro\u2063om")
	}

	content := format.HTMLToContent(fullHTML)
	extraContent := map[string]any{
		"com.beeper.linkpreviews": previews,
	}

	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &content, Extra: extraContent}
}

func (mc *MessageConverter) renderDiscordVideoEmbed(context context.Context, intent bridgev2.MatrixAPI, embed *discordgo.MessageEmbed) *bridgev2.ConvertedMessagePart {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordSticker(context context.Context, intent bridgev2.MatrixAPI, sticker *discordgo.StickerItem) *bridgev2.ConvertedMessagePart {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordMarkdownOnlyHTML(text string, allowInlineLinks bool) string {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordMarkdownOnlyHTMLNoUnwrap(text string, allowInlineLinks bool) string {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordRichEmbed(context context.Context, intent bridgev2.MatrixAPI, embed *discordgo.MessageEmbed, messageID string, i int) string {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordLinkEmbed(context context.Context, intent bridgev2.MatrixAPI, embed *discordgo.MessageEmbed) *event.BeeperLinkPreview {
	panic("unimplemented")
}

func (mc *MessageConverter) renderDiscordAttachment(context context.Context, intent bridgev2.MatrixAPI, d string, att *discordgo.MessageAttachment) *bridgev2.ConvertedMessagePart {
	panic("unimplemented")
}

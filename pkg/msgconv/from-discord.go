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
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exmaps"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type contextKey int

const (
	contextKeyPortal contextKey = iota
	contextKeyIntent
	contextKeyUserLogin
	contextKeyDiscordClient
)

// ToMatrix bridges a Discord message to Matrix.
//
// This method expects ghost information to be up-to-date.
func (mc *MessageConverter) ToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	intent bridgev2.MatrixAPI,
	source *bridgev2.UserLogin,
	session *discordgo.Session,
	msg *discordgo.Message,
) *bridgev2.ConvertedMessage {
	ctx = context.WithValue(ctx, contextKeyUserLogin, source)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	ctx = context.WithValue(ctx, contextKeyDiscordClient, session)
	predictedLength := len(msg.Attachments) + len(msg.StickerItems)
	if msg.Content != "" {
		predictedLength++
	}
	parts := make([]*bridgev2.ConvertedMessagePart, 0, predictedLength)
	if textPart := mc.renderDiscordTextMessage(ctx, intent, portal, msg, source); textPart != nil {
		parts = append(parts, textPart)
	}

	ctx = zerolog.Ctx(ctx).With().
		Str("action", "convert discord message to matrix").
		Str("message_id", msg.ID).
		Logger().WithContext(ctx)
	log := zerolog.Ctx(ctx)
	handledIDs := make(exmaps.Set[string])

	for _, att := range msg.Attachments {
		if !handledIDs.Add(att.ID) {
			continue
		}

		log := log.With().Str("attachment_id", att.ID).Logger()
		if part := mc.renderDiscordAttachment(log.WithContext(ctx), att); part != nil {
			parts = append(parts, part)
		}
	}

	for _, sticker := range msg.StickerItems {
		if !handledIDs.Add(sticker.ID) {
			continue
		}

		log := log.With().Str("sticker_id", sticker.ID).Logger()
		if part := mc.renderDiscordSticker(log.WithContext(ctx), sticker); part != nil {
			parts = append(parts, part)
		}
	}

	for i, embed := range msg.Embeds {
		// Ignore non-video embeds, they're handled in convertDiscordTextMessage
		if getEmbedType(msg, embed) != EmbedVideo {
			continue
		}
		// Discord deduplicates embeds by URL. It makes things easier for us too.
		if !handledIDs.Add(embed.URL) {
			continue
		}

		log := log.With().
			Str("computed_embed_type", "video").
			Str("embed_type", string(embed.Type)).
			Int("embed_index", i).
			Logger()
		part := mc.renderDiscordVideoEmbed(log.WithContext(ctx), embed)
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

	sender := discordid.MakeUserID(msg.Author.ID)
	pmp, err := portal.PerMessageProfileForSender(ctx, sender)
	if err != nil {
		log.Err(err).Msg("Failed to make per-message profile")
	}

	// Assign incrementing part IDs.
	for i, part := range parts {
		part.ID = networkid.PartID(strconv.Itoa(i))

		// Beeper clients support backfilling backwards (scrolling up to load
		// more messages). Adding per-message profiles to every part helps them
		// present the right message authorship information even when a
		// membership event isn't present.
		part.Content.BeeperPerMessageProfile = &pmp
	}

	converted := &bridgev2.ConvertedMessage{Parts: parts}
	// TODO This is sorta gross; it might be worth bundling these parameters
	// into a struct.
	mc.tryAddingReplyToConvertedMessage(
		ctx,
		converted,
		portal,
		source,
		msg,
	)

	return converted
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

func (mc *MessageConverter) tryAddingReplyToConvertedMessage(
	ctx context.Context,
	converted *bridgev2.ConvertedMessage,
	portal *bridgev2.Portal,
	source *bridgev2.UserLogin,
	msg *discordgo.Message,
) {
	ref := msg.MessageReference
	if ref == nil {
		return
	}
	// TODO: Support threads.

	log := zerolog.Ctx(ctx).With().
		Str("referenced_channel_id", ref.ChannelID).
		Str("referenced_guild_id", ref.GuildID).
		Str("referenced_message_id", ref.MessageID).Logger()

	// The portal containing the message that was replied to.
	targetPortal := portal
	if ref.ChannelID != discordid.ParsePortalID(portal.ID) {
		var err error
		targetPortal, err = mc.Bridge.GetPortalByKey(ctx, discordid.MakePortalKeyWithID(ref.ChannelID))
		if err != nil {
			log.Err(err).Msg("Failed to get cross-room reply portal; proceeding")
			return
		}

		if targetPortal == nil {
			return
		}
	}

	messageID := discordid.MakeMessageID(ref.MessageID)
	repliedToMatrixMsg, err := mc.Bridge.DB.Message.GetFirstPartByID(ctx, source.ID, messageID)
	if err != nil {
		log.Err(err).Msg("Failed to query database for first message part; proceeding")
		return
	}
	if repliedToMatrixMsg == nil {
		log.Debug().Msg("Couldn't find a first message part for reply target; proceeding")
		return
	}

	converted.ReplyTo = &networkid.MessageOptionalPartID{
		MessageID: repliedToMatrixMsg.ID,
		PartID:    &repliedToMatrixMsg.PartID,
	}
	converted.ReplyToRoom = targetPortal.PortalKey
	converted.ReplyToUser = repliedToMatrixMsg.SenderID
}

func (mc *MessageConverter) renderDiscordTextMessage(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, msg *discordgo.Message, source *bridgev2.UserLogin) *bridgev2.ConvertedMessagePart {
	log := zerolog.Ctx(ctx)
	switch msg.Type {
	case discordgo.MessageTypeCall:
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "started a call",
		}}
	case discordgo.MessageTypeGuildMemberJoin:
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "joined the server",
		}}
	}

	var htmlParts []string

	if msg.Interaction != nil {
		ghost, err := mc.Bridge.GetGhostByID(ctx, discordid.MakeUserID(msg.Interaction.User.ID))
		// TODO(skip): Try doing ghost.UpdateInfoIfNecessary.
		if err == nil {
			htmlParts = append(htmlParts, fmt.Sprintf(msgInteractionTemplateHTML, ghost.Intent.GetMXID(), ghost.Name, msg.Interaction.Name))
		} else {
			log.Err(err).Msg("Couldn't get ghost by ID while bridging interaction")
		}
	}

	if msg.Content != "" && !isPlainGifMessage(msg) {
		// Bridge basic text messages.
		htmlParts = append(htmlParts, mc.renderDiscordMarkdownOnlyHTML(portal, msg.Content, true))
	} else if msg.MessageReference != nil &&
		msg.MessageReference.Type == discordgo.MessageReferenceTypeForward &&
		len(msg.MessageSnapshots) > 0 &&
		msg.MessageSnapshots[0].Message != nil {
		// Bridge forwarded messages.
		htmlParts = append(htmlParts, mc.forwardedMessageHTMLPart(ctx, portal, source, msg))
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
			htmlParts = append(htmlParts, mc.renderDiscordRichEmbed(log.WithContext(ctx), embed))
		case EmbedLinkPreview:
			log := with.Str("computed_embed_type", "link preview").Logger()
			previews = append(previews, mc.renderDiscordLinkEmbed(log.WithContext(ctx), embed))
		case EmbedVideo:
			// Video embeds are handled as separate messages via renderDiscordVideoEmbed.
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

func (mc *MessageConverter) forwardedMessageHTMLPart(ctx context.Context, portal *bridgev2.Portal, source *bridgev2.UserLogin, msg *discordgo.Message) string {
	log := zerolog.Ctx(ctx)

	forwardedHTML := mc.renderDiscordMarkdownOnlyHTMLNoUnwrap(portal, msg.MessageSnapshots[0].Message.Content, true)
	msgTSText := msg.MessageSnapshots[0].Message.Timestamp.Format("2006-01-02 15:04 MST")
	origLink := fmt.Sprintf("unknown channel • %s", msgTSText)
	if forwardedFromPortal, err := mc.Bridge.DB.Portal.GetByKey(ctx, discordid.MakePortalKeyWithID(msg.MessageReference.ChannelID)); err == nil && forwardedFromPortal != nil {
		if origMessage, err := mc.Bridge.DB.Message.GetFirstPartByID(ctx, source.ID, discordid.MakeMessageID(msg.MessageReference.MessageID)); err == nil && origMessage != nil {
			// We've bridged the message that was forwarded, so we can link to it directly.
			origLink = fmt.Sprintf(
				`<a href="%s">#%s • %s</a>`,
				forwardedFromPortal.MXID.EventURI(origMessage.MXID, mc.Bridge.Matrix.ServerName()),
				forwardedFromPortal.Name,
				msgTSText,
			)
		} else if err != nil {
			log.Err(err).Msg("Couldn't find corresponding message when bridging forwarded message")
		} else if forwardedFromPortal.MXID != "" {
			// We don't have the message but we have the portal, so link to that.
			origLink = fmt.Sprintf(
				`<a href="%s">#%s</a> • %s`,
				forwardedFromPortal.MXID.URI(mc.Bridge.Matrix.ServerName()),
				forwardedFromPortal.Name,
				msgTSText,
			)
		} else if forwardedFromPortal.Name != "" {
			// We only have the name of the portal.
			origLink = fmt.Sprintf("%s • %s", forwardedFromPortal.Name, msgTSText)
		}
	} else if err != nil {
		log.Err(err).Msg("Couldn't find corresponding portal when bridging forwarded message")
	}

	return fmt.Sprintf(forwardTemplateHTML, forwardedHTML, origLink)
}

func mediaFailedMessage(err error) *event.MessageEventContent {
	return &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", err),
		MsgType: event.MsgNotice,
	}
}

func (mc *MessageConverter) renderDiscordVideoEmbed(ctx context.Context, embed *discordgo.MessageEmbed) *bridgev2.ConvertedMessagePart {
	var proxyURL string
	if embed.Video != nil {
		proxyURL = embed.Video.ProxyURL
	} else if embed.Thumbnail != nil {
		proxyURL = embed.Thumbnail.ProxyURL
	} else {
		zerolog.Ctx(ctx).Warn().Str("embed_url", embed.URL).Msg("No video or thumbnail proxy URL found in embed")
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				Body:    "Failed to bridge media: no video or thumbnail proxy URL found in embed",
				MsgType: event.MsgNotice,
			},
		}
	}

	reupload, err := mc.ReuploadUnknownMedia(ctx, proxyURL, true)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy video embed to Matrix")
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: mediaFailedMessage(err),
		}
	}

	content := &event.MessageEventContent{
		Body: embed.URL,
		URL:  reupload.MXC,
		File: reupload.File,
		Info: &event.FileInfo{
			MimeType: reupload.MimeType,
			Size:     reupload.Size,
		},
	}

	if embed.Video != nil {
		content.MsgType = event.MsgVideo
		content.Info.Width = embed.Video.Width
		content.Info.Height = embed.Video.Height
	} else {
		content.MsgType = event.MsgImage
		content.Info.Width = embed.Thumbnail.Width
		content.Info.Height = embed.Thumbnail.Height
	}

	extra := map[string]any{}
	if content.MsgType == event.MsgVideo && embed.Type == discordgo.EmbedTypeGifv {
		extra["info"] = map[string]any{
			"fi.mau.discord.gifv":  true,
			"fi.mau.gif":           true,
			"fi.mau.loop":          true,
			"fi.mau.autoplay":      true,
			"fi.mau.hide_controls": true,
			"fi.mau.no_audio":      true,
		}
	}

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
		Extra:   extra,
	}
}

func (mc *MessageConverter) renderDiscordSticker(ctx context.Context, sticker *discordgo.StickerItem) *bridgev2.ConvertedMessagePart {
	panic("unimplemented")
}

const (
	embedHTMLWrapper         = `<blockquote class="discord-embed">%s</blockquote>`
	embedHTMLWrapperColor    = `<blockquote class="discord-embed" background-color="#%06X">%s</blockquote>`
	embedHTMLAuthorWithImage = `<p class="discord-embed-author"><img data-mx-emoticon height="24" src="%s" title="Author icon" alt="">&nbsp;<span>%s</span></p>`
	embedHTMLAuthorPlain     = `<p class="discord-embed-author"><span>%s</span></p>`
	embedHTMLAuthorLink      = `<a href="%s">%s</a>`
	embedHTMLTitleWithLink   = `<p class="discord-embed-title"><a href="%s"><strong>%s</strong></a></p>`
	embedHTMLTitlePlain      = `<p class="discord-embed-title"><strong>%s</strong></p>`
	embedHTMLDescription     = `<p class="discord-embed-description">%s</p>`
	embedHTMLFieldName       = `<th>%s</th>`
	embedHTMLFieldValue      = `<td>%s</td>`
	embedHTMLFields          = `<table class="discord-embed-fields"><tr>%s</tr><tr>%s</tr></table>`
	embedHTMLLinearField     = `<p class="discord-embed-field" x-inline="%s"><strong>%s</strong><br><span>%s</span></p>`
	embedHTMLImage           = `<p class="discord-embed-image"><img src="%s" alt="" title="Embed image"></p>`
	embedHTMLFooterWithImage = `<p class="discord-embed-footer"><sub><img data-mx-emoticon height="20" src="%s" title="Footer icon" alt="">&nbsp;<span>%s</span>%s</sub></p>`
	embedHTMLFooterPlain     = `<p class="discord-embed-footer"><sub><span>%s</span>%s</sub></p>`
	embedHTMLFooterOnlyDate  = `<p class="discord-embed-footer"><sub>%s</sub></p>`
	embedHTMLDate            = `<time datetime="%s">%s</time>`
	embedFooterDateSeparator = ` • `
)

func (mc *MessageConverter) renderDiscordRichEmbed(ctx context.Context, embed *discordgo.MessageEmbed) string {
	log := zerolog.Ctx(ctx)
	var htmlParts []string
	if embed.Author != nil {
		var authorHTML string
		authorNameHTML := html.EscapeString(embed.Author.Name)
		if embed.Author.URL != "" {
			authorNameHTML = fmt.Sprintf(embedHTMLAuthorLink, embed.Author.URL, authorNameHTML)
		}
		authorHTML = fmt.Sprintf(embedHTMLAuthorPlain, authorNameHTML)
		if embed.Author.ProxyIconURL != "" {
			reupload, err := mc.ReuploadUnknownMedia(ctx, embed.Author.ProxyIconURL, false)

			if err != nil {
				log.Warn().Err(err).Msg("Failed to reupload author icon in embed")
			} else {
				authorHTML = fmt.Sprintf(embedHTMLAuthorWithImage, reupload.MXC, authorNameHTML)
			}
		}
		htmlParts = append(htmlParts, authorHTML)
	}

	portal := ctx.Value(contextKeyPortal).(*bridgev2.Portal)
	if embed.Title != "" {
		var titleHTML string
		baseTitleHTML := mc.renderDiscordMarkdownOnlyHTML(portal, embed.Title, false)
		if embed.URL != "" {
			titleHTML = fmt.Sprintf(embedHTMLTitleWithLink, html.EscapeString(embed.URL), baseTitleHTML)
		} else {
			titleHTML = fmt.Sprintf(embedHTMLTitlePlain, baseTitleHTML)
		}
		htmlParts = append(htmlParts, titleHTML)
	}

	if embed.Description != "" {
		htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLDescription, mc.renderDiscordMarkdownOnlyHTML(portal, embed.Description, true)))
	}

	for i := 0; i < len(embed.Fields); i++ {
		item := embed.Fields[i]
		// TODO(skip): Port EmbedFieldsAsTables.
		if false {
			splitItems := []*discordgo.MessageEmbedField{item}
			if item.Inline && len(embed.Fields) > i+1 && embed.Fields[i+1].Inline {
				splitItems = append(splitItems, embed.Fields[i+1])
				i++
				if len(embed.Fields) > i+1 && embed.Fields[i+1].Inline {
					splitItems = append(splitItems, embed.Fields[i+1])
					i++
				}
			}
			headerParts := make([]string, len(splitItems))
			contentParts := make([]string, len(splitItems))
			for j, splitItem := range splitItems {
				headerParts[j] = fmt.Sprintf(embedHTMLFieldName, mc.renderDiscordMarkdownOnlyHTML(portal, splitItem.Name, false))
				contentParts[j] = fmt.Sprintf(embedHTMLFieldValue, mc.renderDiscordMarkdownOnlyHTML(portal, splitItem.Value, true))
			}
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLFields, strings.Join(headerParts, ""), strings.Join(contentParts, "")))
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLLinearField,
				strconv.FormatBool(item.Inline),
				mc.renderDiscordMarkdownOnlyHTML(portal, item.Name, false),
				mc.renderDiscordMarkdownOnlyHTML(portal, item.Value, true),
			))
		}
	}

	if embed.Image != nil {
		reupload, err := mc.ReuploadUnknownMedia(ctx, embed.Image.ProxyURL, false)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to reupload image in embed")
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLImage, reupload.MXC))
		}
	}

	var embedDateHTML string
	if embed.Timestamp != "" {
		formattedTime := embed.Timestamp
		parsedTS, err := time.Parse(time.RFC3339, embed.Timestamp)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to parse timestamp in embed")
		} else {
			formattedTime = parsedTS.Format(discordTimestampStyle('F').Format())
		}
		embedDateHTML = fmt.Sprintf(embedHTMLDate, embed.Timestamp, formattedTime)
	}

	if embed.Footer != nil {
		var footerHTML string
		var datePart string
		if embedDateHTML != "" {
			datePart = embedFooterDateSeparator + embedDateHTML
		}
		footerHTML = fmt.Sprintf(embedHTMLFooterPlain, html.EscapeString(embed.Footer.Text), datePart)
		if embed.Footer.ProxyIconURL != "" {
			reupload, err := mc.ReuploadUnknownMedia(ctx, embed.Footer.ProxyIconURL, false)

			if err != nil {
				log.Warn().Err(err).Msg("Failed to reupload footer icon in embed")
			} else {
				footerHTML = fmt.Sprintf(embedHTMLFooterWithImage, reupload.MXC, html.EscapeString(embed.Footer.Text), datePart)
			}
		}
		htmlParts = append(htmlParts, footerHTML)
	} else if embed.Timestamp != "" {
		htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLFooterOnlyDate, embedDateHTML))
	}

	if len(htmlParts) == 0 {
		return ""
	}

	compiledHTML := strings.Join(htmlParts, "")
	if embed.Color != 0 {
		compiledHTML = fmt.Sprintf(embedHTMLWrapperColor, embed.Color, compiledHTML)
	} else {
		compiledHTML = fmt.Sprintf(embedHTMLWrapper, compiledHTML)
	}
	return compiledHTML
}

func (mc *MessageConverter) renderDiscordLinkEmbedImage(
	ctx context.Context, url string, width, height int, preview *event.BeeperLinkPreview,
) {
	reupload, err := mc.ReuploadUnknownMedia(ctx, url, true)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to reupload image in URL preview, ignoring")
		return
	}

	if width != 0 || height != 0 {
		preview.ImageWidth = event.IntOrString(width)
		preview.ImageHeight = event.IntOrString(height)
	}
	preview.ImageSize = event.IntOrString(reupload.Size)
	preview.ImageType = reupload.MimeType
	preview.ImageURL, preview.ImageEncryption = reupload.MXC, reupload.File
}

func (mc *MessageConverter) renderDiscordLinkEmbed(ctx context.Context, embed *discordgo.MessageEmbed) *event.BeeperLinkPreview {
	var preview event.BeeperLinkPreview
	preview.MatchedURL = embed.URL
	preview.Title = embed.Title
	preview.Description = embed.Description
	if embed.Image != nil {
		mc.renderDiscordLinkEmbedImage(ctx, embed.Image.ProxyURL, embed.Image.Width, embed.Image.Height, &preview)
	} else if embed.Thumbnail != nil {
		mc.renderDiscordLinkEmbedImage(ctx, embed.Thumbnail.ProxyURL, embed.Thumbnail.Width, embed.Thumbnail.Height, &preview)
	}
	return &preview
}

func (mc *MessageConverter) renderDiscordAttachment(ctx context.Context, att *discordgo.MessageAttachment) *bridgev2.ConvertedMessagePart {
	// TODO(skip): Support direct media.
	reupload, err := mc.ReuploadMedia(ctx, att.URL, att.ContentType, att.Filename, att.Size, true)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy attachment to Matrix")
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: mediaFailedMessage(err),
		}
	}

	content := &event.MessageEventContent{
		Body: reupload.FileName,
		Info: &event.FileInfo{
			Width:    att.Width,
			Height:   att.Height,
			MimeType: reupload.MimeType,
			Size:     reupload.Size,
		},
	}

	var extra = make(map[string]any)

	if strings.HasPrefix(att.Filename, "SPOILER_") {
		extra["page.codeberg.everypizza.msc4193.spoiler"] = true
	}

	if att.Description != "" {
		content.Body = att.Description
		content.FileName = reupload.FileName
	}

	switch strings.ToLower(strings.Split(content.Info.MimeType, "/")[0]) {
	case "audio":
		content.MsgType = event.MsgAudio
		if att.Waveform != nil {
			// Bridge a voice message.

			// TODO convert waveform
			extra["org.matrix.msc1767.audio"] = map[string]any{
				"duration": int(att.DurationSeconds * 1000),
			}
			extra["org.matrix.msc3245.voice"] = map[string]any{}
		}
	case "image":
		content.MsgType = event.MsgImage
	case "video":
		content.MsgType = event.MsgVideo
	default:
		content.MsgType = event.MsgFile
	}

	content.URL, content.File = reupload.MXC, reupload.File
	content.Info.Size = reupload.Size
	if content.Info.Width == 0 && content.Info.Height == 0 {
		content.Info.Width = att.Width
		content.Info.Height = att.Height
	}

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
		Extra:   extra,
	}
}

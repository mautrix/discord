// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2023 Tulir Asokan
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

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

type ConvertedMessage struct {
	AttachmentID string

	Type    event.Type
	Content *event.MessageEventContent
	Extra   map[string]any
}

func (portal *Portal) createMediaFailedMessage(bridgeErr error) *event.MessageEventContent {
	return &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", bridgeErr),
		MsgType: event.MsgNotice,
	}
}

const DiscordStickerSize = 160

func (portal *Portal) convertDiscordFile(ctx context.Context, typeName string, intent *appservice.IntentAPI, id, url string, content *event.MessageEventContent) *event.MessageEventContent {
	meta := AttachmentMeta{AttachmentID: id, MimeType: content.Info.MimeType}
	if typeName == "sticker" && content.Info.MimeType == "application/json" {
		meta.Converter = portal.bridge.convertLottie
	}
	dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, url, portal.Encrypted, meta)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy attachment to Matrix")
		return portal.createMediaFailedMessage(err)
	}
	if typeName == "sticker" && content.Info.MimeType == "application/json" {
		content.Info.MimeType = dbFile.MimeType
	}
	content.Info.Size = dbFile.Size
	if content.Info.Width == 0 && content.Info.Height == 0 {
		content.Info.Width = dbFile.Width
		content.Info.Height = dbFile.Height
	}
	if dbFile.DecryptionInfo != nil {
		content.File = &event.EncryptedFileInfo{
			EncryptedFile: *dbFile.DecryptionInfo,
			URL:           dbFile.MXC.CUString(),
		}
	} else {
		content.URL = dbFile.MXC.CUString()
	}
	return content
}

func (portal *Portal) cleanupConvertedStickerInfo(content *event.MessageEventContent) {
	if content.Info == nil {
		return
	}
	if content.Info.Width == 0 && content.Info.Height == 0 {
		content.Info.Width = DiscordStickerSize
		content.Info.Height = DiscordStickerSize
	} else if content.Info.Width > DiscordStickerSize || content.Info.Height > DiscordStickerSize {
		if content.Info.Width > content.Info.Height {
			content.Info.Height /= content.Info.Width / DiscordStickerSize
			content.Info.Width = DiscordStickerSize
		} else if content.Info.Width < content.Info.Height {
			content.Info.Width /= content.Info.Height / DiscordStickerSize
			content.Info.Height = DiscordStickerSize
		} else {
			content.Info.Width = DiscordStickerSize
			content.Info.Height = DiscordStickerSize
		}
	}
}

func (portal *Portal) convertDiscordSticker(ctx context.Context, intent *appservice.IntentAPI, sticker *discordgo.StickerItem) *ConvertedMessage {
	var mime string
	switch sticker.FormatType {
	case discordgo.StickerFormatTypePNG:
		mime = "image/png"
	case discordgo.StickerFormatTypeAPNG:
		mime = "image/apng"
	case discordgo.StickerFormatTypeLottie:
		mime = "application/json"
	case discordgo.StickerFormatTypeGIF:
		mime = "image/gif"
	default:
		zerolog.Ctx(ctx).Warn().
			Int("sticker_format", int(sticker.FormatType)).
			Str("sticker_id", sticker.ID).
			Msg("Unknown sticker format")
	}
	content := &event.MessageEventContent{
		Body: sticker.Name, // TODO find description from somewhere?
		Info: &event.FileInfo{
			MimeType: mime,
		},
	}

	mxc := portal.bridge.DMA.StickerMXC(sticker.ID, sticker.FormatType)
	// TODO add config option to use direct media even for lottie stickers
	if mxc.IsEmpty() && mime != "application/json" {
		content = portal.convertDiscordFile(ctx, "sticker", intent, sticker.ID, sticker.URL(), content)
	} else {
		content.URL = mxc.CUString()
	}
	portal.cleanupConvertedStickerInfo(content)
	return &ConvertedMessage{
		AttachmentID: sticker.ID,
		Type:         event.EventSticker,
		Content:      content,
	}
}

func (portal *Portal) convertDiscordAttachment(ctx context.Context, intent *appservice.IntentAPI, messageID string, att *discordgo.MessageAttachment) *ConvertedMessage {
	content := &event.MessageEventContent{
		Body: att.Filename,
		Info: &event.FileInfo{
			Height:   att.Height,
			MimeType: att.ContentType,
			Width:    att.Width,

			// This gets overwritten later after the file is uploaded to the homeserver
			Size: att.Size,
		},
	}
	if att.Description != "" {
		content.Body = att.Description
		content.FileName = att.Filename
	}

	var extra map[string]any

	switch strings.ToLower(strings.Split(att.ContentType, "/")[0]) {
	case "audio":
		content.MsgType = event.MsgAudio
		if att.Waveform != nil {
			// TODO convert waveform
			extra = map[string]any{
				"org.matrix.msc1767.audio": map[string]any{
					"duration": int(att.DurationSeconds * 1000),
				},
				"org.matrix.msc3245.voice": map[string]any{},
			}
		}
	case "image":
		content.MsgType = event.MsgImage
	case "video":
		content.MsgType = event.MsgVideo
	default:
		content.MsgType = event.MsgFile
	}
	mxc := portal.bridge.DMA.AttachmentMXC(portal.Key.ChannelID, messageID, att)
	if mxc.IsEmpty() {
		content = portal.convertDiscordFile(ctx, "attachment", intent, att.ID, att.URL, content)
	} else {
		content.URL = mxc.CUString()
	}
	return &ConvertedMessage{
		AttachmentID: att.ID,
		Type:         event.EventMessage,
		Content:      content,
		Extra:        extra,
	}
}

func (portal *Portal) convertDiscordVideoEmbed(ctx context.Context, intent *appservice.IntentAPI, embed *discordgo.MessageEmbed) *ConvertedMessage {
	attachmentID := fmt.Sprintf("video_%s", embed.URL)
	var proxyURL string
	if embed.Video != nil {
		proxyURL = embed.Video.ProxyURL
	} else if embed.Thumbnail != nil {
		proxyURL = embed.Thumbnail.ProxyURL
	} else {
		zerolog.Ctx(ctx).Warn().Str("embed_url", embed.URL).Msg("No video or thumbnail proxy URL found in embed")
		return &ConvertedMessage{
			AttachmentID: attachmentID,
			Type:         event.EventMessage,
			Content: &event.MessageEventContent{
				Body:    "Failed to bridge media: no video or thumbnail proxy URL found in embed",
				MsgType: event.MsgNotice,
			},
		}
	}
	dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, proxyURL, portal.Encrypted, NoMeta)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy video embed to Matrix")
		return &ConvertedMessage{
			AttachmentID: attachmentID,
			Type:         event.EventMessage,
			Content:      portal.createMediaFailedMessage(err),
		}
	}

	content := &event.MessageEventContent{
		Body: embed.URL,
		Info: &event.FileInfo{
			MimeType: dbFile.MimeType,
			Size:     dbFile.Size,
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
	if content.Info.Width == 0 && content.Info.Height == 0 {
		content.Info.Width = dbFile.Width
		content.Info.Height = dbFile.Height
	}
	if dbFile.DecryptionInfo != nil {
		content.File = &event.EncryptedFileInfo{
			EncryptedFile: *dbFile.DecryptionInfo,
			URL:           dbFile.MXC.CUString(),
		}
	} else {
		content.URL = dbFile.MXC.CUString()
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
	return &ConvertedMessage{
		AttachmentID: attachmentID,
		Type:         event.EventMessage,
		Content:      content,
		Extra:        extra,
	}
}

func (portal *Portal) convertDiscordMessage(ctx context.Context, puppet *Puppet, intent *appservice.IntentAPI, msg *discordgo.Message) []*ConvertedMessage {
	predictedLength := len(msg.Attachments) + len(msg.StickerItems)
	if msg.Content != "" {
		predictedLength++
	}
	parts := make([]*ConvertedMessage, 0, predictedLength)
	if textPart := portal.convertDiscordTextMessage(ctx, intent, msg); textPart != nil {
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
		if part := portal.convertDiscordAttachment(log.WithContext(ctx), intent, msg.ID, att); part != nil {
			parts = append(parts, part)
		}
	}
	for _, sticker := range msg.StickerItems {
		if _, handled := handledIDs[sticker.ID]; handled {
			continue
		}
		handledIDs[sticker.ID] = struct{}{}
		log := log.With().Str("sticker_id", sticker.ID).Logger()
		if part := portal.convertDiscordSticker(log.WithContext(ctx), intent, sticker); part != nil {
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
		part := portal.convertDiscordVideoEmbed(log.WithContext(ctx), intent, embed)
		if part != nil {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 && msg.Thread != nil {
		parts = append(parts, &ConvertedMessage{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    fmt.Sprintf("Created a thread: %s", msg.Thread.Name),
		}})
	}
	for _, part := range parts {
		puppet.addWebhookMeta(part, msg)
		puppet.addMemberMeta(part, msg)
	}
	return parts
}

func (puppet *Puppet) addMemberMeta(part *ConvertedMessage, msg *discordgo.Message) {
	if msg.Member == nil {
		return
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	var avatarURL id.ContentURI
	var discordAvatarURL string
	if msg.Member.Avatar != "" {
		var err error
		avatarURL, discordAvatarURL, err = puppet.bridge.reuploadUserAvatar(puppet.DefaultIntent(), msg.GuildID, msg.Author.ID, msg.Member.Avatar)
		if err != nil {
			puppet.log.Warn().Err(err).
				Str("avatar_id", msg.Member.Avatar).
				Msg("Failed to reupload guild user avatar")
		}
	}
	part.Extra["fi.mau.discord.guild_member_metadata"] = map[string]any{
		"nick":       msg.Member.Nick,
		"avatar_id":  msg.Member.Avatar,
		"avatar_url": discordAvatarURL,
		"avatar_mxc": avatarURL.String(),
	}
	if msg.Member.Nick != "" || !avatarURL.IsEmpty() {
		perMessageProfile := map[string]any{
			"id":          fmt.Sprintf("%s_%s", msg.GuildID, msg.Author.ID),
			"displayname": msg.Member.Nick,
			"avatar_url":  avatarURL.String(),
		}
		if msg.Member.Nick == "" {
			perMessageProfile["displayname"] = puppet.Name
		}
		if avatarURL.IsEmpty() {
			perMessageProfile["avatar_url"] = puppet.AvatarURL.String()
		}
		part.Extra["com.beeper.per_message_profile"] = perMessageProfile
	}
}

func (puppet *Puppet) addWebhookMeta(part *ConvertedMessage, msg *discordgo.Message) {
	if msg.WebhookID == "" {
		return
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	var avatarURL id.ContentURI
	if msg.Author.Avatar != "" {
		var err error
		avatarURL, _, err = puppet.bridge.reuploadUserAvatar(puppet.DefaultIntent(), "", msg.Author.ID, msg.Author.Avatar)
		if err != nil {
			puppet.log.Warn().Err(err).
				Str("avatar_id", msg.Author.Avatar).
				Msg("Failed to reupload webhook avatar")
		}
	}
	part.Extra["fi.mau.discord.webhook_metadata"] = map[string]any{
		"id":         msg.WebhookID,
		"name":       msg.Author.Username,
		"avatar_id":  msg.Author.Avatar,
		"avatar_url": msg.Author.AvatarURL(""),
		"avatar_mxc": avatarURL.String(),
	}
	profileID := sha256.Sum256(fmt.Appendf(nil, "%s:%s", msg.Author.Username, msg.Author.Avatar))
	part.Extra["com.beeper.per_message_profile"] = map[string]any{
		"id":          hex.EncodeToString(profileID[:]),
		"avatar_url":  avatarURL.String(),
		"displayname": msg.Author.Username,
	}
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

func (portal *Portal) convertDiscordRichEmbed(ctx context.Context, intent *appservice.IntentAPI, embed *discordgo.MessageEmbed, msgID string, index int) string {
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
			dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, embed.Author.ProxyIconURL, false, NoMeta)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to reupload author icon in embed")
			} else {
				authorHTML = fmt.Sprintf(embedHTMLAuthorWithImage, dbFile.MXC, authorNameHTML)
			}
		}
		htmlParts = append(htmlParts, authorHTML)
	}
	if embed.Title != "" {
		var titleHTML string
		baseTitleHTML := portal.renderDiscordMarkdownOnlyHTML(embed.Title, false)
		if embed.URL != "" {
			titleHTML = fmt.Sprintf(embedHTMLTitleWithLink, html.EscapeString(embed.URL), baseTitleHTML)
		} else {
			titleHTML = fmt.Sprintf(embedHTMLTitlePlain, baseTitleHTML)
		}
		htmlParts = append(htmlParts, titleHTML)
	}
	if embed.Description != "" {
		htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLDescription, portal.renderDiscordMarkdownOnlyHTML(embed.Description, true)))
	}
	for i := 0; i < len(embed.Fields); i++ {
		item := embed.Fields[i]
		if portal.bridge.Config.Bridge.EmbedFieldsAsTables {
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
				headerParts[j] = fmt.Sprintf(embedHTMLFieldName, portal.renderDiscordMarkdownOnlyHTML(splitItem.Name, false))
				contentParts[j] = fmt.Sprintf(embedHTMLFieldValue, portal.renderDiscordMarkdownOnlyHTML(splitItem.Value, true))
			}
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLFields, strings.Join(headerParts, ""), strings.Join(contentParts, "")))
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLLinearField,
				strconv.FormatBool(item.Inline),
				portal.renderDiscordMarkdownOnlyHTML(item.Name, false),
				portal.renderDiscordMarkdownOnlyHTML(item.Value, true),
			))
		}
	}
	if embed.Image != nil {
		dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, embed.Image.ProxyURL, false, NoMeta)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to reupload image in embed")
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLImage, dbFile.MXC))
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
			dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, embed.Footer.ProxyIconURL, false, NoMeta)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to reupload footer icon in embed")
			} else {
				footerHTML = fmt.Sprintf(embedHTMLFooterWithImage, dbFile.MXC, html.EscapeString(embed.Footer.Text), datePart)
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

type BeeperLinkPreview struct {
	mautrix.RespPreviewURL
	MatchedURL      string                   `json:"matched_url"`
	ImageEncryption *event.EncryptedFileInfo `json:"beeper:image:encryption,omitempty"`
}

func (portal *Portal) convertDiscordLinkEmbedImage(ctx context.Context, intent *appservice.IntentAPI, url string, width, height int, preview *BeeperLinkPreview) {
	dbFile, err := portal.bridge.copyAttachmentToMatrix(intent, url, portal.Encrypted, NoMeta)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to reupload image in URL preview")
		return
	}
	if width != 0 || height != 0 {
		preview.ImageWidth = width
		preview.ImageHeight = height
	} else {
		preview.ImageWidth = dbFile.Width
		preview.ImageHeight = dbFile.Height
	}
	preview.ImageSize = dbFile.Size
	preview.ImageType = dbFile.MimeType
	if dbFile.Encrypted {
		preview.ImageEncryption = &event.EncryptedFileInfo{
			EncryptedFile: *dbFile.DecryptionInfo,
			URL:           dbFile.MXC.CUString(),
		}
	} else {
		preview.ImageURL = dbFile.MXC.CUString()
	}
}

func (portal *Portal) convertDiscordLinkEmbedToBeeper(ctx context.Context, intent *appservice.IntentAPI, embed *discordgo.MessageEmbed) *BeeperLinkPreview {
	var preview BeeperLinkPreview
	preview.MatchedURL = embed.URL
	preview.Title = embed.Title
	preview.Description = embed.Description
	if embed.Image != nil {
		portal.convertDiscordLinkEmbedImage(ctx, intent, embed.Image.ProxyURL, embed.Image.Width, embed.Image.Height, &preview)
	} else if embed.Thumbnail != nil {
		portal.convertDiscordLinkEmbedImage(ctx, intent, embed.Thumbnail.ProxyURL, embed.Thumbnail.Width, embed.Thumbnail.Height, &preview)
	}
	return &preview
}

const msgInteractionTemplateHTML = `<blockquote>
<a href="https://matrix.to/#/%s">%s</a> used <font color="#3771bb">/%s</font>
</blockquote>`

const msgComponentTemplateHTML = `<p>This message contains interactive elements. Use the Discord app to interact with the message.</p>`

type BridgeEmbedType int

const (
	EmbedUnknown BridgeEmbedType = iota
	EmbedRich
	EmbedLinkPreview
	EmbedVideo
)

func isActuallyLinkPreview(embed *discordgo.MessageEmbed) bool {
	// Sending YouTube links creates a video embed, but we want to bridge it as a URL preview,
	// so this is a hacky way to detect those.
	return embed.Video != nil && embed.Video.ProxyURL == ""
}

func getEmbedType(msg *discordgo.Message, embed *discordgo.MessageEmbed) BridgeEmbedType {
	switch embed.Type {
	case discordgo.EmbedTypeLink, discordgo.EmbedTypeArticle:
		return EmbedLinkPreview
	case discordgo.EmbedTypeVideo:
		if isActuallyLinkPreview(embed) {
			return EmbedLinkPreview
		}
		return EmbedVideo
	case discordgo.EmbedTypeGifv:
		return EmbedVideo
	case discordgo.EmbedTypeImage:
		if msg != nil && isPlainGifMessage(msg) {
			return EmbedVideo
		} else if embed.Image == nil && embed.Thumbnail != nil {
			return EmbedLinkPreview
		}
		return EmbedRich
	case discordgo.EmbedTypeRich:
		return EmbedRich
	default:
		return EmbedUnknown
	}
}

func isPlainGifMessage(msg *discordgo.Message) bool {
	if len(msg.Embeds) != 1 {
		return false
	}
	embed := msg.Embeds[0]
	isGifVideo := embed.Type == discordgo.EmbedTypeGifv && embed.Video != nil
	isGifImage := embed.Type == discordgo.EmbedTypeImage && embed.Image == nil && embed.Thumbnail != nil && embed.Title == ""
	contentIsOnlyURL := msg.Content == embed.URL || discordLinkRegexFull.MatchString(msg.Content)
	return contentIsOnlyURL && (isGifVideo || isGifImage)
}

func (portal *Portal) convertDiscordMentions(msg *discordgo.Message, syncGhosts bool) *event.Mentions {
	var matrixMentions event.Mentions
	for _, mention := range msg.Mentions {
		puppet := portal.bridge.GetPuppetByID(mention.ID)
		if syncGhosts {
			puppet.UpdateInfo(nil, mention, nil)
		}
		user := portal.bridge.GetUserByID(mention.ID)
		if user != nil {
			matrixMentions.UserIDs = append(matrixMentions.UserIDs, user.MXID)
		} else {
			matrixMentions.UserIDs = append(matrixMentions.UserIDs, puppet.MXID)
		}
	}
	slices.Sort(matrixMentions.UserIDs)
	matrixMentions.UserIDs = slices.Compact(matrixMentions.UserIDs)
	if msg.MentionEveryone {
		matrixMentions.Room = true
	}
	return &matrixMentions
}

func (portal *Portal) convertDiscordTextMessage(ctx context.Context, intent *appservice.IntentAPI, msg *discordgo.Message) *ConvertedMessage {
	log := zerolog.Ctx(ctx)
	if msg.Type == discordgo.MessageTypeCall {
		return &ConvertedMessage{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "started a call",
		}}
	} else if msg.Type == discordgo.MessageTypeGuildMemberJoin {
		return &ConvertedMessage{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote,
			Body:    "joined the server",
		}}
	}
	var htmlParts []string
	if msg.Interaction != nil {
		puppet := portal.bridge.GetPuppetByID(msg.Interaction.User.ID)
		puppet.UpdateInfo(nil, msg.Interaction.User, nil)
		htmlParts = append(htmlParts, fmt.Sprintf(msgInteractionTemplateHTML, puppet.MXID, puppet.Name, msg.Interaction.Name))
	}
	if msg.Content != "" && !isPlainGifMessage(msg) {
		htmlParts = append(htmlParts, portal.renderDiscordMarkdownOnlyHTML(msg.Content, true))
	}
	previews := make([]*BeeperLinkPreview, 0)
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
			htmlParts = append(htmlParts, portal.convertDiscordRichEmbed(log.WithContext(ctx), intent, embed, msg.ID, i))
		case EmbedLinkPreview:
			log := with.Str("computed_embed_type", "link preview").Logger()
			previews = append(previews, portal.convertDiscordLinkEmbedToBeeper(log.WithContext(ctx), intent, embed))
		case EmbedVideo:
			// Ignore video embeds, they're handled as separate messages
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

	if strings.HasPrefix(msg.Content, "↷ Forwarded") {
		fullHTML = fmt.Sprintf("<blockquote>%s</blockquote>", fullHTML)
	}

	content := format.HTMLToContent(fullHTML)
	extraContent := map[string]any{
		"com.beeper.linkpreviews": previews,
	}

	if msg.WebhookID != "" && msg.ApplicationID == "" && portal.bridge.Config.Bridge.PrefixWebhookMessages {
		content.EnsureHasHTML()
		content.Body = fmt.Sprintf("%s: %s", msg.Author.Username, content.Body)
		content.FormattedBody = fmt.Sprintf("<strong>%s</strong>: %s", html.EscapeString(msg.Author.Username), content.FormattedBody)
	}

	return &ConvertedMessage{Type: event.EventMessage, Content: &content, Extra: extraContent}
}

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
	"net/url"
	"path"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

const componentMediaPartIDPrefix = "component_"

type componentMedia struct {
	Media       discordgo.UnfurledMediaItem
	Description string
	Spoiler     bool
	FileName    string
	Size        int
}

func (cm *componentMedia) key() string {
	if cm.Media.AttachmentID != "" {
		return cm.Media.AttachmentID
	}
	return componentMediaPartIDPrefix + cm.Media.URL
}

func ComponentAttachments(msg *discordgo.Message) []*discordgo.MessageAttachment {
	var atts []*discordgo.MessageAttachment
	for _, item := range collectComponentMedia(msg.Components, false) {
		if att := item.attachment(); att != nil {
			atts = append(atts, att)
		}
	}
	return atts
}

func (cm *componentMedia) attachment() *discordgo.MessageAttachment {
	if cm.Media.AttachmentID == "" {
		return nil
	}
	fileName := cm.FileName
	if fileName == "" {
		if parsed, err := url.Parse(cm.Media.URL); err == nil {
			fileName = path.Base(parsed.Path)
		}
	}
	return &discordgo.MessageAttachment{
		ID:          cm.Media.AttachmentID,
		URL:         cm.Media.URL,
		ProxyURL:    cm.Media.ProxyURL,
		Filename:    fileName,
		Description: cm.Description,
		ContentType: cm.Media.ContentType,
		Width:       cm.Media.Width,
		Height:      cm.Media.Height,
		Size:        cm.Size,
	}
}

func collectComponentMedia(components []discordgo.MessageComponent, spoiler bool) []componentMedia {
	var items []componentMedia
	for _, component := range components {
		switch c := component.(type) {
		case *discordgo.Container:
			items = append(items, collectComponentMedia(c.Components, spoiler || c.Spoiler)...)
		case *discordgo.Section:
			if thumb, ok := c.Accessory.(*discordgo.Thumbnail); ok {
				item := componentMedia{Media: thumb.Media, Spoiler: spoiler || thumb.Spoiler}
				if thumb.Description != nil {
					item.Description = *thumb.Description
				}
				items = append(items, item)
			}
		case *discordgo.MediaGallery:
			for _, galleryItem := range c.Items {
				item := componentMedia{Media: galleryItem.Media, Spoiler: spoiler || galleryItem.Spoiler}
				if galleryItem.Description != nil {
					item.Description = *galleryItem.Description
				}
				items = append(items, item)
			}
		case *discordgo.FileComponent:
			items = append(items, componentMedia{
				Media:    c.File,
				Spoiler:  spoiler || c.Spoiler,
				FileName: c.Name,
				Size:     c.Size,
			})
		}
	}
	return items
}

func (mc *MessageConverter) renderDiscordComponentMedia(
	ctx context.Context,
	source *bridgev2.UserLogin,
	msg *discordgo.Message,
	item *componentMedia,
) *bridgev2.ConvertedMessagePart {
	var part *bridgev2.ConvertedMessagePart
	if att := item.attachment(); att != nil {
		mediaInfo := discordid.NewMediaInfoV1(source.ID, msg.ChannelID, msg.ID, att.ID)
		part = mc.renderDiscordAttachment(ctx, att, &mediaInfo)
	} else {
		part = mc.renderDiscordExternalComponentMedia(ctx, item)
	}
	if part == nil {
		return nil
	}
	if item.Spoiler && part.Content.MsgType != event.MsgNotice {
		if part.Extra == nil {
			part.Extra = make(map[string]any)
		}
		part.Extra["page.codeberg.everypizza.msc4193.spoiler"] = true
	}
	return part
}

func (mc *MessageConverter) renderDiscordExternalComponentMedia(ctx context.Context, item *componentMedia) *bridgev2.ConvertedMessagePart {
	if item.Media.ProxyURL == "" {
		zerolog.Ctx(ctx).Warn().Str("media_url", item.Media.URL).Msg("No proxy URL for external component media")
		return nil
	}
	reupload, err := mc.ReuploadMedia(ctx, item.Media.ProxyURL, item.Media.ContentType, "", 0, true)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy component media to Matrix")
		return conversionFailedPart(err)
	}
	content := &event.MessageEventContent{
		Body: item.Media.URL,
		URL:  reupload.MXC,
		File: reupload.File,
		Info: &event.FileInfo{
			MimeType: reupload.MimeType,
			Size:     reupload.Size,
			Width:    item.Media.Width,
			Height:   item.Media.Height,
		},
	}
	if item.Description != "" {
		content.Body = item.Description
	}
	switch strings.ToLower(strings.Split(reupload.MimeType, "/")[0]) {
	case "image":
		content.MsgType = event.MsgImage
	case "video":
		content.MsgType = event.MsgVideo
	case "audio":
		content.MsgType = event.MsgAudio
	default:
		content.MsgType = event.MsgFile
	}
	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: content}
}

func (mc *MessageConverter) renderDiscordComponentsHTML(
	ctx context.Context,
	portal *bridgev2.Portal,
	source *bridgev2.UserLogin,
	components []discordgo.MessageComponent,
) (string, bool) {
	var parts []string
	interactive := false
	for _, component := range components {
		switch c := component.(type) {
		case *discordgo.TextDisplay:
			if c.Content != "" {
				parts = append(parts, mc.renderDiscordMarkdownOnlyHTMLNoUnwrap(ctx, portal, source, c.Content, true))
			}
		case *discordgo.Section:
			inner, innerInteractive := mc.renderDiscordComponentsHTML(ctx, portal, source, c.Components)
			if inner != "" {
				parts = append(parts, inner)
			}
			interactive = interactive || innerInteractive
			if c.Accessory != nil {
				if _, isThumbnail := c.Accessory.(*discordgo.Thumbnail); !isThumbnail {
					accessory, accessoryInteractive := renderDiscordInteractiveComponents([]discordgo.MessageComponent{c.Accessory})
					if accessory != "" {
						parts = append(parts, accessory)
					}
					interactive = interactive || accessoryInteractive
				}
			}
		case *discordgo.Container:
			inner, innerInteractive := mc.renderDiscordComponentsHTML(ctx, portal, source, c.Components)
			interactive = interactive || innerInteractive
			if inner == "" {
				continue
			}
			if c.Spoiler {
				inner = fmt.Sprintf("<span data-mx-spoiler>%s</span>", inner)
			}
			parts = append(parts, fmt.Sprintf("<blockquote>%s</blockquote>", inner))
		case *discordgo.Separator:
			if c.Divider == nil || *c.Divider {
				parts = append(parts, "<hr>")
			}
		case *discordgo.ActionsRow:
			row, rowInteractive := renderDiscordInteractiveComponents(c.Components)
			if row != "" {
				parts = append(parts, row)
			}
			interactive = interactive || rowInteractive
		case *discordgo.Thumbnail, *discordgo.MediaGallery, *discordgo.FileComponent:
		default:
			interactive = true
		}
	}
	return strings.Join(parts, ""), interactive
}

func renderDiscordInteractiveComponents(components []discordgo.MessageComponent) (string, bool) {
	var labels []string
	interactive := false
	for _, component := range components {
		switch c := component.(type) {
		case *discordgo.Button:
			label := c.Label
			if c.Emoji != nil && c.Emoji.ID == "" && c.Emoji.Name != "" {
				label = strings.TrimSpace(c.Emoji.Name + " " + label)
			}
			if c.Style == discordgo.LinkButton && c.URL != "" {
				if label == "" {
					label = c.URL
				}
				labels = append(labels, fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(c.URL), html.EscapeString(label)))
				continue
			}
			interactive = true
			if label != "" {
				labels = append(labels, fmt.Sprintf("[%s]", html.EscapeString(label)))
			}
		case *discordgo.SelectMenu:
			interactive = true
			if c.Placeholder != "" {
				labels = append(labels, fmt.Sprintf("[%s ▾]", html.EscapeString(c.Placeholder)))
			}
		default:
			interactive = true
		}
	}
	if len(labels) == 0 {
		return "", interactive
	}
	return fmt.Sprintf("<p>%s</p>", strings.Join(labels, " · ")), interactive
}

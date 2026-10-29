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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var (
	_ bridgev2.DirectMediableNetwork = (*DiscordConnector)(nil)
)

func (d *DiscordConnector) Download(
	ctx context.Context,
	mediaID networkid.MediaID,
	params map[string]string,
) (mediaproxy.GetMediaResponse, error) {
	info, err := discordid.ParseMediaID(mediaID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse media id for download: %w", err)
	}

	return d.downloadAttachment(ctx, info)
}

func (d *DiscordConnector) SetUseDirectMedia() {
	d.MsgConv.DirectMedia = true
}

func (d *DiscordConnector) downloadAttachment(
	ctx context.Context,
	info *discordid.MediaInfo,
) (*mediaproxy.GetMediaResponseURL, error) {
	url, expiresAt, err := d.resolveAttachmentURL(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh attachment url for download: %w", err)
	}
	if expiresAt.IsZero() {
		// A zero expiry becomes effectively immutable caching in mediaproxy.
		// Unknown expiry is safer as no-store for now.
		expiresAt = time.Now()
	}
	return &mediaproxy.GetMediaResponseURL{
		URL:       url,
		ExpiresAt: expiresAt,
	}, nil
}

func (d *DiscordConnector) resolveAttachmentURL(ctx context.Context, info *discordid.MediaInfo) (url string, expires time.Time, err error) {
	if entry, ok := d.attachmentCache.Get(info.MediaInfoV1); ok {
		return entry.URL, entry.Expiry, nil
	}

	url, expiresAt, err := d.refreshAttachmentURL(ctx, info)
	if err != nil {
		return "", time.Time{}, err
	}

	d.attachmentCache.Insert(info, url)
	return url, expiresAt, nil
}

func (d *DiscordConnector) refreshAttachmentURL(
	ctx context.Context,
	info *discordid.MediaInfo,
) (url string, expires time.Time, err error) {
	log := zerolog.Ctx(ctx).With().Str("action", "refresh attachment url").Logger()
	ctx = log.WithContext(ctx)

	login, err := d.Bridge.GetExistingUserLoginByID(ctx, info.UserLoginID)
	if err != nil {
		return "", time.Time{}, err
	} else if login == nil {
		return "", time.Time{}, mautrix.MNotFound.WithMessage("Direct media login not found")
	}

	client, ok := login.Client.(*DiscordClient)
	if !ok || client == nil || !client.IsLoggedIn() {
		return "", time.Time{}, mautrix.MNotFound.WithMessage("Direct media login is not connected")
	}

	channelID := info.ChannelID
	messageID := info.MessageID
	attachmentID := info.AttachmentID

	parentChannelID := channelID
	threadChannelID := ""
	threadInfo, err := d.DB.Thread.GetByThreadChannelID(ctx, string(info.UserLoginID), channelID)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to query thread info: %w", err)
	} else if threadInfo != nil {
		parentChannelID = threadInfo.ParentChannelID
		threadChannelID = threadInfo.ThreadChannelID
	}

	var requestOptions []discordgo.RequestOption
	portalKey := discordid.MakeChannelPortalKey(parentChannelID, info.UserLoginID, d.Bridge.Config.SplitPortals)
	portal, err := d.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to query portal for direct media: %w", err)
	} else if portal != nil {
		if meta, ok := portal.Metadata.(*discordid.PortalMetadata); ok {
			requestOptions = append(requestOptions, makeDiscordReferer(meta.GuildID, parentChannelID, threadChannelID))
		}
	} else if threadChannelID == "" {
		// DMs still benefit from @me referers.
		requestOptions = append(requestOptions, makeDiscordReferer("", parentChannelID, ""))
	}

	var messages []*discordgo.Message
	if client.Session.IsUser {
		messages, err = client.Session.ChannelMessages(channelID, 5, "", "", messageID, requestOptions...)
	} else {
		var msg *discordgo.Message
		msg, err = client.Session.ChannelMessage(channelID, messageID, requestOptions...)
		if err == nil && msg != nil {
			messages = []*discordgo.Message{msg}
		}
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to fetch direct media message: %w", err)
	}

	for _, msg := range messages {
		for _, att := range msg.Attachments {
			if att.ID == attachmentID {
				expiresAt := normalizeAttachmentExpiry(parseAttachmentExpiryFromURL(att.URL))
				// (Trace is not the default log level, so this is only visible
				// in development scenarios.)
				log.Trace().
					Str("channel_id", channelID).
					Str("message_id", messageID).
					Str("attachment_id", attachmentID).
					Time("expires_at", expiresAt).
					Msg("Resolved direct media attachment URL")
				// TODO(skip): This is ignoring the rest of the attachments.
				return att.URL, expiresAt, nil
			}
		}
	}

	return "", time.Time{}, mautrix.MNotFound.WithMessage("Attachment not found in message")
}

func parseAttachmentExpiryParam(ex string) time.Time {
	tsBytes, err := hex.DecodeString(ex)
	if err != nil || len(tsBytes) != 4 {
		return time.Time{}
	}

	parsedTS := int64(binary.BigEndian.Uint32(tsBytes))
	now := time.Now()
	expiry := time.Unix(parsedTS, 0)
	if expiry.Before(now) || expiry.After(now.Add(365*24*time.Hour)) {
		// Looks to be invalid.
		return time.Time{}
	}
	return expiry
}

func parseAttachmentExpiryFromURL(rawURL string) time.Time {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}
	}

	return parseAttachmentExpiryParam(parsedURL.Query().Get("ex"))
}

func normalizeAttachmentExpiry(expiry time.Time) time.Time {
	// Default to a validity period of 24 hours.
	if expiry.IsZero() {
		return time.Now().Add(24 * time.Hour)
	}

	return expiry
}

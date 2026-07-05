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
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

const relayWebhookName = "mau bridge"

var discordWebhookUsernameWord = regexp.MustCompile(`(?i)discord`)

func relayWebhookFlags(flags *int) discordgo.MessageFlags {
	if flags == nil {
		return 0
	}
	return discordgo.MessageFlags(*flags) & discordgo.MessageFlagsSuppressEmbeds
}

func prependReplyEmbed(embeds []*discordgo.MessageEmbed, guildID, channelID, messageID string) []*discordgo.MessageEmbed {
	if channelID == "" || messageID == "" {
		return embeds
	}
	guildPart := guildID
	if guildPart == "" {
		guildPart = "@me"
	}
	replyEmbed := &discordgo.MessageEmbed{
		Description: fmt.Sprintf("[Replying to message](https://discord.com/channels/%s/%s/%s)", guildPart, channelID, messageID),
	}
	return append([]*discordgo.MessageEmbed{replyEmbed}, embeds...)
}

func (d *DiscordClient) executeRelayWebhook(
	ctx context.Context,
	portal *bridgev2.Portal,
	channelID string,
	threadID string,
	params *discordgo.WebhookParams,
	refererOpt discordgo.RequestOption,
) (*discordgo.Message, error) {
	webhookID, webhookToken, err := d.getRelayWebhook(ctx, portal, channelID, refererOpt)
	if err != nil {
		return nil, d.tryWrappingError(ctx, err)
	}
	sentMsg, err := d.Session.WebhookThreadExecute(webhookID, webhookToken, true, threadID, params, discordgo.WithContext(ctx))
	if err == nil || !isStaleWebhookError(err) {
		return sentMsg, err
	}
	zerolog.Ctx(ctx).Warn().Err(err).Msg("Relay webhook failed, clearing cached credentials and retrying once")
	if clearErr := d.clearRelayWebhook(ctx, portal); clearErr != nil {
		zerolog.Ctx(ctx).Warn().Err(clearErr).Msg("Failed to clear stale relay webhook metadata")
	}
	webhookID, webhookToken, err = d.getRelayWebhook(ctx, portal, channelID, refererOpt)
	if err != nil {
		return nil, d.tryWrappingError(ctx, err)
	}
	return d.Session.WebhookThreadExecute(webhookID, webhookToken, true, threadID, params, discordgo.WithContext(ctx))
}

func (d *DiscordClient) getRelayWebhook(ctx context.Context, portal *bridgev2.Portal, channelID string, refererOpt discordgo.RequestOption) (id, token string, err error) {
	meta := portal.Metadata.(*discordid.PortalMetadata)
	if meta.RelayWebhookID != "" && meta.RelayWebhookToken != "" {
		return meta.RelayWebhookID, meta.RelayWebhookToken, nil
	}

	webhooks, err := d.Session.ChannelWebhooks(channelID, refererOpt, discordgo.WithContext(ctx))
	if err != nil {
		return "", "", err
	}
	for _, webhook := range webhooks {
		if webhook != nil && webhook.Name == relayWebhookName && webhook.Token != "" {
			meta.RelayWebhookID = webhook.ID
			meta.RelayWebhookToken = webhook.Token
			if err = d.UserLogin.Bridge.DB.Portal.Update(ctx, portal.Portal); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to save relay webhook metadata")
			}
			return webhook.ID, webhook.Token, nil
		}
	}

	webhook, err := d.Session.WebhookCreate(channelID, relayWebhookName, "", refererOpt, discordgo.WithContext(ctx))
	if err != nil {
		return "", "", err
	}
	meta.RelayWebhookID = webhook.ID
	meta.RelayWebhookToken = webhook.Token
	if err = d.UserLogin.Bridge.DB.Portal.Update(ctx, portal.Portal); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to save relay webhook metadata")
	}
	return webhook.ID, webhook.Token, nil
}

func (d *DiscordClient) clearRelayWebhook(ctx context.Context, portal *bridgev2.Portal) error {
	meta := portal.Metadata.(*discordid.PortalMetadata)
	meta.RelayWebhookID = ""
	meta.RelayWebhookToken = ""
	return d.UserLogin.Bridge.DB.Portal.Update(ctx, portal.Portal)
}

func isStaleWebhookError(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) {
		return false
	}
	if restErr.Response != nil && (restErr.Response.StatusCode == http.StatusUnauthorized || restErr.Response.StatusCode == http.StatusNotFound) {
		return true
	}
	return restErr.Message != nil && (restErr.Message.Code == discordgo.ErrCodeUnknownWebhook || restErr.Message.Code == discordgo.ErrCodeInvalidWebhookTokenProvided)
}

func (d *DiscordClient) relayWebhookProfile(ctx context.Context, portal *bridgev2.Portal, sender *bridgev2.OrigSender) (username, avatarURL string) {
	log := zerolog.Ctx(ctx)
	username = relayWebhookUsername(sender)
	if sender.User != nil {
		if login, _, err := portal.FindPreferredLogin(ctx, sender.User, false); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Stringer("sender_mxid", sender.UserID).Msg("No explicit Discord login for relayed sender in portal")
		} else if login != nil {
			if user := d.userCache.Resolve(ctx, discordid.ParseUserLoginID(login.ID)); user != nil {
				log.Debug().
					Stringer("sender_mxid", sender.UserID).
					Str("login_id", string(login.ID)).
					Str("webhook_username", user.DisplayName()).
					Bool("has_avatar_url", user.AvatarURL("256") != "").
					Msg("Resolved relay webhook profile from explicit Discord login")
				return user.DisplayName(), user.AvatarURL("256")
			}
		}
	}
	if ghostID, ok := d.UserLogin.Bridge.Matrix.ParseGhostMXID(sender.UserID); ok {
		if user := d.userCache.Resolve(ctx, discordid.ParseUserID(ghostID)); user != nil {
			log.Debug().
				Stringer("sender_mxid", sender.UserID).
				Str("ghost_id", string(ghostID)).
				Str("webhook_username", user.DisplayName()).
				Bool("has_avatar_url", user.AvatarURL("256") != "").
				Msg("Resolved relay webhook profile from ghost MXID")
			return user.DisplayName(), user.AvatarURL("256")
		}
	}
	if profile := d.resolveRelayGhostByDisplayName(ctx, username); profile != nil {
		log.Debug().
			Stringer("sender_mxid", sender.UserID).
			Str("display_name", username).
			Str("webhook_username", profile.username).
			Bool("has_avatar_url", profile.avatarURL != "").
			Msg("Resolved relay webhook profile from matching Discord ghost display name")
		return profile.username, profile.avatarURL
	}
	log.Debug().
		Stringer("sender_mxid", sender.UserID).
		Str("display_name", username).
		Msg("Falling back to Matrix relay webhook profile")
	return username, ""
}

func sanitizeRelayWebhookUsername(username, fallback string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		username = strings.TrimSpace(fallback)
	}
	username = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, username)
	username = strings.TrimSpace(discordWebhookUsernameWord.ReplaceAllString(username, "dscord"))
	if username == "" {
		username = "Matrix user"
	}
	const maxWebhookUsernameRunes = 80
	if utf8.RuneCountInString(username) > maxWebhookUsernameRunes {
		runes := []rune(username)
		username = string(runes[:maxWebhookUsernameRunes])
	}
	return username
}

func relayWebhookUsername(sender *bridgev2.OrigSender) string {
	if sender.PerMessageProfile.Displayname != "" {
		return sender.PerMessageProfile.Displayname
	}
	if sender.MemberEventContent.Displayname != "" {
		return sender.MemberEventContent.Displayname
	}
	return sender.DisambiguatedName
}

type relayWebhookProfileMatch struct {
	username  string
	avatarURL string
}

type relayWebhookGhostMatch struct {
	userID    string
	name      string
	avatarURL string
}

func chooseRelayWebhookGhostMatch(matches []relayWebhookGhostMatch) *relayWebhookGhostMatch {
	var realAvatarMatches []relayWebhookGhostMatch
	for _, match := range matches {
		if !strings.Contains(match.avatarURL, "cdn.discordapp.com/embed/avatars/") {
			realAvatarMatches = append(realAvatarMatches, match)
		}
	}
	switch {
	case len(realAvatarMatches) == 1:
		return &realAvatarMatches[0]
	case len(realAvatarMatches) > 1:
		return nil
	case len(matches) == 1:
		return &matches[0]
	default:
		return nil
	}
}

func (d *DiscordClient) resolveRelayGhostByDisplayName(ctx context.Context, displayName string) *relayWebhookProfileMatch {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return nil
	}

	rows, err := d.UserLogin.Bridge.DB.Query(ctx, `
		SELECT id, name, avatar_id FROM ghost
		WHERE bridge_id=$1 AND (
			name=$2 OR
			name=$3 OR
			name=$4
		)
	`, d.UserLogin.Bridge.DB.BridgeID, displayName, displayName+" (Discord)", displayName+" (bot) (Discord)")
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("display_name", displayName).Msg("Failed to resolve relay sender against Discord ghosts")
		return nil
	}
	defer rows.Close()

	var matches []relayWebhookGhostMatch
	for rows.Next() {
		var match relayWebhookGhostMatch
		if err = rows.Scan(&match.userID, &match.name, &match.avatarURL); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("display_name", displayName).Msg("Failed to scan relay ghost match")
			return nil
		}
		matches = append(matches, match)
	}
	if err = rows.Err(); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("display_name", displayName).Msg("Failed while resolving relay ghost match")
		return nil
	}
	matched := chooseRelayWebhookGhostMatch(matches)
	if matched == nil {
		zerolog.Ctx(ctx).Debug().
			Str("display_name", displayName).
			Int("match_count", len(matches)).
			Msg("Skipping relay webhook profile match due to ambiguous Discord ghost display names")
		return nil
	}
	return &relayWebhookProfileMatch{
		username:  matched.name,
		avatarURL: matched.avatarURL,
	}
}

func (d *DiscordClient) populateRelayWebhookEditMedia(ctx context.Context, edit *discordgo.WebhookEdit, content *event.MessageEventContent) error {
	switch content.MsgType {
	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
	default:
		return nil
	}
	mediaData, err := d.connector.MsgConv.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to download Matrix attachment for relay webhook edit")
		return bridgev2.ErrMediaDownloadFailed
	}
	filename := content.Body
	if content.FileName != "" {
		filename = content.FileName
	}
	if filename == "" {
		filename = "attachment"
	}
	contentType := ""
	if content.Info != nil {
		contentType = content.Info.MimeType
	}
	edit.Files = []*discordgo.File{{
		Name:        filename,
		ContentType: contentType,
		Reader:      bytes.NewReader(mediaData),
	}}
	attachments := []*discordgo.MessageAttachment{}
	edit.Attachments = &attachments
	return nil
}

func (d *DiscordClient) webhookMessageEditThread(ctx context.Context, webhookID, token, messageID, threadID string, data *discordgo.WebhookEdit) (*discordgo.Message, error) {
	uri := webhookMessageThreadURI(webhookID, token, messageID, threadID)
	var response []byte
	var err error
	if len(data.Files) > 0 {
		contentType, body, encodeErr := discordgo.MultipartBodyWithJSON(data, data.Files)
		if encodeErr != nil {
			return nil, encodeErr
		}
		response, err = d.Session.RequestRaw("PATCH", uri, contentType, body, uri, 0, discordgo.WithContext(ctx))
	} else {
		response, err = d.Session.RequestWithBucketID("PATCH", uri, data, discordgo.EndpointWebhookToken("", ""), discordgo.WithContext(ctx))
	}
	if err != nil {
		return nil, err
	}
	var edited *discordgo.Message
	err = discordgo.Unmarshal(response, &edited)
	return edited, err
}

func (d *DiscordClient) webhookMessageDeleteThread(ctx context.Context, webhookID, token, messageID, threadID string) error {
	uri := webhookMessageThreadURI(webhookID, token, messageID, threadID)
	_, err := d.Session.RequestWithBucketID("DELETE", uri, nil, discordgo.EndpointWebhookToken("", ""), discordgo.WithContext(ctx))
	return err
}

func webhookMessageThreadURI(webhookID, token, messageID, threadID string) string {
	uri := discordgo.EndpointWebhookMessage(webhookID, token, messageID)
	if threadID == "" {
		return uri
	}
	v := url.Values{}
	v.Set("thread_id", threadID)
	return uri + "?" + v.Encode()
}

func (d *DiscordClient) isRelayWebhookMessage(portal *bridgev2.Portal, msg *database.Message) bool {
	if msg == nil {
		return false
	}
	meta := portal.Metadata.(*discordid.PortalMetadata)
	return meta.RelayWebhookID != "" && meta.RelayWebhookToken != "" && string(msg.SenderID) == meta.RelayWebhookID
}

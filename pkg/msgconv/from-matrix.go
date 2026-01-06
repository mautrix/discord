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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
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

func uploadDiscordAttachment(cli *http.Client, url string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}

	for key, value := range discordgo.DroidBaseHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Referer", "https://discord.com/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respData)
	}
	return nil
}

// ToDiscord converts a Matrix message into a discordgo.MessageSend that is appropriate
// for bridging the message to Discord.
func (mc *MessageConverter) ToDiscord(
	ctx context.Context,
	session *discordgo.Session,
	msg *bridgev2.MatrixMessage,
) (*discordgo.MessageSend, error) {
	var req discordgo.MessageSend
	req.Nonce = generateMessageNonce()
	log := zerolog.Ctx(ctx)

	if msg.ReplyTo != nil {
		req.Reference = &discordgo.MessageReference{
			ChannelID: string(msg.ReplyTo.Room.ID),
			MessageID: string(msg.ReplyTo.ID),
		}
	}

	portal := msg.Portal
	channelID := string(portal.ID)
	content := msg.Content

	convertMatrix := func() {
		req.Content, req.AllowedMentions = mc.convertMatrixMessageContent(ctx, msg.Portal, content, parseAllowedLinkPreviews(msg.Event.Content.Raw))
		if content.MsgType == event.MsgEmote {
			req.Content = fmt.Sprintf("_%s_", req.Content)
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		convertMatrix()
	case event.MsgAudio, event.MsgFile, event.MsgVideo:
		mediaData, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
		if err != nil {
			log.Err(err).Msg("Failed to download Matrix attachment for bridging")
			return nil, bridgev2.ErrMediaDownloadFailed
		}

		filename := content.Body
		if content.FileName != "" && content.FileName != content.Body {
			filename = content.FileName
			convertMatrix()
		}
		if msg.Event.Content.Raw["page.codeberg.everypizza.msc4193.spoiler"] == true {
			filename = "SPOILER_" + filename
		}

		// TODO: Support attachments for relay/webhook. (A branch was removed here.)
		att := &discordgo.MessageAttachment{
			ID:       "0",
			Filename: filename,
		}

		upload_id := mc.NextDiscordUploadID()
		log.Debug().Str("upload_id", upload_id).Msg("Preparing attachment")
		prep, err := session.ChannelAttachmentCreate(channelID, &discordgo.ReqPrepareAttachments{
			Files: []*discordgo.FilePrepare{{
				Size: len(mediaData),
				Name: att.Filename,
				ID:   mc.NextDiscordUploadID(),
			}},
			// TODO: Populate with guild ID. Support threads.
		}, discordgo.WithChannelReferer("", channelID))

		if err != nil {
			log.Err(err).Msg("Failed to create attachment in preparation for attachment reupload")
			return nil, bridgev2.ErrMediaReuploadFailed
		}

		prepared := prep.Attachments[0]
		att.UploadedFilename = prepared.UploadFilename

		err = uploadDiscordAttachment(session.Client, prepared.UploadURL, mediaData)
		if err != nil {
			log.Err(err).Msg("Failed to reupload Discord attachment after preparing")
			return nil, bridgev2.ErrMediaReuploadFailed
		}

		req.Attachments = append(req.Attachments, att)
	}

	// TODO: Handle (silent) replies and allowed mentions.

	return &req, nil
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

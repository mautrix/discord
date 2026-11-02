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
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func (d *DiscordConnector) relayMetadata(portal *bridgev2.Portal) discordid.PortalMetadata {
	d.relayLock.RLock()
	defer d.relayLock.RUnlock()
	return *portal.Metadata.(*discordid.PortalMetadata)
}

func (d *DiscordConnector) isWebhookMessage(portal *bridgev2.Portal, message *database.Message) bool {
	meta := d.relayMetadata(portal)
	return message != nil && meta.RelayWebhookID != "" && message.SenderID == discordid.MakeUserID(meta.RelayWebhookID)
}

func webhookError(err error, secret string) error {
	if err == nil {
		return nil
	}
	if secret == "" {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	return fmt.Errorf("webhook request failed: %s", strings.ReplaceAll(err.Error(), secret, "<redacted>"))
}

func (d *DiscordClient) webhookAllowedMentions(ctx context.Context, portal *bridgev2.Portal, sender id.UserID, content string, allowed *discordgo.MessageAllowedMentions) *discordgo.MessageAllowedMentions {
	if allowed == nil {
		allowed = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
	}
	if strings.Contains(content, "@everyone") || strings.Contains(content, "@here") {
		levels, err := d.connector.Bridge.Matrix.GetPowerLevels(ctx, portal.MXID)
		if err == nil && levels.GetUserLevel(sender) >= levels.Notifications.Room() {
			allowed.Parse = append(allowed.Parse, discordgo.AllowedMentionTypeEveryone)
		}
	}
	return allowed
}

func (d *DiscordClient) sendWebhookMessage(ctx context.Context, msg *bridgev2.MatrixMessage, threadID string) (*bridgev2.MatrixMessageResponse, error) {
	meta := d.connector.relayMetadata(msg.Portal)
	if meta.RelayWebhookID == "" || meta.RelayWebhookSecret == "" || meta.GuildID == "" {
		return nil, fmt.Errorf("this channel has no relay webhook")
	}
	converted, err := d.connector.MsgConv.ToDiscord(ctx, d.connector.relayClient, msg, discordid.ParseChannelPortalID(msg.Portal.ID), discordgo.WithContext(ctx), true)
	if err != nil {
		return nil, err
	}
	sender := msg.OrigSender
	name := sender.Displayname
	if name == "" {
		name = sender.UserID.String()
	}
	avatar := ""
	if publicMedia, ok := d.connector.Bridge.Matrix.(bridgev2.MatrixConnectorWithPublicMedia); ok && sender.AvatarURL != "" {
		avatar = publicMedia.GetPublicMediaAddress(sender.AvatarURL)
	}
	params := &discordgo.WebhookParams{
		Content: converted.Content, Username: name, AvatarURL: avatar,
		Files:           converted.Files,
		AllowedMentions: d.webhookAllowedMentions(ctx, msg.Portal, msg.Event.Sender, converted.Content, converted.AllowedMentions),
	}
	if msg.ReplyTo != nil && msg.ReplyTo.ThreadRoot == getMatrixThreadRootID(msg.ThreadRoot) {
		if embed, err := d.webhookReplyEmbed(ctx, msg, threadID); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to create webhook reply embed")
		} else {
			params.Embeds = []*discordgo.MessageEmbed{embed}
		}
	}
	sent, err := d.connector.relayClient.WebhookThreadExecute(meta.RelayWebhookID, meta.RelayWebhookSecret, true, threadID, params, discordgo.WithContext(ctx))
	if err != nil {
		return nil, webhookError(err, meta.RelayWebhookSecret)
	}
	timestamp, _ := discordgo.SnowflakeTimestamp(sent.ID)
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID: discordid.MakeMessageID(sent.ID), SenderID: discordid.MakeUserID(meta.RelayWebhookID), Timestamp: timestamp,
	}}, nil
}

func getMatrixThreadRootID(root *database.Message) networkid.MessageID {
	if root == nil {
		return ""
	}
	if root.ThreadRoot != "" {
		return root.ThreadRoot
	}
	return root.ID
}

func (d *DiscordClient) webhookReplyEmbed(ctx context.Context, msg *bridgev2.MatrixMessage, threadID string) (*discordgo.MessageEmbed, error) {
	target, err := d.connector.Bridge.Bot.GetEvent(ctx, msg.Portal.MXID, msg.ReplyTo.MXID)
	if err != nil {
		return nil, err
	}
	if target.Content.Parsed == nil {
		if err = target.Content.ParseRaw(target.Type); err != nil {
			return nil, err
		}
	}
	content, ok := target.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil, fmt.Errorf("reply target isn't a message")
	}
	copy := *content
	copy.RemoveReplyFallback()
	body := strings.TrimSpace(copy.Body)
	firstLine, _, multiline := strings.Cut(body, "\n")
	runes := []rune(firstLine)
	if len(runes) > 72 {
		firstLine = string(runes[:72]) + "…"
	} else if multiline {
		firstLine += " […]"
	}
	quoted, _ := d.connector.MsgConv.ConvertMatrixMessageContent(ctx, msg.Portal, &event.MessageEventContent{Body: firstLine}, nil)
	author := target.Sender.String()
	if member, err := d.connector.Bridge.Matrix.GetMemberInfo(ctx, msg.Portal.MXID, target.Sender); err == nil && member != nil && member.Displayname != "" {
		author = member.Displayname
	}
	if targetID, ok := d.connector.Bridge.Matrix.ParseGhostMXID(target.Sender); ok {
		author = "<@" + discordid.ParseUserID(targetID) + ">"
	}
	channelID := discordid.ParseChannelPortalID(msg.Portal.ID)
	if threadID != "" {
		channelID = threadID
	}
	url := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", msg.Portal.Metadata.(*discordid.PortalMetadata).GuildID, channelID, discordid.ParseMessageID(msg.ReplyTo.ID))
	return &discordgo.MessageEmbed{Description: fmt.Sprintf("**[Replying to](%s) %s**\n%s", url, author, quoted)}, nil
}

func (d *DiscordClient) webhookTarget(ctx context.Context, portal *bridgev2.Portal, target *database.Message) (discordid.PortalMetadata, string, error) {
	meta := d.connector.relayMetadata(portal)
	if target == nil || !d.connector.isWebhookMessage(portal, target) || meta.RelayWebhookSecret == "" {
		return meta, "", fmt.Errorf("target message wasn't sent through this relay webhook")
	}
	threadID := ""
	if target.ThreadRoot != "" {
		thread, err := d.getThreadByRootMessageID(ctx, discordid.ParseMessageID(target.ThreadRoot))
		if err != nil {
			return meta, "", err
		}
		if thread == nil {
			return meta, "", fmt.Errorf("couldn't find the target message's thread")
		}
		threadID = thread.ThreadChannelID
	}
	return meta, threadID, nil
}

func webhookRequestOptions(ctx context.Context, threadID string) []discordgo.RequestOption {
	options := []discordgo.RequestOption{discordgo.WithContext(ctx)}
	if threadID != "" {
		options = append(options, discordgo.WithQueryParam("thread_id", threadID))
	}
	return options
}

func (d *DiscordClient) editWebhookMessage(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	meta, threadID, err := d.webhookTarget(ctx, msg.Portal, msg.EditTarget)
	if err != nil {
		return err
	}
	if msg.EditTarget.SenderMXID != msg.Event.Sender {
		return fmt.Errorf("you can only edit your own relayed messages")
	}
	content, allowed := d.connector.MsgConv.ConvertMatrixMessageContent(ctx, msg.Portal, msg.Content, nil)
	allowed = d.webhookAllowedMentions(ctx, msg.Portal, msg.Event.Sender, content, allowed)
	_, err = d.connector.relayClient.WebhookMessageEdit(meta.RelayWebhookID, meta.RelayWebhookSecret, discordid.ParseMessageID(msg.EditTarget.ID), &discordgo.WebhookEdit{Content: &content, AllowedMentions: allowed}, webhookRequestOptions(ctx, threadID)...)
	return webhookError(err, meta.RelayWebhookSecret)
}

func (d *DiscordClient) deleteWebhookMessage(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	meta, threadID, err := d.webhookTarget(ctx, msg.Portal, msg.TargetMessage)
	if err != nil {
		return err
	}
	if msg.TargetMessage.SenderMXID != msg.Event.Sender {
		levels, err := d.connector.Bridge.Matrix.GetPowerLevels(ctx, msg.Portal.MXID)
		if err != nil {
			return err
		}
		if levels.GetUserLevel(msg.Event.Sender) < levels.Redact() {
			return fmt.Errorf("you can only delete your own relayed messages")
		}
	}
	err = d.connector.relayClient.WebhookMessageDelete(meta.RelayWebhookID, meta.RelayWebhookSecret, discordid.ParseMessageID(msg.TargetMessage.ID), webhookRequestOptions(ctx, threadID)...)
	if isUnknownMessage(err) {
		return nil
	}
	return webhookError(err, meta.RelayWebhookSecret)
}

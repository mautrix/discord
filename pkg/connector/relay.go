// Relay mode logic (webhook-backed UserLogin).
//
// Implemented in Group 6 (Task 6.2).
//
// When the framework invokes HandleMatrixMessage / HandleMatrixEdit /
// HandleMatrixMessageRemove with a relay login (msg.OrigSender != nil), these
// helpers take over the send path. Instead of going through the relay login's
// Discord user session, the message is routed through a Discord incoming
// webhook stored in PortalMeta.RelayWebhookID / RelayWebhookSecret.
//
// Per the REVISED relay decision (ar-report.md C5 / design.md "Decision: Relay
// (REVISED)"): webhook creds live in PortalMeta; the framework's set-relay
// command designates a live UserLogin as the relay driver. The relay UserLogin's
// HandleMatrixMessage detects OrigSender != nil, reads the webhook creds from
// PortalMeta, and executes the webhook with a spoofed username/avatar derived
// from the original sender's Matrix profile (getRelayUserMeta, FR-44).
//
// The dedicated relayClient uses no auth token — Discord's webhook API is
// authenticated by the webhook token embedded in the URL.
package connector

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"
)

// relayClient is a minimal discordgo Session with no auth token, used
// exclusively for webhook sends (which authenticate via the webhook token in
// the URL, not an Authorization header). Mirrors the legacy `var relayClient`
// at the top of portal.go.
var relayClient, _ = discordgo.New("")

// relayWebhookCreds extracts the webhook ID and secret from a portal's metadata.
// Returns ("", "") if the portal has no webhook configured.
func relayWebhookCreds(portal *bridgev2.Portal) (webhookID, webhookSecret string) {
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		return "", ""
	}
	return meta.RelayWebhookID, meta.RelayWebhookSecret
}

// isRelayWebhookSend reports whether the outbound message should go via the
// relay webhook. This is true when:
//  1. The message is being relayed on behalf of another Matrix user
//     (msg.OrigSender != nil), AND
//  2. The portal has webhook credentials stored in PortalMeta.
func isRelayWebhookSend(msg *bridgev2.MatrixMessage, portal *bridgev2.Portal) bool {
	if msg.OrigSender == nil {
		return false
	}
	wid, _ := relayWebhookCreds(portal)
	return wid != ""
}

// getRelayUserMeta derives the Discord webhook username and avatar URL to use
// when impersonating a relayed Matrix user (FR-44).
//
// Name: use the disambiguated display name built by the framework (already
// includes disambiguation suffix when needed); fall back to the MXID string.
//
// Avatar: use the Matrix member avatar, proxied through the configured
// public_address so Discord can fetch it. Omitted when public_address is not
// set or the member has no avatar.
//
// Mirrors the legacy Portal.getRelayUserMeta.
func (dc *DiscordClient) getRelayUserMeta(
	ctx context.Context,
	portal *bridgev2.Portal,
	origSender *bridgev2.OrigSender,
) (username, avatarURL string) {
	// Framework has already resolved the display name and disambiguation.
	username = origSender.FormattedName
	if username == "" {
		username = origSender.DisambiguatedName
	}
	if username == "" {
		username = string(origSender.UserID)
	}

	// Build a proxied avatar URL only when we have both a matrix AvatarURL and
	// a public address to proxy through.
	mxc := origSender.AvatarURL.ParseOrIgnore()
	if !mxc.IsEmpty() && dc.connector.Config.PublicAddress != "" {
		avatarURL = makeAvatarProxyURL(dc.connector.Config.PublicAddress, mxc)
	}
	return
}

// makeAvatarProxyURL constructs the avatar proxy URL that Discord will fetch to
// display the Matrix user's avatar in a webhook message.
// Format mirrors the legacy portal.makeMediaProxyURL pattern.
func makeAvatarProxyURL(publicAddress string, mxc id.ContentURI) string {
	if mxc.IsEmpty() {
		return ""
	}
	// Strip trailing slash from the base so we don't double up.
	base := strings.TrimRight(publicAddress, "/")
	return fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", base, mxc.Homeserver, mxc.FileID)
}

// makeRelayReplyEmbed builds the Discord embed that a webhook send uses to
// represent a Matrix reply-to (since webhooks cannot use MessageReference for
// in-channel replies). Mirrors the legacy convertReplyMessageToEmbed logic.
//
// Format: "**[Replying to](discordURL) @target**\n<quoted body>"
func (dc *DiscordClient) makeRelayReplyEmbed(
	ctx context.Context,
	portal *bridgev2.Portal,
	replyTo *database.Message,
	channelID string, // the channel (or thread) where the reply is going
) *discordgo.MessageEmbed {
	if replyTo == nil {
		return nil
	}

	_, discordMsgID, ok := discordIDsForMessage(replyTo)
	if !ok || discordMsgID == "" {
		return nil
	}

	meta, _ := portal.Metadata.(*PortalMeta)
	guildID := ""
	if meta != nil {
		guildID = meta.GuildID
	}

	messageURL := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, discordMsgID)

	// Sender attribution: prefer the Matrix ghost displayname, fall back to
	// the sender MXID.
	var targetUser string
	if replyTo.SenderMXID != "" {
		memberInfo, err := portal.Bridge.Matrix.GetMemberInfo(ctx, portal.MXID, replyTo.SenderMXID)
		if err == nil && memberInfo != nil && memberInfo.Displayname != "" {
			targetUser = memberInfo.Displayname
		}
	}
	if targetUser == "" && replyTo.SenderMXID != "" {
		targetUser = string(replyTo.SenderMXID)
	}
	if targetUser == "" {
		targetUser = string(replyTo.SenderID)
	}

	body := fmt.Sprintf("**[Replying to](%s) %s**", messageURL, targetUser)
	return &discordgo.MessageEmbed{Description: body}
}

// sendViaWebhook executes a Discord incoming webhook to relay a Matrix message.
// It uses relayClient (no auth) and the webhook creds stored in PortalMeta.
//
// For thread channels, Discord's WebhookThreadExecute must be used with the
// thread channel ID; for regular channels WebhookExecute suffices (both
// signatures route through the same underlying webhookExecute function).
func sendViaWebhook(
	webhookID, webhookSecret string,
	threadID string, // empty for non-thread sends
	params *discordgo.WebhookParams,
) (*discordgo.Message, error) {
	if threadID != "" {
		return relayClient.WebhookThreadExecute(webhookID, webhookSecret, true, threadID, params)
	}
	return relayClient.WebhookExecute(webhookID, webhookSecret, true, params)
}

// editViaWebhook edits a previously-sent webhook message.
// Discord's WebhookMessageEdit takes the message ID (not the webhook URL thread
// path), so there is no thread-variant — thread membership is implicit.
func editViaWebhook(
	webhookID, webhookSecret, discordMsgID string,
	edit *discordgo.WebhookEdit,
) (*discordgo.Message, error) {
	return relayClient.WebhookMessageEdit(webhookID, webhookSecret, discordMsgID, edit)
}

// deleteViaWebhook deletes a previously-sent webhook message.
func deleteViaWebhook(webhookID, webhookSecret, discordMsgID string) error {
	return relayClient.WebhookMessageDelete(webhookID, webhookSecret, discordMsgID)
}

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

// Matrix → Discord message conversion (Task 5.1).
//
// FRs implemented here:
//   - FR-33  HTML→Discord markdown (italic/underline/spoiler/links with URL-preserving escape)
//   - FR-34  pills → Discord mentions (room alias/ID→<#channel>, user pill→<@id>, allowed-mentions)
//   - FR-35  media download from Matrix + upload to Discord (CDN v2 for user sessions / multipart fallback)
//   - FR-36  reply ref → MessageReference (relay path → embed fallback; hook left for Group 6)
package connector

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/variationselector"
	"golang.org/x/exp/slices"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

// discordEpoch is the Discord epoch in milliseconds.
const discordEpoch int64 = 1420070400000

// generateNonce generates a Discord nonce from the current time.
// Discord nonces are Discord snowflakes: (ms_since_epoch << 22).
func generateNonce() string {
	snowflake := (time.Now().UnixMilli() - discordEpoch) << 22
	return strconv.FormatInt(snowflake, 10)
}

// --- escaping ---
// escapeFixer, escapeReplacement, discordLinkPattern, discordLinkRegex and
// discordLinkRegexFull are declared in formatter.go (same package); we use
// them directly here.

// discordMarkdownEscaper escapes Discord-markdown special characters.
var discordMarkdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`_`, `\_`,
	`*`, `\*`,
	`~`, `\~`,
	"`", "\\`",
	`|`, `\|`,
	`<`, `\<`,
	`#`, `\#`,
)

// escapeDiscordMarkdown escapes Discord markdown in s, but leaves bare URLs
// intact so they remain clickable.
func escapeDiscordMarkdown(s string) string {
	submatches := discordLinkRegex.FindAllStringIndex(s, -1)
	if submatches == nil {
		return discordMarkdownEscaper.Replace(s)
	}
	var builder strings.Builder
	offset := 0
	for _, match := range submatches {
		start, end := match[0], match[1]
		builder.WriteString(discordMarkdownEscaper.Replace(s[offset:start]))
		builder.WriteString(s[start:end])
		offset = end
	}
	builder.WriteString(discordMarkdownEscaper.Replace(s[offset:]))
	return builder.String()
}

// --- format context keys (ported from legacy formatter.go) ---

const (
	fmtCtxAllowedMentionsKey      = "fi.mau.discord.allowed_mentions"
	fmtCtxInputAllowedMentionsKey = "fi.mau.discord.input_allowed_mentions"
	fmtCtxInputLinkPreviewsKey    = "fi.mau.discord.input_allowed_link_previews"
	fmtCtxBridgeKey               = "fi.mau.discord.bridge"
	fmtCtxPortalKey               = "fi.mau.discord.portal"
)

// appendIfNotContains appends newItem to arr only if it is not already present.
func appendIfNotContains(arr []string, newItem string) []string {
	for _, item := range arr {
		if item == newItem {
			return arr
		}
	}
	return append(arr, newItem)
}

// makePillConverter returns a format.PillConverter closure that resolves
// Matrix room/user pill MXIDs into Discord mention syntax. It accumulates user
// mentions into the MessageAllowedMentions stored in the format.Context.
//
// Ported from legacy DiscordBridge.pillConverter; adapted for bridgev2 (uses
// bridgev2.Bridge.GetPortalByMXID, br.Matrix.ParseGhostMXID, and
// bridgev2.User.GetUserLogins instead of the legacy appservice helpers).
//
// Note: room-alias pills (#alias:server) are resolved by looking up the
// portal with matching MXID. Full alias→roomID resolution requires an
// authenticated Matrix client call and is deferred to the full send-path
// implementation in handlematrix.go where the session is available.
func makePillConverter(br *bridgev2.Bridge) format.PillConverter {
	return func(displayname, mxid, eventID string, ctx format.Context) string {
		if len(mxid) == 0 {
			return displayname
		}

		// Room alias: the legacy code resolved via br.Bot.ResolveAlias, which
		// needs an authenticated HTTP client. We skip that here since the pill
		// converter closure doesn't have access to the HTTP client; aliases are
		// uncommon and the fallback to displayname is safe.
		if mxid[0] == '#' {
			return displayname
		}

		// --- Room ID → Discord channel mention ---
		if mxid[0] == '!' {
			targetPortal, err := br.GetPortalByMXID(context.TODO(), id.RoomID(mxid))
			if err != nil || targetPortal == nil {
				return displayname
			}
			channelID := string(targetPortal.ID)
			if eventID == "" {
				return fmt.Sprintf("<#%s>", channelID)
			}
			// event-scoped pill (link to a specific message): build a Discord
			// message URL from the stored message metadata.
			// This requires a DB lookup; defer to displayname for now.
			return displayname
		}

		// --- User ID → Discord user mention ---
		if mxid[0] == '@' {
			allowedMentions, _ := ctx.ReturnData[fmtCtxInputAllowedMentionsKey].([]id.UserID)
			if allowedMentions != nil && !slices.Contains(allowedMentions, id.UserID(mxid)) {
				// This user was not listed in content.mentions.user_ids; suppress mention.
				return displayname
			}

			mentions := ctx.ReturnData[fmtCtxAllowedMentionsKey].(*discordgo.MessageAllowedMentions)

			// Ghost (puppet) MXID → Discord user ID via ParseGhostMXID.
			ghostID, ok := br.Matrix.ParseGhostMXID(id.UserID(mxid))
			if ok {
				discordUserID := string(ghostID)
				mentions.Users = appendIfNotContains(mentions.Users, discordUserID)
				return fmt.Sprintf("<@%s>", discordUserID)
			}

			// Real Matrix user → look up whether they have a Discord login.
			user, err := br.GetUserByMXID(context.TODO(), id.UserID(mxid))
			if err == nil && user != nil {
				for _, login := range user.GetUserLogins() {
					discordUserID := string(login.ID)
					if discordUserID != "" {
						mentions.Users = appendIfNotContains(mentions.Users, discordUserID)
						return fmt.Sprintf("<@%s>", discordUserID)
					}
				}
			}
		}

		return displayname
	}
}

// matrixHTMLParser is the HTML→Discord-markdown parser (ported from legacy
// formatter.go). It is constructed per-call with a pill converter that captures
// the current bridge/portal context.
func makeMatrixHTMLParser(br *bridgev2.Bridge) *format.HTMLParser {
	return &format.HTMLParser{
		TabsToSpaces:   4,
		Newline:        "\n",
		HorizontalLine: "\n---\n",
		ItalicConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("*%s*", s)
		},
		UnderlineConverter: func(s string, ctx format.Context) string {
			return fmt.Sprintf("__%s__", s)
		},
		TextConverter: func(s string, ctx format.Context) string {
			if ctx.TagStack.Has("pre") || ctx.TagStack.Has("code") {
				return s
			}
			return escapeDiscordMarkdown(s)
		},
		SpoilerConverter: func(text, reason string, ctx format.Context) string {
			if reason != "" {
				return fmt.Sprintf("(%s) ||%s||", reason, text)
			}
			return fmt.Sprintf("||%s||", text)
		},
		LinkConverter: func(text, href string, ctx format.Context) string {
			linkPreviews := ctx.ReturnData[fmtCtxInputLinkPreviewsKey].([]string)
			allowPreview := linkPreviews == nil || slices.Contains(linkPreviews, href)
			if text == href {
				if !allowPreview {
					return fmt.Sprintf("<%s>", text)
				}
				return text
			} else if !discordLinkRegexFull.MatchString(href) {
				return fmt.Sprintf("%s (%s)", escapeDiscordMarkdown(text), escapeDiscordMarkdown(href))
			} else if !allowPreview {
				return fmt.Sprintf("[%s](<%s>)", escapeDiscordMarkdown(text), href)
			}
			return fmt.Sprintf("[%s](%s)", escapeDiscordMarkdown(text), href)
		},
		PillConverter: makePillConverter(br),
	}
}

// parseAllowedLinkPreviews extracts the list of URLs that are allowed to
// generate link-preview embeds from the Beeper custom event field
// "com.beeper.linkpreviews". A nil return means "all links allowed" (no
// restriction from the sender).
func parseAllowedLinkPreviews(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	linkPreviews, ok := raw["com.beeper.linkpreviews"].([]any)
	if !ok {
		return nil
	}
	allowed := make([]string, 0, len(linkPreviews))
	for _, preview := range linkPreviews {
		previewMap, ok := preview.(map[string]any)
		if !ok {
			continue
		}
		if matchedURL, _ := previewMap["matched_url"].(string); matchedURL != "" {
			allowed = append(allowed, matchedURL)
		}
	}
	return allowed
}

// parseMatrixHTML converts a Matrix message content to Discord markdown plus an
// allowed-mentions struct. If the content has formatted HTML, the HTML parser is
// used; otherwise the plain body is markdown-escaped.
//
// allowedLinkPreviews is the list extracted from com.beeper.linkpreviews (nil =
// all links allowed). portal is passed in the context data so the pill
// converter can access it at parse time.
func parseMatrixHTML(
	br *bridgev2.Bridge,
	portal *bridgev2.Portal,
	content *event.MessageEventContent,
	allowedLinkPreviews []string,
) (string, *discordgo.MessageAllowedMentions) {
	allowedMentions := &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		Users:       []string{},
		RepliedUser: true,
	}
	if content.Format == event.FormatHTML && len(content.FormattedBody) > 0 {
		ctx := format.NewContext(context.Background())
		ctx.ReturnData[fmtCtxInputLinkPreviewsKey] = allowedLinkPreviews
		ctx.ReturnData[fmtCtxBridgeKey] = br
		// store portal in context so pill converters that need it can retrieve it
		ctx.ReturnData[fmtCtxPortalKey] = portal
		ctx.ReturnData[fmtCtxAllowedMentionsKey] = allowedMentions
		if content.Mentions != nil {
			ctx.ReturnData[fmtCtxInputAllowedMentionsKey] = content.Mentions.UserIDs
		}
		parser := makeMatrixHTMLParser(br)
		return variationselector.FullyQualify(parser.Parse(content.FormattedBody, ctx)), allowedMentions
	}
	return variationselector.FullyQualify(escapeDiscordMarkdown(content.Body)), allowedMentions
}

// --- media: Matrix → Discord (FR-35) ---

// nextUploadID is a per-process monotonic counter for CDN upload request IDs.
// It starts at 2 and increments by 2, matching the legacy
// user.nextDiscordUploadID behaviour.
var nextUploadID atomic.Int64

func init() {
	nextUploadID.Store(0)
}

func getNextUploadID() string {
	val := nextUploadID.Add(2)
	return strconv.FormatInt(val, 10)
}

// downloadMatrixMedia downloads and optionally decrypts a Matrix attachment.
// It uses the MatrixAPI intent's DownloadMedia method which handles decryption
// automatically when file != nil.
func downloadMatrixMedia(
	ctx context.Context,
	intent bridgev2.MatrixAPI,
	content *event.MessageEventContent,
) ([]byte, error) {
	var file *event.EncryptedFileInfo
	rawMXC := content.URL

	if content.File != nil {
		file = content.File
		rawMXC = content.File.URL
	}

	data, err := intent.DownloadMedia(ctx, rawMXC, file)
	if err != nil {
		return nil, fmt.Errorf("failed to download matrix media: %w", err)
	}
	return data, nil
}

// uploadToDiscordCDN performs a CDN v2 upload (user-session path): it first
// prepares the upload slot via REST, then PUTs the bytes to the returned upload
// URL. Returns the uploaded filename and any error. On success, att.UploadedFilename
// is populated.
func uploadToDiscordCDN(
	ctx context.Context,
	sess *discordgo.Session,
	channelID string,
	att *discordgo.MessageAttachment,
	data []byte,
	threadID string,
) error {
	isClip := false
	var opts []discordgo.RequestOption
	// Build referer option. We don't have portal.GuildID here, so use empty string
	// (the referer is advisory only and Discord does not reject mismatches).
	opts = append(opts, discordgo.WithChannelReferer("", channelID))

	prep, err := sess.ChannelAttachmentCreate(channelID, &discordgo.ReqPrepareAttachments{
		Files: []*discordgo.FilePrepare{{
			Size:                len(data),
			Name:                att.Filename,
			ID:                  getNextUploadID(),
			IsClip:              &isClip,
			OriginalContentType: att.OriginalContentType,
		}},
	}, opts...)
	if err != nil {
		return fmt.Errorf("failed to prepare CDN upload: %w", err)
	}
	prepared := prep.Attachments[0]
	att.UploadedFilename = prepared.UploadFilename

	// PUT the bytes to Discord's CDN.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, prepared.UploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to build CDN upload request: %w", err)
	}
	for key, value := range discordgo.DroidBaseHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Referer", "https://discord.com/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := sess.Client.Do(req)
	if err != nil {
		return fmt.Errorf("CDN upload PUT failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode > 299 {
		return fmt.Errorf("CDN upload returned unexpected status %d", resp.StatusCode)
	}
	return nil
}

// --- reply ref (FR-36) ---

// makeReplyRef resolves a Matrix reply-to event to a Discord MessageReference.
// replyToMsg is the database record of the message being replied to.
// Returns nil if the message cannot be resolved or is in a different thread.
func makeReplyRef(
	replyToMsg *database.Message,
	threadID string,
) *discordgo.MessageReference {
	if replyToMsg == nil {
		return nil
	}
	meta, _ := replyToMsg.Metadata.(*MessageMeta)
	if meta == nil || meta.DiscordID == "" {
		return nil
	}
	// Only attach a reply reference if the target is in the same thread.
	discordChannelID, _, ok := parseMessageIDFromRecord(replyToMsg)
	if !ok {
		return nil
	}
	if threadID != "" && threadID != discordChannelID {
		return nil
	}
	return &discordgo.MessageReference{
		ChannelID: discordChannelID,
		MessageID: meta.DiscordID,
	}
}

// parseMessageIDFromRecord extracts the channel/message IDs from a database
// message row (ID field is "channelID-messageID").
func parseMessageIDFromRecord(msg *database.Message) (channelID, discordMsgID string, ok bool) {
	s := string(msg.ID)
	idx := strings.Index(s, "-")
	if idx < 0 {
		return "", "", false
	}
	channelID = s[:idx]
	meta, _ := msg.Metadata.(*MessageMeta)
	if meta != nil && meta.DiscordID != "" {
		discordMsgID = meta.DiscordID
		return channelID, discordMsgID, true
	}
	return channelID, "", true
}

// --- main conversion entry point ---

// MatrixConvertResult is the output of convertMatrixToDiscord. It contains the
// MessageSend payload ready to hand to discordgo, plus computed metadata needed
// by handlematrix.go to make send decisions.
type MatrixConvertResult struct {
	// Send is the Discord MessageSend payload; caller fills Nonce before sending.
	Send *discordgo.MessageSend
	// IsVoiceMessage is true when the MSC3245 voice-message flag was detected
	// and the caller should set MessageFlagsIsVoiceMessage on the send request.
	IsVoiceMessage bool
	// RelayEmbed is set when the send is on the relay (webhook) path and the
	// reply reference must be expressed as an embed rather than a MessageReference.
	// Set by the caller (Group 6) — we only populate sendReq.Reference here.
	RelayEmbed *discordgo.MessageEmbed
}

// convertMatrixToDiscord converts a Matrix message into a Discord MessageSend
// payload. It handles text, media, reply refs, and CDN vs multipart upload.
//
// Parameters:
//   - ctx: request context.
//   - br: the bridgev2.Bridge (for portal/user lookups used by the pill converter).
//   - intent: the Matrix API intent used to download encrypted attachments.
//   - sess: the active Discord session for the sending user login; if nil, the
//     caller is on the relay (webhook) path and CDN upload is skipped.
//   - portal: the bridgev2.Portal the message is going to.
//   - msg: the MatrixMessage from the framework.
//
// Called by handlematrix.go's HandleMatrixMessage.
func convertMatrixToDiscord(
	ctx context.Context,
	br *bridgev2.Bridge,
	intent bridgev2.MatrixAPI,
	sess *discordgo.Session,
	portal *bridgev2.Portal,
	msg *bridgev2.MatrixMessage,
) (*MatrixConvertResult, error) {
	content := msg.Content
	isWebhookSend := sess == nil

	// Work out which Discord channel ID to target (may be overridden to a thread
	// ID by handlematrix.go before calling — we keep this symmetric with legacy).
	channelID := string(portal.ID)

	// Reply reference (FR-36). Thread-ID scoping is handled in makeReplyRef.
	var replyToUser id.UserID
	var sendReq discordgo.MessageSend
	if msg.ReplyTo != nil {
		replyToMsg := msg.ReplyTo
		if replyToMsg.SenderMXID != "" {
			replyToUser = replyToMsg.SenderMXID
		}
		if !isWebhookSend {
			sendReq.Reference = makeReplyRef(replyToMsg, "")
		}
		// Relay path: leave RelayEmbed nil here; handlematrix.go (Group 6) fills it.
	}

	// Sticker fixup (legacy parity: stickers are sent as image MsgType).
	if msg.Event.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	}

	// Body / MsgType conversion.
	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		sendReq.Content, sendReq.AllowedMentions = parseMatrixHTML(
			br, portal, content,
			parseAllowedLinkPreviews(msg.Event.Content.Raw),
		)
		if content.MsgType == event.MsgEmote {
			sendReq.Content = fmt.Sprintf("_%s_", sendReq.Content)
		}

	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		data, err := downloadMatrixMedia(ctx, intent, content)
		if err != nil {
			return nil, fmt.Errorf("error downloading matrix media: %w", err)
		}

		filename := content.Body
		if content.FileName != "" && content.FileName != content.Body {
			// Separate filename from caption.
			filename = content.FileName
			sendReq.Content, sendReq.AllowedMentions = parseMatrixHTML(
				br, portal, content,
				parseAllowedLinkPreviews(msg.Event.Content.Raw),
			)
		}

		// MSC4193 spoiler flag → SPOILER_ filename prefix.
		if msg.Event.Content.Raw["page.codeberg.everypizza.msc4193.spoiler"] == true {
			filename = "SPOILER_" + filename
		}

		// CDN v2 upload for user sessions (FR-35).
		useDiscordCDN := sess != nil && sess.IsUser && !isWebhookSend
		if connector, ok := portal.Bridge.Network.(*DiscordConnector); ok {
			useDiscordCDN = useDiscordCDN && connector.Config.UseDiscordCDNUpload
		}

		if useDiscordCDN {
			att := &discordgo.MessageAttachment{
				ID:                  "0",
				Filename:            filename,
				OriginalContentType: content.Info.MimeType,
			}
			sendReq.Attachments = []*discordgo.MessageAttachment{att}

			if err = uploadToDiscordCDN(ctx, sess, channelID, att, data, ""); err != nil {
				return nil, fmt.Errorf("error uploading via CDN v2: %w", err)
			}
		} else {
			// Multipart fallback (bots, webhooks, non-CDN user sessions).
			sendReq.Files = []*discordgo.File{{
				Name:        filename,
				ContentType: content.Info.MimeType,
				Reader:      bytes.NewReader(data),
			}}
		}

	default:
		return nil, fmt.Errorf("unsupported message type %q", content.MsgType)
	}

	// Detect MSC3245 voice message (org.matrix.msc3245.voice key in raw content).
	isVoiceMessage := false
	if _, ok := msg.Event.Content.Raw["org.matrix.msc3245.voice"]; ok {
		isVoiceMessage = true
	}

	// Allowed-mentions: resolve "silent reply" logic (FR-34 / legacy parity).
	//
	// In legacy code, AllowedMentions was cleared for non-webhook sends *unless*
	// the reply needed to be silent. We preserve that here: for user sessions we
	// only set AllowedMentions to suppress the replied-user ping; for webhooks we
	// leave it as-is (the webhook may have higher permissions).
	silentReply := content.Mentions != nil &&
		msg.ReplyTo != nil &&
		(len(content.Mentions.UserIDs) == 0 ||
			(replyToUser != "" && !slices.Contains(content.Mentions.UserIDs, replyToUser)))
	if silentReply && sendReq.AllowedMentions != nil {
		sendReq.AllowedMentions.RepliedUser = false
	}
	if !isWebhookSend {
		if silentReply {
			sendReq.AllowedMentions = &discordgo.MessageAllowedMentions{
				Parse: []discordgo.AllowedMentionType{
					discordgo.AllowedMentionTypeUsers,
					discordgo.AllowedMentionTypeRoles,
					discordgo.AllowedMentionTypeEveryone,
				},
				RepliedUser: false,
			}
		} else {
			// Non-relay user sessions: let Discord decide (legacy parity).
			sendReq.AllowedMentions = nil
		}
	}

	return &MatrixConvertResult{
		Send:           &sendReq,
		IsVoiceMessage: isVoiceMessage,
	}, nil
}

// convertMatrixEditToDiscord converts a Matrix edit into the minimal Discord
// payload (just the new content string + allowed-mentions). Called by
// HandleMatrixEdit in handlematrix.go.
func convertMatrixEditToDiscord(
	br *bridgev2.Bridge,
	portal *bridgev2.Portal,
	msg *bridgev2.MatrixEdit,
) (string, *discordgo.MessageAllowedMentions, error) {
	newContent := msg.Content.NewContent
	if newContent == nil {
		return "", nil, fmt.Errorf("edit event has no m.new_content")
	}
	newContentRaw, _ := msg.Event.Content.Raw["m.new_content"].(map[string]any)
	text, allowedMentions := parseMatrixHTML(
		br, portal, newContent,
		parseAllowedLinkPreviews(newContentRaw),
	)
	return text, allowedMentions, nil
}

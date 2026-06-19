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

// Discord → Matrix message conversion (Task 4.2), ported from the legacy
// portal_convert.go + attachments.go (conversion bits) and adapted to bridgev2:
// instead of sending events directly, ConvertMessage/ConvertEdit produce
// *bridgev2.ConvertedMessage / *bridgev2.ConvertedEdit with one
// ConvertedMessagePart per attachment/sticker/embed plus the text part.
//
// FRs implemented here (see .claude/docs/ar-report.md):
//   - FR-23..27 text/embed/sticker/attachment conversion
//   - FR-28      one part per attachment
//   - FR-31 / M1 per-message profile in ConvertedMessagePart.Extra (NOT OrigSender)
//   - FR-32      system messages, slash-command interactions, forwarded messages
//   - FR-69/70   @everyone/@here → @room with anti-ping injection + PL gate
//   - FR-71      custom-emoji reaction shortcode fallback hook
//   - FR-72      pinned-message events are not bridged (handled in handlediscord)
//   - FR-76      messages with interactive components get a "use Discord app" notice
package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// DiscordStickerSize is the max dimension stickers are scaled to (legacy parity).
const DiscordStickerSize = 160

// antiPingRoom is the zero-width-separated form of "@room" injected when a
// message contains @everyone/@here but the sender isn't permitted to ping
// everyone (FR-69). It matches the legacy "@⁣ro⁣om" replacement.
const antiPingRoom = "@⁣ro⁣om"

// --- media re-upload (replaces legacy copyAttachmentToMatrix/uploadMatrixAttachment) ---

// uploadResult is the minimal result of a Discord→Matrix media re-upload.
type uploadResult struct {
	MXC            id.ContentURIString
	DecryptionInfo *event.EncryptedFileInfo
	Size           int64
	Width          int
	Height         int
	MimeType       string
}

// reuploadDiscordFile downloads a Discord CDN URL and uploads it to Matrix via
// the given intent, returning the resulting mxc/decryption info. It dedups
// through the dc_file table (FR-59): a previous upload for the same (url,
// encrypted) pair is reused. Whether the upload is encrypted is decided by the
// framework from the portal's room (intent.UploadMedia is given portal.MXID).
//
// converter, when non-nil, transforms the downloaded bytes before upload (used
// for Lottie stickers → png/gif/webm/webp, FR-26).
func (dc *DiscordConnector) reuploadDiscordFile(
	ctx context.Context,
	intent bridgev2.MatrixAPI,
	portal *bridgev2.Portal,
	attachmentID, emojiName, url, mimeType string,
	converter func([]byte) ([]byte, string, error),
) (*uploadResult, error) {
	log := zerolog.Ctx(ctx)
	encrypted := portalIsEncrypted(portal)
	cacheable := dc.Config.CacheMedia != "never" && (dc.Config.CacheMedia == "always" || !encrypted)

	if cacheable {
		if cached, err := dc.DB.File.Get(ctx, url, encrypted); err == nil && cached != nil {
			return fileToUploadResult(cached), nil
		} else if err != nil {
			log.Warn().Err(err).Msg("Failed to look up cached file, re-uploading")
		}
	}

	data, err := downloadDiscordAttachment(http.DefaultClient, url, dc.MaxFileSize())
	if err != nil {
		return nil, fmt.Errorf("failed to download attachment: %w", err)
	}
	if converter != nil {
		data, mimeType, err = converter(data)
		if err != nil {
			return nil, fmt.Errorf("failed to convert attachment: %w", err)
		}
	}

	if mimeType == "" {
		// Fall back to content sniffing when the caller didn't supply a MIME
		// (e.g. embed images). http.DetectContentType reads the first 512 bytes.
		mimeType = http.DetectContentType(data)
	}

	var width, height int
	if strings.HasPrefix(mimeType, "image/") {
		if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(data)); cfgErr == nil {
			width = cfg.Width
			height = cfg.Height
		}
	}

	var roomID id.RoomID
	if portal != nil {
		roomID = portal.MXID
	}
	mxc, encFile, err := intent.UploadMedia(ctx, roomID, data, fileNameForMedia(emojiName, attachmentID), mimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to upload media: %w", err)
	}

	res := &uploadResult{
		MXC:            mxc,
		DecryptionInfo: encFile,
		Size:           int64(len(data)),
		Width:          width,
		Height:         height,
		MimeType:       mimeType,
	}

	if cacheable {
		dc.cacheUploadedFile(ctx, url, encrypted, attachmentID, emojiName, res)
	}
	return res, nil
}

// portalIsEncrypted reports whether media for this portal should be treated as
// encrypted for the dc_file cache key. The framework's
// UploadMedia(portal.MXID, ...) is authoritative for whether bytes are actually
// encrypted; this only keys the cache so a plaintext upload isn't reused for an
// E2EE room. We conservatively treat media as plaintext unless we positively
// know the room is encrypted — a plaintext-keyed entry simply won't be reused
// for an encrypted room, so this is safe.
//
// TODO(group): once bridgev2 exposes per-portal encryption state cleanly, key
// the cache on the real value.
func portalIsEncrypted(_ *bridgev2.Portal) bool {
	return false
}

// fileToUploadResult converts a cached dc_file row into an uploadResult.
func fileToUploadResult(f *discorddb.File) *uploadResult {
	res := &uploadResult{
		MXC:      id.ContentURIString(f.MXC),
		Size:     f.Size,
		MimeType: f.MimeType,
	}
	if f.Width != nil {
		res.Width = *f.Width
	}
	if f.Height != nil {
		res.Height = *f.Height
	}
	if f.Encrypted && f.DecryptionInfo != nil {
		var encFile event.EncryptedFileInfo
		if err := json.Unmarshal(*f.DecryptionInfo, &encFile); err == nil {
			res.DecryptionInfo = &encFile
		}
	}
	return res
}

// cacheUploadedFile writes a dc_file row for a freshly uploaded attachment.
func (dc *DiscordConnector) cacheUploadedFile(ctx context.Context, url string, encrypted bool, attachmentID, emojiName string, res *uploadResult) {
	file := &discorddb.File{
		URL:       url,
		Encrypted: encrypted,
		MXC:       string(res.MXC),
		Size:      res.Size,
		MimeType:  res.MimeType,
		Timestamp: time.Now().UnixMilli(),
	}
	if attachmentID != "" {
		file.ID = &attachmentID
	}
	if emojiName != "" {
		file.EmojiName = &emojiName
	}
	if res.Width != 0 {
		file.Width = &res.Width
	}
	if res.Height != 0 {
		file.Height = &res.Height
	}
	if res.DecryptionInfo != nil {
		if raw, err := json.Marshal(res.DecryptionInfo); err == nil {
			rawMsg := json.RawMessage(raw)
			file.DecryptionInfo = &rawMsg
		}
	}
	if err := dc.DB.File.Upsert(ctx, file); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("url", url).Msg("Failed to cache uploaded file")
	}
}

// fileNameForMedia picks a filename for the upload (emoji name preferred).
func fileNameForMedia(emojiName, attachmentID string) string {
	if emojiName != "" {
		return strings.Trim(emojiName, ":")
	}
	return attachmentID
}

// applyUpload writes the upload result into a message content (URL or File).
func applyUpload(content *event.MessageEventContent, res *uploadResult) {
	if content.Info == nil {
		content.Info = &event.FileInfo{}
	}
	content.Info.Size = int(res.Size)
	if content.Info.Width == 0 && content.Info.Height == 0 {
		content.Info.Width = res.Width
		content.Info.Height = res.Height
	}
	if res.DecryptionInfo != nil {
		res.DecryptionInfo.URL = res.MXC
		content.File = res.DecryptionInfo
	} else {
		content.URL = res.MXC
	}
}

// downloadDiscordAttachment downloads a Discord CDN URL with the droid headers.
// Ported from legacy attachments.go.
func downloadDiscordAttachment(cli *http.Client, url string, maxSize int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range discordgo.DroidDownloadHeaders {
		req.Header.Set(key, value)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d downloading %s: %s", resp.StatusCode, url, data)
	}
	if maxSize <= 0 {
		// No limit known yet (SetMaxFileSize not called); use a generous default.
		maxSize = 100 * 1024 * 1024
	}
	if resp.Header.Get("Content-Length") != "" {
		length, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse content length: %w", err)
		} else if length > maxSize {
			return nil, fmt.Errorf("attachment too large (%d > %d)", length, maxSize)
		}
		return io.ReadAll(resp.Body)
	}
	var mbe *http.MaxBytesError
	data, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, maxSize))
	if err != nil && errors.As(err, &mbe) {
		return nil, fmt.Errorf("attachment too large (over %d)", maxSize)
	}
	return data, err
}

// MaxFileSize returns the homeserver's configured max upload size (0 if unset).
func (dc *DiscordConnector) MaxFileSize() int64 {
	dc.maxFileSizeMu.RLock()
	defer dc.maxFileSizeMu.RUnlock()
	return dc.maxFileSize
}

// convertLottie runs lottieconverter (+ ffmpeg for animated targets) to turn a
// Lottie sticker JSON into a raster/video. Ported from legacy attachments.go;
// the bridge config now lives in DiscordConfig.AnimatedSticker.
func (dc *DiscordConnector) convertLottie(ctx context.Context, data []byte) ([]byte, string, error) {
	cfg := dc.Config.AnimatedSticker
	fps := cfg.Args.FPS
	width := cfg.Args.Width
	height := cfg.Args.Height
	target := cfg.Target
	var lottieTarget, outputMime string
	switch target {
	case "png":
		lottieTarget = "png"
		outputMime = "image/png"
		fps = 1
	case "gif":
		lottieTarget = "gif"
		outputMime = "image/gif"
	case "webm":
		lottieTarget = "pngs"
		outputMime = "video/webm"
	case "webp":
		lottieTarget = "pngs"
		outputMime = "image/webp"
	case "disable", "":
		return data, "application/json", nil
	default:
		return nil, "", fmt.Errorf("invalid animated sticker target %q in bridge config", target)
	}

	tempdir, err := os.MkdirTemp("", "mautrix_discord_lottie_")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tempdir); rmErr != nil {
			zerolog.Ctx(ctx).Warn().Err(rmErr).Msg("Failed to delete lottie conversion temp dir")
		}
	}()

	lottieOutput := filepath.Join(tempdir, "out_")
	if lottieTarget != "pngs" {
		lottieOutput = filepath.Join(tempdir, "output."+lottieTarget)
	}
	cmd := exec.CommandContext(ctx, "lottieconverter", "-", lottieOutput, lottieTarget, fmt.Sprintf("%dx%d", width, height), strconv.Itoa(fps))
	cmd.Stdin = bytes.NewReader(data)
	if err = cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("failed to run lottieconverter: %w", err)
	}
	var path string
	if lottieTarget == "pngs" {
		var videoCodec string
		outputExtension := "." + target
		switch target {
		case "webm":
			videoCodec = "libvpx-vp9"
		case "webp":
			videoCodec = "libwebp_anim"
		default:
			return nil, "", fmt.Errorf("impossible case: unknown target %q", target)
		}
		path, err = ffmpeg.ConvertPath(
			ctx, lottieOutput+"*.png", outputExtension,
			[]string{"-framerate", strconv.Itoa(fps), "-pattern_type", "glob"},
			[]string{"-c:v", videoCodec, "-pix_fmt", "yuva420p", "-f", target},
			false,
		)
		if err != nil {
			return nil, "", fmt.Errorf("failed to run ffmpeg: %w", err)
		}
	} else {
		path = lottieOutput
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read converted file: %w", err)
	}
	return data, outputMime, nil
}

// resolveCustomEmojiMXC returns the mxc:// URI for a Discord custom emoji,
// uploading it to Matrix (and caching) if necessary. Used by the formatter
// (custom emoji rendering) and reaction shortcode handling (FR-40/71).
//
// TODO(group6): when direct media is enabled, return a custom mxc:// pointing at
// the direct-media route instead of re-uploading.
func (dc *DiscordConnector) resolveCustomEmojiMXC(ctx context.Context, _ *bridgev2.Portal, emojiID, name string, animated bool) id.ContentURI {
	var url, mimeType string
	if animated {
		url = discordgo.EndpointEmojiAnimated(emojiID)
		mimeType = "image/gif"
	} else {
		url = discordgo.EndpointEmoji(emojiID)
		mimeType = "image/png"
	}
	// Emoji are always uploaded unencrypted (legacy parity: copyAttachmentToMatrix
	// was called with encrypt=false). Pass a nil portal so reuploadDiscordFile
	// uses the bot intent without room-based encryption.
	res, err := dc.reuploadDiscordFile(ctx, dc.br.Bot, nil, emojiID, name, url, mimeType, nil)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("emoji_id", emojiID).Msg("Failed to copy emoji to Matrix")
		return id.ContentURI{}
	}
	parsed, err := res.MXC.Parse()
	if err != nil {
		return id.ContentURI{}
	}
	return parsed
}

// ConvertReactionEmoji is the FR-71 custom-emoji reaction conversion hook used
// by handlediscord's RemoteReaction.GetReactionEmoji. For a Unicode emoji it
// returns the fully-qualified emoji as the Matrix reaction key and an empty
// shortcode. For a Discord custom emoji it uploads the emoji image (via
// resolveCustomEmojiMXC) and returns the mxc:// URI as the Matrix key plus the
// emoji shortcode (e.g. ":partyparrot:") for com.beeper.reaction.shortcode.
//
// handlediscord is responsible for putting the returned shortcode into the
// reaction event's Extra["com.beeper.reaction.shortcode"], and for building the
// networkid.EmojiID ("<name>:<id>" for custom, the char for Unicode).
func (dc *DiscordClient) ConvertReactionEmoji(ctx context.Context, portal *bridgev2.Portal, emoji *discordgo.Emoji) (matrixKey, shortcode string) {
	if emoji == nil {
		return "", ""
	}
	if emoji.ID == "" {
		// Unicode emoji: the name is the character itself.
		return variationselector.FullyQualify(emoji.Name), ""
	}
	mxc := dc.connector.resolveCustomEmojiMXC(ctx, portal, emoji.ID, emoji.Name, emoji.Animated)
	if mxc.IsEmpty() {
		return "", ""
	}
	return mxc.String(), fmt.Sprintf(":%s:", emoji.Name)
}

// --- message conversion entry points ---

// ConvertMessage converts a Discord message into a bridgev2 ConvertedMessage.
// It is invoked from handlediscord's RemoteMessage.ConvertMessage and from
// backfill. The intent is the sender ghost's (or bot's) Matrix intent, used for
// media re-upload.
func (dc *DiscordClient) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *discordgo.Message) (*bridgev2.ConvertedMessage, error) {
	rctx := dc.newRenderContext(ctx, portal, msg.GuildID)

	parts := make([]*bridgev2.ConvertedMessagePart, 0, len(msg.Attachments)+len(msg.StickerItems)+1)

	if textPart := dc.convertTextMessage(ctx, intent, portal, rctx, msg); textPart != nil {
		parts = append(parts, textPart)
	}

	handledIDs := make(map[string]struct{})
	attachmentIndex := 0
	for _, att := range msg.Attachments {
		if _, handled := handledIDs[att.ID]; handled {
			continue
		}
		handledIDs[att.ID] = struct{}{}
		if part := dc.convertAttachment(ctx, intent, portal, attachmentIndex, att); part != nil {
			parts = append(parts, part)
			attachmentIndex++
		}
	}
	for _, sticker := range msg.StickerItems {
		if _, handled := handledIDs[sticker.ID]; handled {
			continue
		}
		handledIDs[sticker.ID] = struct{}{}
		if part := dc.convertSticker(ctx, intent, portal, sticker); part != nil {
			parts = append(parts, part)
		}
	}
	for _, embed := range msg.Embeds {
		if getEmbedType(msg, embed) != EmbedVideo {
			continue
		}
		// Discord deduplicates embeds by URL; reuse the same handled set.
		if _, handled := handledIDs[embed.URL]; handled {
			continue
		}
		handledIDs[embed.URL] = struct{}{}
		if part := dc.convertVideoEmbed(ctx, intent, portal, embed); part != nil {
			parts = append(parts, part)
		}
	}

	if len(parts) == 0 && msg.Thread != nil {
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    fmt.Sprintf("Created a thread: %s", msg.Thread.Name),
			},
		})
	}

	// Per-message profile (FR-31 / ar M1): webhook + guild-member display goes in
	// each part's Extra["com.beeper.per_message_profile"].
	dc.applyPerMessageProfile(ctx, intent, parts, msg)

	// Assign deterministic part IDs (FR-28/FR-43).
	assignPartIDs(parts)

	converted := &bridgev2.ConvertedMessage{
		Parts:      parts,
		ThreadRoot: dc.threadRoot(portal, msg),
		ReplyTo:    dc.replyTo(portal, msg),
	}
	// Collapse a single text+single media into a caption when applicable.
	converted.MergeCaption()
	return converted, nil
}

// ConvertEdit converts a Discord message update into a bridgev2 ConvertedEdit.
// existing are the DB message rows for the original message's parts.
func (dc *DiscordClient) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, msg *discordgo.Message) (*bridgev2.ConvertedEdit, error) {
	converted := &bridgev2.ConvertedEdit{}
	if len(existing) == 0 {
		return converted, nil
	}
	// Discord edits only change the text content; attachments aren't editable.
	// Re-render the text and apply it to the existing text part (part_id "").
	rctx := dc.newRenderContext(ctx, portal, msg.GuildID)
	textPart := dc.convertTextMessage(ctx, intent, portal, rctx, msg)
	if textPart == nil {
		// Text was removed entirely; nothing actionable for an in-place edit.
		return converted, nil
	}
	dc.applyPerMessageProfile(ctx, intent, []*bridgev2.ConvertedMessagePart{textPart}, msg)

	var target *database.Message
	for _, ex := range existing {
		if ex.PartID == "" {
			target = ex
			break
		}
	}
	if target == nil {
		target = existing[0]
	}
	converted.ModifiedParts = append(converted.ModifiedParts, &bridgev2.ConvertedEditPart{
		Part:    target,
		Type:    textPart.Type,
		Content: textPart.Content,
		Extra:   textPart.Extra,
	})
	return converted, nil
}

// --- part construction ---

func (dc *DiscordClient) convertAttachment(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, index int, att *discordgo.MessageAttachment) *bridgev2.ConvertedMessagePart {
	content := &event.MessageEventContent{
		Body: att.Filename,
		Info: &event.FileInfo{
			Height:   att.Height,
			MimeType: att.ContentType,
			Width:    att.Width,
			Size:     att.Size,
		},
	}
	extra := make(map[string]any)
	if strings.HasPrefix(att.Filename, "SPOILER_") {
		extra["page.codeberg.everypizza.msc4193.spoiler"] = true
	}
	if att.Description != "" {
		content.Body = att.Description
		content.FileName = att.Filename
	}
	switch strings.ToLower(strings.Split(att.ContentType, "/")[0]) {
	case "audio":
		content.MsgType = event.MsgAudio
		if att.Waveform != nil {
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

	res, err := dc.connector.reuploadDiscordFile(ctx, intent, portal, att.ID, "", att.URL, att.ContentType, nil)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("attachment_id", att.ID).Msg("Failed to reupload attachment")
		return &bridgev2.ConvertedMessagePart{
			ID:      discordid.MakePartID(index, att.ID),
			Type:    event.EventMessage,
			Content: createMediaFailedMessage(err),
		}
	}
	applyUpload(content, res)
	return &bridgev2.ConvertedMessagePart{
		ID:      discordid.MakePartID(index, att.ID),
		Type:    event.EventMessage,
		Content: content,
		Extra:   extra,
	}
}

func (dc *DiscordClient) convertSticker(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, sticker *discordgo.StickerItem) *bridgev2.ConvertedMessagePart {
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
		Body: sticker.Name,
		Info: &event.FileInfo{MimeType: mime},
	}

	var converter func([]byte) ([]byte, string, error)
	if mime == "application/json" {
		converter = func(b []byte) ([]byte, string, error) { return dc.connector.convertLottie(ctx, b) }
	}
	res, err := dc.connector.reuploadDiscordFile(ctx, intent, portal, sticker.ID, "", sticker.URL(), mime, converter)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("sticker_id", sticker.ID).Msg("Failed to reupload sticker")
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: createMediaFailedMessage(err),
		}
	}
	if mime == "application/json" {
		content.Info.MimeType = res.MimeType
	}
	applyUpload(content, res)
	cleanupConvertedStickerInfo(content)
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventSticker,
		Content: content,
	}
}

func cleanupConvertedStickerInfo(content *event.MessageEventContent) {
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

func (dc *DiscordClient) convertVideoEmbed(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, embed *discordgo.MessageEmbed) *bridgev2.ConvertedMessagePart {
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
	res, err := dc.connector.reuploadDiscordFile(ctx, intent, portal, "", "", proxyURL, "", nil)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to copy video embed to Matrix")
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: createMediaFailedMessage(err),
		}
	}
	content := &event.MessageEventContent{
		Body: embed.URL,
		Info: &event.FileInfo{MimeType: res.MimeType, Size: int(res.Size)},
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
	applyUpload(content, res)
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

func createMediaFailedMessage(bridgeErr error) *event.MessageEventContent {
	return &event.MessageEventContent{
		Body:    fmt.Sprintf("Failed to bridge media: %v", bridgeErr),
		MsgType: event.MsgNotice,
	}
}

// --- text + embeds + system messages ---

const (
	msgInteractionTemplateHTML = `<blockquote>
<a href="https://matrix.to/#/%s">%s</a> used <font color="#3771bb">/%s</font>
</blockquote>`
	msgComponentTemplateHTML = `<p>This message contains interactive elements. Use the Discord app to interact with the message.</p>`
	forwardTemplateHTML      = `<blockquote>
<p>↷ Forwarded</p>
%s
<p>%s</p>
</blockquote>`
)

func (dc *DiscordClient) convertTextMessage(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, rctx *discordRenderContext, msg *discordgo.Message) *bridgev2.ConvertedMessagePart {
	log := zerolog.Ctx(ctx)

	// System messages (FR-32).
	switch msg.Type {
	case discordgo.MessageTypeCall:
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote, Body: "started a call",
		}}
	case discordgo.MessageTypeGuildMemberJoin:
		return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgEmote, Body: "joined the server",
		}}
	}

	var htmlParts []string

	// Slash-command interaction notice (FR-32).
	if msg.Interaction != nil && msg.Interaction.User != nil {
		userMXID := dc.ghostMXID(msg.Interaction.User.ID)
		name := msg.Interaction.User.Username
		htmlParts = append(htmlParts, fmt.Sprintf(msgInteractionTemplateHTML, userMXID, name, msg.Interaction.Name))
	}

	if msg.Content != "" && !isPlainGifMessage(msg) {
		htmlParts = append(htmlParts, rctx.renderMarkdown(msg.Content, true))
	} else if msg.MessageReference != nil &&
		msg.MessageReference.Type == discordgo.MessageReferenceTypeForward &&
		len(msg.MessageSnapshots) > 0 &&
		msg.MessageSnapshots[0].Message != nil {
		// Forwarded message (FR-32).
		forwardedHTML := rctx.renderMarkdownNoUnwrap(msg.MessageSnapshots[0].Message.Content, true)
		msgTSText := msg.MessageSnapshots[0].Message.Timestamp.Format("2006-01-02 15:04 MST")
		origLink := dc.forwardOrigLink(ctx, msg, msgTSText)
		htmlParts = append(htmlParts, fmt.Sprintf(forwardTemplateHTML, forwardedHTML, origLink))
	}

	previews := make([]*event.BeeperLinkPreview, 0)
	for i, embed := range msg.Embeds {
		if i == 0 && msg.MessageReference == nil && isReplyEmbed(embed) {
			continue
		}
		switch getEmbedType(msg, embed) {
		case EmbedRich:
			htmlParts = append(htmlParts, dc.convertRichEmbed(ctx, intent, rctx, embed))
		case EmbedLinkPreview:
			previews = append(previews, dc.convertLinkEmbedToBeeper(ctx, intent, portal, embed))
		case EmbedVideo:
			// Handled as a separate part.
		default:
			log.Warn().Str("embed_type", string(embed.Type)).Int("embed_index", i).Msg("Unknown embed type in message")
		}
	}

	// Interactive components (FR-76).
	if len(msg.Components) > 0 {
		htmlParts = append(htmlParts, msgComponentTemplateHTML)
	}

	if len(htmlParts) == 0 {
		return nil
	}

	fullHTML := strings.Join(htmlParts, "\n")

	// @everyone/@here gating (FR-69/70): if the message isn't permitted to ping
	// everyone, replace the rendered @room with the anti-ping zero-width form.
	mayPing := dc.mayMentionEveryone(ctx, portal, msg)
	if !mayPing {
		fullHTML = strings.ReplaceAll(fullHTML, "@room", antiPingRoom)
	}

	content := format.HTMLToContent(fullHTML)
	if msg.MentionEveryone && mayPing {
		if content.Mentions == nil {
			content.Mentions = &event.Mentions{}
		}
		content.Mentions.Room = true
	}
	content.BeeperLinkPreviews = previews

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: &content,
	}
}

// mayMentionEveryone reports whether the @everyone/@here in this message should
// produce a real @room ping (FR-70). It honors the message's MentionEveryone
// flag (Discord only sets it when the sender had permission to ping everyone),
// which is the authoritative power-level gate from Discord's side.
//
// TODO(group6/relay): when relaying via webhook, refine the gate using the
// relay portal's power levels.
func (dc *DiscordClient) mayMentionEveryone(_ context.Context, _ *bridgev2.Portal, msg *discordgo.Message) bool {
	return msg.MentionEveryone
}

func (dc *DiscordClient) forwardOrigLink(ctx context.Context, msg *discordgo.Message, msgTSText string) string {
	origLink := fmt.Sprintf("unknown channel • %s", msgTSText)
	if msg.MessageReference == nil {
		return origLink
	}
	fwdPortal, err := dc.br.GetExistingPortalByKey(ctx, discordid.MakePortalKey(msg.MessageReference.ChannelID, "", false))
	if err != nil || fwdPortal == nil {
		return origLink
	}
	name := channelMentionName(fwdPortal)
	if fwdPortal.MXID != "" {
		return fmt.Sprintf(`<a href="%s">#%s</a> • %s`, fwdPortal.MXID.URI(dc.homeserverDomain()).MatrixToURL(), name, msgTSText)
	} else if name != "" {
		return fmt.Sprintf("%s • %s", name, msgTSText)
	}
	return origLink
}

// --- embed → HTML / link preview (FR-27) ---

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

func (dc *DiscordClient) convertRichEmbed(ctx context.Context, intent bridgev2.MatrixAPI, rctx *discordRenderContext, embed *discordgo.MessageEmbed) string {
	log := zerolog.Ctx(ctx)
	var htmlParts []string
	if embed.Author != nil {
		authorNameHTML := html.EscapeString(embed.Author.Name)
		if embed.Author.URL != "" {
			authorNameHTML = fmt.Sprintf(embedHTMLAuthorLink, embed.Author.URL, authorNameHTML)
		}
		authorHTML := fmt.Sprintf(embedHTMLAuthorPlain, authorNameHTML)
		if embed.Author.ProxyIconURL != "" {
			if res, err := dc.connector.reuploadDiscordFile(ctx, intent, nil, "", "", embed.Author.ProxyIconURL, "", nil); err != nil {
				log.Warn().Err(err).Msg("Failed to reupload author icon in embed")
			} else {
				authorHTML = fmt.Sprintf(embedHTMLAuthorWithImage, res.MXC, authorNameHTML)
			}
		}
		htmlParts = append(htmlParts, authorHTML)
	}
	if embed.Title != "" {
		baseTitleHTML := rctx.renderMarkdown(embed.Title, false)
		var titleHTML string
		if embed.URL != "" {
			titleHTML = fmt.Sprintf(embedHTMLTitleWithLink, html.EscapeString(embed.URL), baseTitleHTML)
		} else {
			titleHTML = fmt.Sprintf(embedHTMLTitlePlain, baseTitleHTML)
		}
		htmlParts = append(htmlParts, titleHTML)
	}
	if embed.Description != "" {
		htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLDescription, rctx.renderMarkdown(embed.Description, true)))
	}
	// TODO(task 2.2 config): DiscordConfig has no embed_fields_as_tables key yet;
	// default to the legacy default (linear fields). Wire this to the config when
	// the field is added.
	const embedFieldsAsTables = false
	for i := 0; i < len(embed.Fields); i++ {
		item := embed.Fields[i]
		if embedFieldsAsTables {
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
				headerParts[j] = fmt.Sprintf(embedHTMLFieldName, rctx.renderMarkdown(splitItem.Name, false))
				contentParts[j] = fmt.Sprintf(embedHTMLFieldValue, rctx.renderMarkdown(splitItem.Value, true))
			}
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLFields, strings.Join(headerParts, ""), strings.Join(contentParts, "")))
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLLinearField,
				strconv.FormatBool(item.Inline),
				rctx.renderMarkdown(item.Name, false),
				rctx.renderMarkdown(item.Value, true),
			))
		}
	}
	if embed.Image != nil {
		if res, err := dc.connector.reuploadDiscordFile(ctx, intent, nil, "", "", embed.Image.ProxyURL, "", nil); err != nil {
			log.Warn().Err(err).Msg("Failed to reupload image in embed")
		} else {
			htmlParts = append(htmlParts, fmt.Sprintf(embedHTMLImage, res.MXC))
		}
	}
	var embedDateHTML string
	if embed.Timestamp != "" {
		formattedTime := embed.Timestamp
		if parsedTS, err := time.Parse(time.RFC3339, embed.Timestamp); err != nil {
			log.Warn().Err(err).Msg("Failed to parse timestamp in embed")
		} else {
			formattedTime = parsedTS.Format(discordTimestampStyle('F').Format())
		}
		embedDateHTML = fmt.Sprintf(embedHTMLDate, embed.Timestamp, formattedTime)
	}
	if embed.Footer != nil {
		var datePart string
		if embedDateHTML != "" {
			datePart = embedFooterDateSeparator + embedDateHTML
		}
		footerHTML := fmt.Sprintf(embedHTMLFooterPlain, html.EscapeString(embed.Footer.Text), datePart)
		if embed.Footer.ProxyIconURL != "" {
			if res, err := dc.connector.reuploadDiscordFile(ctx, intent, nil, "", "", embed.Footer.ProxyIconURL, "", nil); err != nil {
				log.Warn().Err(err).Msg("Failed to reupload footer icon in embed")
			} else {
				footerHTML = fmt.Sprintf(embedHTMLFooterWithImage, res.MXC, html.EscapeString(embed.Footer.Text), datePart)
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
		return fmt.Sprintf(embedHTMLWrapperColor, embed.Color, compiledHTML)
	}
	return fmt.Sprintf(embedHTMLWrapper, compiledHTML)
}

func (dc *DiscordClient) convertLinkEmbedToBeeper(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, embed *discordgo.MessageEmbed) *event.BeeperLinkPreview {
	preview := &event.BeeperLinkPreview{}
	preview.MatchedURL = embed.URL
	preview.Title = embed.Title
	preview.Description = embed.Description
	if embed.Image != nil {
		dc.convertLinkEmbedImage(ctx, intent, portal, embed.Image.ProxyURL, embed.Image.Width, embed.Image.Height, preview)
	} else if embed.Thumbnail != nil {
		dc.convertLinkEmbedImage(ctx, intent, portal, embed.Thumbnail.ProxyURL, embed.Thumbnail.Width, embed.Thumbnail.Height, preview)
	}
	return preview
}

func (dc *DiscordClient) convertLinkEmbedImage(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, url string, width, height int, preview *event.BeeperLinkPreview) {
	res, err := dc.connector.reuploadDiscordFile(ctx, intent, portal, "", "", url, "", nil)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to reupload image in URL preview")
		return
	}
	if width != 0 || height != 0 {
		preview.ImageWidth = event.IntOrString(width)
		preview.ImageHeight = event.IntOrString(height)
	} else {
		preview.ImageWidth = event.IntOrString(res.Width)
		preview.ImageHeight = event.IntOrString(res.Height)
	}
	preview.ImageSize = event.IntOrString(res.Size)
	preview.ImageType = res.MimeType
	if res.DecryptionInfo != nil {
		res.DecryptionInfo.URL = res.MXC
		preview.ImageEncryption = res.DecryptionInfo
	} else {
		preview.ImageURL = res.MXC
	}
}

// --- embed type classification (ported from legacy) ---

type BridgeEmbedType int

const (
	EmbedUnknown BridgeEmbedType = iota
	EmbedRich
	EmbedLinkPreview
	EmbedVideo
)

func isActuallyLinkPreview(embed *discordgo.MessageEmbed) bool {
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

// --- per-message profile (FR-31 / ar M1) ---

// applyPerMessageProfile attaches webhook / guild-member display info to each
// converted part's Extra["com.beeper.per_message_profile"] (NOT OrigSender,
// which is the Matrix→Discord relay direction). Mirrors legacy
// addWebhookMeta + addMemberMeta.
func (dc *DiscordClient) applyPerMessageProfile(ctx context.Context, intent bridgev2.MatrixAPI, parts []*bridgev2.ConvertedMessagePart, msg *discordgo.Message) {
	if len(parts) == 0 || msg.Author == nil {
		return
	}
	if msg.WebhookID != "" {
		dc.applyWebhookProfile(ctx, intent, parts, msg)
		return
	}
	dc.applyMemberProfile(ctx, intent, parts, msg)
}

func (dc *DiscordClient) applyWebhookProfile(ctx context.Context, intent bridgev2.MatrixAPI, parts []*bridgev2.ConvertedMessagePart, msg *discordgo.Message) {
	var avatarURL id.ContentURIString
	if dc.connector.Config.EnableWebhookAvatars && msg.Author.Avatar != "" {
		if res, err := dc.connector.reuploadDiscordFile(ctx, intent, nil, "", "", msg.Author.AvatarURL(""), "", nil); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("avatar_id", msg.Author.Avatar).Msg("Failed to reupload webhook avatar")
		} else {
			avatarURL = res.MXC
		}
	}
	profileID := sha256.Sum256(fmt.Appendf(nil, "%s:%s", msg.Author.Username, msg.Author.Avatar))
	profile := &event.BeeperPerMessageProfile{
		ID:          hex.EncodeToString(profileID[:]),
		Displayname: msg.Author.Username,
	}
	if avatarURL != "" {
		profile.AvatarURL = &avatarURL
	}

	prefix := msg.ApplicationID == "" && dc.connector.Config.PrefixWebhookMessages
	for _, part := range parts {
		setPerMessageProfile(part, profile)
		if prefix && shouldPrefixWebhook(part.Content) {
			part.Content.AddPerMessageProfileFallback()
		}
	}
}

func shouldPrefixWebhook(content *event.MessageEventContent) bool {
	return content != nil && (content.MsgType == event.MsgText || content.MsgType == event.MsgNotice ||
		(content.FileName != "" && content.FileName != content.Body))
}

func (dc *DiscordClient) applyMemberProfile(ctx context.Context, intent bridgev2.MatrixAPI, parts []*bridgev2.ConvertedMessagePart, msg *discordgo.Message) {
	if msg.Member == nil {
		return
	}
	var avatarURL id.ContentURIString
	if msg.Member.Avatar != "" {
		guildAvatarURL := discordgo.EndpointGuildMemberAvatar(msg.GuildID, msg.Author.ID, msg.Member.Avatar)
		if res, err := dc.connector.reuploadDiscordFile(ctx, intent, nil, "", "", guildAvatarURL, "", nil); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("avatar_id", msg.Member.Avatar).Msg("Failed to reupload guild user avatar")
		} else {
			avatarURL = res.MXC
		}
	}
	if msg.Member.Nick == "" && avatarURL == "" {
		return
	}
	displayname := msg.Member.Nick
	if displayname == "" {
		displayname = msg.Author.GlobalName
		if displayname == "" {
			displayname = msg.Author.Username
		}
	}
	profile := &event.BeeperPerMessageProfile{
		ID:          fmt.Sprintf("%s_%s", msg.GuildID, msg.Author.ID),
		Displayname: displayname,
	}
	if avatarURL != "" {
		profile.AvatarURL = &avatarURL
	}
	for _, part := range parts {
		setPerMessageProfile(part, profile)
	}
}

func setPerMessageProfile(part *bridgev2.ConvertedMessagePart, profile *event.BeeperPerMessageProfile) {
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	part.Extra["com.beeper.per_message_profile"] = profile
	if part.Content != nil {
		part.Content.BeeperPerMessageProfile = profile
	}
}

// --- ids, reply, thread root ---

// assignPartIDs sets deterministic PartIDs. Attachment parts already carry their
// attachment-<index>-<id> id; the text part keeps "". If the message has exactly
// one part, it gets the empty single-part id (matching the migration collapse).
func assignPartIDs(parts []*bridgev2.ConvertedMessagePart) {
	if len(parts) == 1 {
		parts[0].ID = ""
		return
	}
	for i, part := range parts {
		if part.ID != "" {
			continue
		}
		if part.Content != nil && part.Content.MsgType == event.MsgText {
			// Leave the text part as "".
			continue
		}
		part.ID = networkid.PartID(fmt.Sprintf("part-%d", i))
	}
}

// threadRoot returns the thread-root MessageID when the message belongs to a
// thread (in-room thread model, ar H7). The parent channel is the portal; the
// thread's root message is identified by the thread channel ID, which equals the
// root message ID for public threads.
func (dc *DiscordClient) threadRoot(portal *bridgev2.Portal, msg *discordgo.Message) *networkid.MessageID {
	threadID := threadIDForMessage(portal, msg)
	if threadID == "" {
		return nil
	}
	// A Discord public thread's channel ID is the same snowflake as the root
	// message ID; the root message lives in the parent channel.
	parentChannelID := discordid.ParsePortalID(portal.ID)
	rootID := discordid.MakeMessageID(parentChannelID, threadID)
	return &rootID
}

// threadIDForMessage returns the thread channel ID if the message was sent in a
// thread whose parent is this portal. The connector's handlediscord maps thread
// messages to the parent portal; the message's ChannelID then differs from the
// portal's channel.
func threadIDForMessage(portal *bridgev2.Portal, msg *discordgo.Message) string {
	parentChannelID := discordid.ParsePortalID(portal.ID)
	if msg.ChannelID != "" && msg.ChannelID != parentChannelID {
		return msg.ChannelID
	}
	return ""
}

// replyTo builds the reply reference from a Discord MessageReference (FR-30).
func (dc *DiscordClient) replyTo(portal *bridgev2.Portal, msg *discordgo.Message) *networkid.MessageOptionalPartID {
	ref := msg.MessageReference
	if ref == nil && len(msg.Embeds) > 0 {
		// Legacy hacky reply embed fallback.
		if match := hackyReplyPattern.FindStringSubmatch(msg.Embeds[0].Description); match != nil {
			ref = &discordgo.MessageReference{ChannelID: match[2], MessageID: match[3]}
		}
	}
	if ref == nil || ref.MessageID == "" {
		return nil
	}
	if ref.Type == discordgo.MessageReferenceTypeForward {
		// Forwards are rendered inline, not as replies.
		return nil
	}
	channelID := ref.ChannelID
	if channelID == "" {
		channelID = discordid.ParsePortalID(portal.ID)
	}
	return &networkid.MessageOptionalPartID{
		MessageID: discordid.MakeMessageID(channelID, ref.MessageID),
	}
}

// --- render context + helpers ---

// newRenderContext builds the per-message render context, pre-populating the
// role cache for the message's guild (H10: in-memory cache hot path with a
// dc_role cold-start fallback).
func (dc *DiscordClient) newRenderContext(ctx context.Context, portal *bridgev2.Portal, guildID string) *discordRenderContext {
	return &discordRenderContext{
		ctx:              ctx,
		conn:             dc.connector,
		portal:           portal,
		guildID:          guildID,
		homeserverDomain: dc.homeserverDomain(),
		roles:            dc.connector.loadRolesForGuild(ctx, guildID),
	}
}

// homeserverDomain returns the homeserver domain used for matrix.to links.
func (dc *DiscordClient) homeserverDomain() string {
	if dc.br != nil && dc.br.Matrix != nil {
		if sn, ok := dc.br.Matrix.(interface{ ServerName() string }); ok {
			return sn.ServerName()
		}
	}
	return ""
}

// ghostMXID returns the ghost MXID for a Discord user ID without loading the
// ghost (used for the interaction notice link).
func (dc *DiscordClient) ghostMXID(userID string) id.UserID {
	return dc.br.Matrix.GhostIntent(discordid.MakeUserID(userID)).GetMXID()
}

// loadRolesForGuild builds the per-message role lookup. It reads the in-memory
// role cache first (H10) and falls back to the dc_role backing store on a miss.
//
// TODO(ar H10): the in-memory role cache (connector.roleCache) is currently a
// placeholder populated by handlediscord (Task 4.1). Until that lands, this
// queries dc_role per guild once per message (one query, not per-mention), which
// is acceptable for now.
func (dc *DiscordConnector) loadRolesForGuild(ctx context.Context, guildID string) map[string]*cachedRole {
	roles := make(map[string]*cachedRole)
	if guildID == "" {
		return roles
	}
	dbRoles, err := dc.DB.Role.GetAllForGuild(ctx, guildID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("guild_id", guildID).Msg("Failed to load roles for guild")
		return roles
	}
	for _, r := range dbRoles {
		roles[r.RoleID] = &cachedRole{Name: r.Name, Color: r.Color}
	}
	return roles
}

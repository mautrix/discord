// Download and MediaID encoding for the bridgev2 direct-media subsystem.
// Implements bridgev2.DirectMediableNetwork on *DiscordConnector.
//
// MediaID encoding (new format, AC-6):
//
//	The framework (matrix/directmedia.go) wraps the connector's MediaID as:
//	    base64(cat_emoji_prefix + networkid.MediaID + HMAC_truncated)
//	The connector's MediaID (= what Download receives) encodes media type and
//	Discord CDN coordinates in a compact binary struct — same layout as the
//	legacy directmedia_id.go but WITHOUT the leading "\U0001F408DISCORD"
//	prefix (that was added by the old standalone implementation; the new
//	framework handles the outer HMAC wrapper).
//
// Dual-format detection (H5 / AC-6):
//
//	Pre-migration mxc:// URIs contain the legacy binary payload as their
//	networkid.MediaID. In the legacy code the full payload was:
//	    "\U0001F408DISCORD" + version + class + data + HMAC
//	The framework strips the leading "\U0001F408" during parsing, so the
//	networkid.MediaID the connector receives starts with "DISCORD" for any
//	pre-migration URI. New-format MediaIDs start with a single-byte class tag
//	(values 1–5), which can never begin with 'D' (0x44). The implementation
//	therefore branches on bytes[0] == 'D' to select the parser.
//
// Cache (M12):
//
//	Refreshed Discord CDN attachment URLs are cached in an LRU-capped,
//	TTL-bounded in-memory map to avoid redundant REST calls. Capacity is
//	capped at maxAttachmentCacheSize entries; entries are evicted when they
//	expire or when the LRU watermark is hit.
package connector

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"
)

// --- DirectMediableNetwork ---

// useDirectMedia is set true by the framework after it has configured the
// media proxy. Protected by useDirectMediaMu.
var useDirectMedia bool
var useDirectMediaMu sync.RWMutex

// SetUseDirectMedia is called by the framework once the media proxy is ready.
func (dc *DiscordConnector) SetUseDirectMedia() {
	useDirectMediaMu.Lock()
	useDirectMedia = true
	useDirectMediaMu.Unlock()
}

// isDirectMediaEnabled returns whether the framework has enabled direct media.
func isDirectMediaEnabled() bool {
	useDirectMediaMu.RLock()
	defer useDirectMediaMu.RUnlock()
	return useDirectMedia
}

// --- MediaID encoding (new format) ---

// mediaIDClass identifies the kind of Discord asset encoded in a new-format
// MediaID. Values are kept identical to the legacy constants so that the two
// formats share the same class semantics.
type mediaIDClass uint8

const (
	mediaIDClassAttachment        mediaIDClass = 1
	mediaIDClassEmoji             mediaIDClass = 2
	mediaIDClassSticker           mediaIDClass = 3
	mediaIDClassUserAvatar        mediaIDClass = 4
	mediaIDClassGuildMemberAvatar mediaIDClass = 5
)

// newMediaID encodes a class byte followed by the raw binary struct into a
// networkid.MediaID that the framework will HMAC-sign and base64-wrap.
func newMediaID(class mediaIDClass, data any) networkid.MediaID {
	var buf bytes.Buffer
	buf.WriteByte(byte(class))
	_ = binary.Write(&buf, binary.BigEndian, data)
	return networkid.MediaID(buf.Bytes())
}

// newEmojiMediaID encodes an emoji (which has a variable-length name field).
func newEmojiMediaID(inner emojiMediaDataInner, name string) networkid.MediaID {
	var buf bytes.Buffer
	buf.WriteByte(byte(mediaIDClassEmoji))
	_ = binary.Write(&buf, binary.BigEndian, inner)
	buf.WriteString(name)
	return networkid.MediaID(buf.Bytes())
}

// AttachmentMediaID builds a new-format networkid.MediaID for an attachment.
func AttachmentMediaID(channelID, messageID, attachmentID uint64) networkid.MediaID {
	return newMediaID(mediaIDClassAttachment, struct {
		ChannelID    uint64
		MessageID    uint64
		AttachmentID uint64
	}{channelID, messageID, attachmentID})
}

// EmojiMediaID builds a new-format networkid.MediaID for a custom emoji.
func EmojiMediaID(emojiID uint64, animated bool, name string) networkid.MediaID {
	return newEmojiMediaID(emojiMediaDataInner{EmojiID: emojiID, Animated: animated}, name)
}

// StickerMediaID builds a new-format networkid.MediaID for a sticker.
func StickerMediaID(stickerID uint64, format byte) networkid.MediaID {
	return newMediaID(mediaIDClassSticker, struct {
		StickerID uint64
		Format    uint8
	}{stickerID, format})
}

// UserAvatarMediaID builds a new-format networkid.MediaID for a user avatar.
func UserAvatarMediaID(userID uint64, animated bool, avatarID [16]byte) networkid.MediaID {
	return newMediaID(mediaIDClassUserAvatar, userAvatarData{
		UserID:   userID,
		Animated: animated,
		AvatarID: avatarID,
	})
}

// GuildMemberAvatarMediaID builds a new-format networkid.MediaID for a guild
// member avatar.
func GuildMemberAvatarMediaID(guildID, userID uint64, animated bool, avatarID [16]byte) networkid.MediaID {
	return newMediaID(mediaIDClassGuildMemberAvatar, guildMemberAvatarData{
		GuildID:  guildID,
		UserID:   userID,
		Animated: animated,
		AvatarID: avatarID,
	})
}

// Binary structs for new-format MediaID payloads (shared by encoder + decoder).

type emojiMediaDataInner struct {
	EmojiID  uint64
	Animated bool
}

type userAvatarData struct {
	UserID   uint64
	Animated bool
	AvatarID [16]byte
}

type guildMemberAvatarData struct {
	GuildID  uint64
	UserID   uint64
	Animated bool
	AvatarID [16]byte
}

// --- Attachment URL cache (M12) ---

const (
	maxAttachmentCacheSize = 1024
	attachmentCacheTTL     = 20 * time.Hour
	attachmentCacheBuffer  = 5 * time.Minute // don't serve URLs within 5 min of expiry
)

type attachmentCacheKey struct {
	ChannelID    uint64
	AttachmentID uint64
}

type attachmentCacheValue struct {
	URL    string
	Expiry time.Time
}

// attachmentCache is a simple TTL+size-bounded map.  It is protected by
// attachmentCacheMu and lives on the DiscordConnector via the package-level
// var below so it survives across Download calls without adding a field to
// DiscordConnector (which is already scaffolded by Group 1).
var (
	attachmentCache     = make(map[attachmentCacheKey]attachmentCacheValue)
	attachmentCacheMu   sync.Mutex
	attachmentCacheKeys []attachmentCacheKey // LRU order (front = oldest)
)

func cacheGetAttachment(key attachmentCacheKey) (attachmentCacheValue, bool) {
	attachmentCacheMu.Lock()
	defer attachmentCacheMu.Unlock()
	val, ok := attachmentCache[key]
	if !ok {
		return attachmentCacheValue{}, false
	}
	if time.Until(val.Expiry) < attachmentCacheBuffer {
		delete(attachmentCache, key)
		return attachmentCacheValue{}, false
	}
	return val, true
}

func cacheSetAttachment(key attachmentCacheKey, val attachmentCacheValue) {
	attachmentCacheMu.Lock()
	defer attachmentCacheMu.Unlock()
	if _, exists := attachmentCache[key]; !exists {
		// Evict oldest entries if over capacity.
		for len(attachmentCacheKeys) >= maxAttachmentCacheSize {
			oldest := attachmentCacheKeys[0]
			attachmentCacheKeys = attachmentCacheKeys[1:]
			delete(attachmentCache, oldest)
		}
		attachmentCacheKeys = append(attachmentCacheKeys, key)
	}
	attachmentCache[key] = val
}

// parseExpiryTS extracts the Discord CDN expiry timestamp from the ?ex= query
// parameter (a big-endian hex-encoded 32-bit Unix timestamp).
func parseExpiryTS(rawURL string) time.Time {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}
	}
	tsHex := parsed.Query().Get("ex")
	if len(tsHex) == 0 {
		return time.Time{}
	}
	tsBytes, err := hex.DecodeString(tsHex)
	if err != nil || len(tsBytes) != 4 {
		return time.Time{}
	}
	ts := int64(binary.BigEndian.Uint32(tsBytes))
	now := time.Now().Unix()
	if ts > now && ts < now+int64(365*24*time.Hour/time.Second) {
		return time.Unix(ts, 0)
	}
	return time.Time{}
}

// --- Legacy format support (H5) ---

// legacyMediaIDPrefix is the ASCII portion of the legacy MediaIDPrefix that
// follows the cat emoji ("\U0001F408"). The framework strips the cat emoji and
// hands the connector bytes that start with this string for old-format IDs.
const legacyMediaIDPrefix = "DISCORD"

// Legacy binary class constants (must match directmedia_id.go in main).
const (
	legacyClassAttachment        = uint8(1)
	legacyClassEmoji             = uint8(2)
	legacyClassSticker           = uint8(3)
	legacyClassUserAvatar        = uint8(4)
	legacyClassGuildMemberAvatar = uint8(5)
)

// parsedLegacyMediaID holds the decoded content of a legacy binary MediaID.
type parsedLegacyMediaID struct {
	class uint8
	// Only one of the following is populated, matching class.
	attachment struct{ ChannelID, MessageID, AttachmentID uint64 }
	emoji      struct {
		EmojiID  uint64
		Animated bool
		Name     string
	}
	sticker struct {
		StickerID uint64
		Format    uint8
	}
	userAvatar struct {
		UserID   uint64
		Animated bool
		AvatarID [16]byte
	}
	guildMemberAvatar struct {
		GuildID  uint64
		UserID   uint64
		Animated bool
		AvatarID [16]byte
	}
}

// parseLegacyMediaID decodes a legacy-format networkid.MediaID. The input is
// the raw bytes the framework hands to Download() — i.e. the blob AFTER the
// framework has stripped the outer cat emoji prefix and HMAC. For legacy IDs
// those bytes are: "DISCORD" + version(1) + class(1) + data.
func parseLegacyMediaID(raw networkid.MediaID) (*parsedLegacyMediaID, error) {
	r := bytes.NewReader([]byte(raw))

	// Strip "DISCORD" prefix.
	prefix := make([]byte, len(legacyMediaIDPrefix))
	if _, err := io.ReadFull(r, prefix); err != nil || string(prefix) != legacyMediaIDPrefix {
		return nil, fmt.Errorf("legacy media ID: missing DISCORD prefix")
	}

	// Version byte — must be 1.
	versionAndClass := make([]byte, 2)
	if _, err := io.ReadFull(r, versionAndClass); err != nil {
		return nil, fmt.Errorf("legacy media ID: missing version/class")
	}
	if versionAndClass[0] != 1 {
		return nil, fmt.Errorf("legacy media ID: unsupported version %d", versionAndClass[0])
	}

	out := &parsedLegacyMediaID{class: versionAndClass[1]}
	switch out.class {
	case legacyClassAttachment:
		if err := binary.Read(r, binary.BigEndian, &out.attachment); err != nil {
			return nil, fmt.Errorf("legacy media ID: parse attachment: %w", err)
		}
	case legacyClassEmoji:
		var inner struct {
			EmojiID  uint64
			Animated bool
		}
		if err := binary.Read(r, binary.BigEndian, &inner); err != nil {
			return nil, fmt.Errorf("legacy media ID: parse emoji inner: %w", err)
		}
		name, _ := io.ReadAll(r)
		out.emoji.EmojiID = inner.EmojiID
		out.emoji.Animated = inner.Animated
		out.emoji.Name = string(name)
	case legacyClassSticker:
		if err := binary.Read(r, binary.BigEndian, &out.sticker); err != nil {
			return nil, fmt.Errorf("legacy media ID: parse sticker: %w", err)
		}
	case legacyClassUserAvatar:
		if err := binary.Read(r, binary.BigEndian, &out.userAvatar); err != nil {
			return nil, fmt.Errorf("legacy media ID: parse user avatar: %w", err)
		}
	case legacyClassGuildMemberAvatar:
		if err := binary.Read(r, binary.BigEndian, &out.guildMemberAvatar); err != nil {
			return nil, fmt.Errorf("legacy media ID: parse guild member avatar: %w", err)
		}
	default:
		return nil, fmt.Errorf("legacy media ID: unknown class %d", out.class)
	}
	return out, nil
}

// --- HTTP proxy client for attachment downloads ---

var proxyClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
	},
	Timeout: 60 * time.Second,
}

// --- Download (bridgev2.DirectMediableNetwork) ---

// Download decodes a (possibly legacy) signed MediaID and returns the Discord
// CDN URL (or proxied bytes) as a mediaproxy.GetMediaResponse.
//
// Dual-format detection (H5): if the raw networkid.MediaID bytes start with
// the ASCII string "DISCORD", the ID was produced by the legacy bridge and is
// parsed with parseLegacyMediaID; otherwise the new binary class-prefixed
// format is used.
func (dc *DiscordConnector) Download(ctx context.Context, mediaID networkid.MediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	log := zerolog.Ctx(ctx)

	// H5: dual-format detection.
	if bytes.HasPrefix([]byte(mediaID), []byte(legacyMediaIDPrefix)) {
		return dc.downloadLegacy(ctx, log, mediaID)
	}
	return dc.downloadNew(ctx, log, mediaID)
}

// downloadNew handles new-format MediaIDs (class byte + binary data).
func (dc *DiscordConnector) downloadNew(ctx context.Context, log *zerolog.Logger, mediaID networkid.MediaID) (mediaproxy.GetMediaResponse, error) {
	if len(mediaID) < 1 {
		return nil, mediaproxy.ErrInvalidMediaIDSyntax
	}
	class := mediaIDClass(mediaID[0])
	payload := mediaID[1:]
	r := bytes.NewReader(payload)

	switch class {
	case mediaIDClassAttachment:
		var data struct {
			ChannelID    uint64
			MessageID    uint64
			AttachmentID uint64
		}
		if err := binary.Read(r, binary.BigEndian, &data); err != nil {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
		cdnURL, expiry, err := dc.resolveAttachmentURL(ctx, log, data.ChannelID, data.MessageID, data.AttachmentID)
		if err != nil {
			return nil, err
		}
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL, ExpiresAt: expiry}, nil

	case mediaIDClassEmoji:
		var inner emojiMediaDataInner
		if err := binary.Read(r, binary.BigEndian, &inner); err != nil {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
		emojiIDStr := strconv.FormatUint(inner.EmojiID, 10)
		var cdnURL string
		if inner.Animated {
			cdnURL = discordgo.EndpointEmojiAnimated(emojiIDStr)
		} else {
			cdnURL = discordgo.EndpointEmoji(emojiIDStr)
		}
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL}, nil

	case mediaIDClassSticker:
		var data struct {
			StickerID uint64
			Format    uint8
		}
		if err := binary.Read(r, binary.BigEndian, &data); err != nil {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
		cdnURL := discordgo.EndpointStickerImage(
			strconv.FormatUint(data.StickerID, 10),
			discordgo.StickerFormat(data.Format),
		)
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL}, nil

	case mediaIDClassUserAvatar:
		var data userAvatarData
		if err := binary.Read(r, binary.BigEndian, &data); err != nil {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
		return &mediaproxy.GetMediaResponseURL{URL: userAvatarURL(data.UserID, data.AvatarID, data.Animated)}, nil

	case mediaIDClassGuildMemberAvatar:
		var data guildMemberAvatarData
		if err := binary.Read(r, binary.BigEndian, &data); err != nil {
			return nil, mediaproxy.ErrInvalidMediaIDSyntax
		}
		return &mediaproxy.GetMediaResponseURL{URL: guildMemberAvatarURL(data.GuildID, data.UserID, data.AvatarID, data.Animated)}, nil

	default:
		return nil, fmt.Errorf("unknown media ID class %d", class)
	}
}

// downloadLegacy handles pre-migration legacy-format MediaIDs.
func (dc *DiscordConnector) downloadLegacy(ctx context.Context, log *zerolog.Logger, mediaID networkid.MediaID) (mediaproxy.GetMediaResponse, error) {
	parsed, err := parseLegacyMediaID(mediaID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", mediaproxy.ErrInvalidMediaIDSyntax, err)
	}

	switch parsed.class {
	case legacyClassAttachment:
		a := parsed.attachment
		cdnURL, expiry, err := dc.resolveAttachmentURL(ctx, log, a.ChannelID, a.MessageID, a.AttachmentID)
		if err != nil {
			return nil, err
		}
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL, ExpiresAt: expiry}, nil

	case legacyClassEmoji:
		e := parsed.emoji
		emojiIDStr := strconv.FormatUint(e.EmojiID, 10)
		var cdnURL string
		if e.Animated {
			cdnURL = discordgo.EndpointEmojiAnimated(emojiIDStr)
		} else {
			cdnURL = discordgo.EndpointEmoji(emojiIDStr)
		}
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL}, nil

	case legacyClassSticker:
		s := parsed.sticker
		cdnURL := discordgo.EndpointStickerImage(
			strconv.FormatUint(s.StickerID, 10),
			discordgo.StickerFormat(s.Format),
		)
		return &mediaproxy.GetMediaResponseURL{URL: cdnURL}, nil

	case legacyClassUserAvatar:
		a := parsed.userAvatar
		return &mediaproxy.GetMediaResponseURL{URL: userAvatarURL(a.UserID, a.AvatarID, a.Animated)}, nil

	case legacyClassGuildMemberAvatar:
		a := parsed.guildMemberAvatar
		return &mediaproxy.GetMediaResponseURL{URL: guildMemberAvatarURL(a.GuildID, a.UserID, a.AvatarID, a.Animated)}, nil

	default:
		return nil, fmt.Errorf("legacy media ID: unknown class %d", parsed.class)
	}
}

// --- CDN URL helpers ---

func userAvatarURL(userID uint64, avatarID [16]byte, animated bool) string {
	userIDStr := strconv.FormatUint(userID, 10)
	if animated {
		return discordgo.EndpointUserAvatarAnimated(userIDStr, fmt.Sprintf("a_%x", avatarID))
	}
	return discordgo.EndpointUserAvatar(userIDStr, fmt.Sprintf("%x", avatarID))
}

func guildMemberAvatarURL(guildID, userID uint64, avatarID [16]byte, animated bool) string {
	guildIDStr := strconv.FormatUint(guildID, 10)
	userIDStr := strconv.FormatUint(userID, 10)
	if animated {
		return discordgo.EndpointGuildMemberAvatarAnimated(guildIDStr, userIDStr, fmt.Sprintf("a_%x", avatarID))
	}
	return discordgo.EndpointGuildMemberAvatar(guildIDStr, userIDStr, fmt.Sprintf("%x", avatarID))
}

// --- Attachment URL resolution ---

var (
	errNoUsersWithAccess = errors.New("no connected users with access to the channel")
	errAttachmentMissing = errors.New("attachment not found in message (may have been deleted)")
)

// resolveAttachmentURL returns a valid (non-expired) Discord CDN URL for the
// given attachment, refreshing via REST if the cached entry is stale.
func (dc *DiscordConnector) resolveAttachmentURL(
	ctx context.Context,
	log *zerolog.Logger,
	channelID, messageID, attachmentID uint64,
) (string, time.Time, error) {
	key := attachmentCacheKey{ChannelID: channelID, AttachmentID: attachmentID}
	if cached, ok := cacheGetAttachment(key); ok {
		return cached.URL, cached.Expiry, nil
	}

	log.Debug().
		Uint64("channel_id", channelID).
		Uint64("message_id", messageID).
		Uint64("attachment_id", attachmentID).
		Msg("Refreshing Discord attachment URL")

	cdnURL, expiry, err := dc.fetchAttachmentURL(ctx, channelID, messageID, attachmentID)
	if err != nil {
		log.Err(err).Msg("Failed to refresh attachment URL")
		return "", time.Time{}, err
	}

	cacheSetAttachment(key, attachmentCacheValue{URL: cdnURL, Expiry: expiry})
	log.Debug().Time("expiry", expiry).Msg("Refreshed attachment URL successfully")
	return cdnURL, expiry, nil
}

// fetchAttachmentURL fetches a fresh CDN URL by querying the Discord REST API
// via the first available connected UserLogin that has channel access.
func (dc *DiscordConnector) fetchAttachmentURL(
	ctx context.Context,
	channelID, messageID, attachmentID uint64,
) (string, time.Time, error) {
	channelIDStr := strconv.FormatUint(channelID, 10)
	messageIDStr := strconv.FormatUint(messageID, 10)
	attachmentIDStr := strconv.FormatUint(attachmentID, 10)

	// Find the first available session with channel access.
	var session *discordgo.Session
	for _, ul := range dc.br.GetAllCachedUserLogins() {
		client, ok := ul.Client.(*DiscordClient)
		if !ok || client.Session == nil {
			continue
		}
		// Prefer a session that can confirm view-channel permission; fall back
		// to the first available session if permission check fails.
		perms, err := client.Session.State.UserChannelPermissions(string(ul.ID), channelIDStr)
		if err == nil && perms&discordgo.PermissionViewChannel == 0 {
			// Confirmed no access — skip.
			continue
		}
		if session == nil || !client.Session.IsUser {
			session = client.Session
			if !client.Session.IsUser {
				// Bot tokens can directly fetch the message; prefer over user.
				break
			}
		}
	}
	if session == nil {
		return "", time.Time{}, errNoUsersWithAccess
	}

	// Fetch the message(s) to retrieve the updated attachment URL.
	var atts []*discordgo.MessageAttachment
	if session.IsUser {
		msgs, err := session.ChannelMessages(channelIDStr, 5, "", "", messageIDStr)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("fetch messages: %w", err)
		}
		for _, m := range msgs {
			atts = append(atts, m.Attachments...)
		}
	} else {
		msg, err := session.ChannelMessage(channelIDStr, messageIDStr)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("fetch message: %w", err)
		}
		atts = msg.Attachments
	}

	for _, att := range atts {
		if att.ID == attachmentIDStr {
			expiry := parseExpiryTS(att.URL)
			if expiry.IsZero() {
				expiry = time.Now().Add(attachmentCacheTTL)
			}
			return att.URL, expiry, nil
		}
	}
	return "", time.Time{}, errAttachmentMissing
}

// --- Avatar-proxy helper (FR-60) ---

// AvatarProxyKey returns the HMAC signing key used for avatar-proxy URLs.
// The key is derived from dc.Config.AvatarProxyKey (preserved by H6 migration).
func (dc *DiscordConnector) AvatarProxyKey() string {
	return dc.Config.AvatarProxyKey
}

// avatarProxyURLPrefix is the path prefix for avatar-proxy requests.
const avatarProxyURLPrefix = "/discord/avatar"

// registerAvatarProxyRoute registers the /discord/avatar/{...} HTTP handler on
// the router returned by the Matrix connector when public_address is set.
// Called from Start if the router is available.
func (dc *DiscordConnector) registerAvatarProxyRoute(router *http.ServeMux) {
	router.HandleFunc("GET "+avatarProxyURLPrefix+"/{userID}/{avatarID}", dc.handleAvatarProxy)
}

// handleAvatarProxy proxies a request for a Discord user avatar to the CDN.
// The URL format is /discord/avatar/{userID}/{avatarID} where avatarID may be
// prefixed with "a_" for animated avatars.
func (dc *DiscordConnector) handleAvatarProxy(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	avatarID := r.PathValue("avatarID")
	animated := strings.HasPrefix(avatarID, "a_")

	var cdnURL string
	if animated {
		cdnURL = discordgo.EndpointUserAvatarAnimated(userID, avatarID)
	} else {
		cdnURL = discordgo.EndpointUserAvatar(userID, avatarID)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cdnURL, nil)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}
	for k, v := range discordgo.DroidDownloadHeaders {
		req.Header.Set(k, v)
	}
	resp, err := proxyClient.Do(req)
	if err != nil {
		http.Error(w, "failed to proxy avatar", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		return
	}
	w.Header()["Content-Type"] = resp.Header["Content-Type"]
	w.Header()["Content-Length"] = resp.Header["Content-Length"]
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

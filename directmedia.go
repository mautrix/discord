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

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/federation"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/database"
)

type DirectMediaAPI struct {
	bridge *DiscordBridge
	ks     *federation.KeyServer
	cfg    config.DirectMedia
	log    zerolog.Logger
	proxy  http.Client

	signatureKey [32]byte

	attachmentCache     map[AttachmentCacheKey]AttachmentCacheValue
	attachmentCacheLock sync.Mutex
}

type AttachmentCacheKey struct {
	ChannelID    uint64
	AttachmentID uint64
}

type AttachmentCacheValue struct {
	URL    string
	Expiry time.Time
}

func newDirectMediaAPI(br *DiscordBridge) *DirectMediaAPI {
	if !br.Config.Bridge.DirectMedia.Enabled {
		return nil
	}
	dma := &DirectMediaAPI{
		bridge: br,
		cfg:    br.Config.Bridge.DirectMedia,
		log:    br.ZLog.With().Str("component", "direct media").Logger(),
		proxy: http.Client{
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				ForceAttemptHTTP2:   false,
			},
			Timeout: 60 * time.Second,
		},
		attachmentCache: make(map[AttachmentCacheKey]AttachmentCacheValue),
	}
	r := br.AS.Router

	parsed, err := federation.ParseSynapseKey(dma.cfg.ServerKey)
	if err != nil {
		dma.log.WithLevel(zerolog.FatalLevel).Err(err).Msg("Failed to parse server key")
		os.Exit(11)
		return nil
	}
	dma.signatureKey = sha256.Sum256(parsed.Priv.Seed())
	dma.ks = &federation.KeyServer{
		KeyProvider: &federation.StaticServerKey{
			ServerName: dma.cfg.ServerName,
			Key:        parsed,
		},
		WellKnownTarget: dma.cfg.WellKnownResponse,
		Version: federation.ServerVersion{
			Name:    br.Name,
			Version: br.Version,
		},
	}
	if dma.ks.WellKnownTarget == "" {
		dma.ks.WellKnownTarget = fmt.Sprintf("%s:443", dma.cfg.ServerName)
	}
	federationRouter := r.PathPrefix("/_matrix/federation").Subrouter()
	mediaRouter := r.PathPrefix("/_matrix/media").Subrouter()
	clientMediaRouter := r.PathPrefix("/_matrix/client/v1/media").Subrouter()
	var reqIDCounter atomic.Uint64
	middleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With, Content-Type, Authorization")
			log := dma.log.With().
				Str("remote_addr", r.RemoteAddr).
				Str("request_path", r.URL.Path).
				Uint64("req_id", reqIDCounter.Add(1)).
				Logger()
			next.ServeHTTP(w, r.WithContext(log.WithContext(r.Context())))
		})
	}
	mediaRouter.Use(middleware)
	federationRouter.Use(middleware)
	clientMediaRouter.Use(middleware)
	addRoutes := func(version string) {
		mediaRouter.HandleFunc("/"+version+"/download/{serverName}/{mediaID}", dma.DownloadMedia).Methods(http.MethodGet)
		mediaRouter.HandleFunc("/"+version+"/download/{serverName}/{mediaID}/{fileName}", dma.DownloadMedia).Methods(http.MethodGet)
		mediaRouter.HandleFunc("/"+version+"/thumbnail/{serverName}/{mediaID}", dma.DownloadMedia).Methods(http.MethodGet)
		mediaRouter.HandleFunc("/"+version+"/upload/{serverName}/{mediaID}", dma.UploadNotSupported).Methods(http.MethodPut)
		mediaRouter.HandleFunc("/"+version+"/upload", dma.UploadNotSupported).Methods(http.MethodPost)
		mediaRouter.HandleFunc("/"+version+"/create", dma.UploadNotSupported).Methods(http.MethodPost)
		mediaRouter.HandleFunc("/"+version+"/config", dma.UploadNotSupported).Methods(http.MethodGet)
		mediaRouter.HandleFunc("/"+version+"/preview_url", dma.PreviewURLNotSupported).Methods(http.MethodGet)
	}
	clientMediaRouter.HandleFunc("/download/{serverName}/{mediaID}", dma.DownloadMedia).Methods(http.MethodGet)
	clientMediaRouter.HandleFunc("/download/{serverName}/{mediaID}/{fileName}", dma.DownloadMedia).Methods(http.MethodGet)
	clientMediaRouter.HandleFunc("/thumbnail/{serverName}/{mediaID}", dma.DownloadMedia).Methods(http.MethodGet)
	clientMediaRouter.HandleFunc("/upload/{serverName}/{mediaID}", dma.UploadNotSupported).Methods(http.MethodPut)
	clientMediaRouter.HandleFunc("/upload", dma.UploadNotSupported).Methods(http.MethodPost)
	clientMediaRouter.HandleFunc("/create", dma.UploadNotSupported).Methods(http.MethodPost)
	clientMediaRouter.HandleFunc("/config", dma.UploadNotSupported).Methods(http.MethodGet)
	clientMediaRouter.HandleFunc("/preview_url", dma.PreviewURLNotSupported).Methods(http.MethodGet)
	addRoutes("v3")
	addRoutes("r0")
	addRoutes("v1")
	federationRouter.HandleFunc("/v1/media/download/{mediaID}", dma.DownloadMedia).Methods(http.MethodGet)
	federationRouter.HandleFunc("/v1/version", dma.ks.GetServerVersion).Methods(http.MethodGet)
	mediaRouter.NotFoundHandler = http.HandlerFunc(dma.UnknownEndpoint)
	mediaRouter.MethodNotAllowedHandler = http.HandlerFunc(dma.UnsupportedMethod)
	federationRouter.NotFoundHandler = http.HandlerFunc(dma.UnknownEndpoint)
	federationRouter.MethodNotAllowedHandler = http.HandlerFunc(dma.UnsupportedMethod)
	dma.ks.Register(r)

	return dma
}

func (dma *DirectMediaAPI) makeMXC(data MediaIDData) id.ContentURI {
	return id.ContentURI{
		Homeserver: dma.cfg.ServerName,
		FileID:     data.Wrap().SignedString(dma.signatureKey),
	}
}

func parseExpiryTS(addr string) time.Time {
	parsedURL, err := url.Parse(addr)
	if err != nil {
		return time.Time{}
	}
	tsBytes, err := hex.DecodeString(parsedURL.Query().Get("ex"))
	if err != nil || len(tsBytes) != 4 {
		return time.Time{}
	}
	parsedTS := int64(binary.BigEndian.Uint32(tsBytes))
	if parsedTS > time.Now().Unix() && parsedTS < time.Now().Add(365*24*time.Hour).Unix() {
		return time.Unix(parsedTS, 0)
	}
	return time.Time{}
}

func (dma *DirectMediaAPI) addAttachmentToCache(channelID uint64, att *discordgo.MessageAttachment) time.Time {
	attachmentID, err := strconv.ParseUint(att.ID, 10, 64)
	if err != nil {
		return time.Time{}
	}
	expiry := parseExpiryTS(att.URL)
	if expiry.IsZero() {
		expiry = time.Now().Add(24 * time.Hour)
	}
	dma.attachmentCache[AttachmentCacheKey{
		ChannelID:    channelID,
		AttachmentID: attachmentID,
	}] = AttachmentCacheValue{
		URL:    att.URL,
		Expiry: expiry,
	}
	return expiry
}

func (dma *DirectMediaAPI) AttachmentMXC(channelID, messageID string, att *discordgo.MessageAttachment) (mxc id.ContentURI) {
	if dma == nil {
		return
	}
	channelIDInt, err := strconv.ParseUint(channelID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("channel_id", channelID).Msg("Got non-integer channel ID")
		return
	}
	messageIDInt, err := strconv.ParseUint(messageID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("message_id", messageID).Msg("Got non-integer message ID")
		return
	}
	attachmentIDInt, err := strconv.ParseUint(att.ID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("attachment_id", att.ID).Msg("Got non-integer attachment ID")
		return
	}
	dma.attachmentCacheLock.Lock()
	dma.addAttachmentToCache(channelIDInt, att)
	dma.attachmentCacheLock.Unlock()
	return dma.makeMXC(&AttachmentMediaData{
		ChannelID:    channelIDInt,
		MessageID:    messageIDInt,
		AttachmentID: attachmentIDInt,
	})
}

func (dma *DirectMediaAPI) EmojiMXC(emojiID, name string, animated bool) (mxc id.ContentURI) {
	if dma == nil {
		return
	}
	emojiIDInt, err := strconv.ParseUint(emojiID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("emoji_id", emojiID).Msg("Got non-integer emoji ID")
		return
	}
	return dma.makeMXC(&EmojiMediaData{
		EmojiMediaDataInner: EmojiMediaDataInner{
			EmojiID:  emojiIDInt,
			Animated: animated,
		},
		Name: name,
	})
}

func (dma *DirectMediaAPI) StickerMXC(stickerID string, format discordgo.StickerFormat) (mxc id.ContentURI) {
	if dma == nil {
		return
	}
	stickerIDInt, err := strconv.ParseUint(stickerID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("sticker_id", stickerID).Msg("Got non-integer sticker ID")
		return
	} else if format > 255 || format < 0 {
		dma.log.Warn().Int("format", int(format)).Msg("Got invalid sticker format")
		return
	}
	return dma.makeMXC(&StickerMediaData{
		StickerID: stickerIDInt,
		Format:    byte(format),
	})
}

func (dma *DirectMediaAPI) AvatarMXC(guildID, userID, avatarID string) (mxc id.ContentURI) {
	if dma == nil {
		return
	}
	animated := strings.HasPrefix(avatarID, "a_")
	avatarIDBytes, err := hex.DecodeString(strings.TrimPrefix(avatarID, "a_"))
	if err != nil {
		dma.log.Warn().Str("avatar_id", avatarID).Msg("Got non-hex avatar ID")
		return
	} else if len(avatarIDBytes) != 16 {
		dma.log.Warn().Str("avatar_id", avatarID).Msg("Got invalid avatar ID length")
		return
	}
	avatarIDArray := [16]byte(avatarIDBytes)
	userIDInt, err := strconv.ParseUint(userID, 10, 64)
	if err != nil {
		dma.log.Warn().Str("user_id", userID).Msg("Got non-integer user ID")
		return
	}
	if guildID != "" {
		guildIDInt, err := strconv.ParseUint(guildID, 10, 64)
		if err != nil {
			dma.log.Warn().Str("guild_id", guildID).Msg("Got non-integer guild ID")
			return
		}
		return dma.makeMXC(&GuildMemberAvatarMediaData{
			GuildID:  guildIDInt,
			UserID:   userIDInt,
			AvatarID: avatarIDArray,
			Animated: animated,
		})
	} else {
		return dma.makeMXC(&UserAvatarMediaData{
			UserID:   userIDInt,
			AvatarID: avatarIDArray,
			Animated: animated,
		})
	}
}

type RespError struct {
	Code    string
	Message string
	Status  int
}

func (re *RespError) Error() string {
	return re.Message
}

var ErrNoUsersWithAccessFound = errors.New("no users found to fetch message")
var ErrAttachmentNotFound = errors.New("attachment not found")

func (dma *DirectMediaAPI) fetchNewAttachmentURL(ctx context.Context, meta *AttachmentMediaData) (string, time.Time, error) {
	var client *discordgo.Session
	channelIDStr := strconv.FormatUint(meta.ChannelID, 10)
	portal := dma.bridge.GetExistingPortalByID(database.PortalKey{ChannelID: channelIDStr})
	var users []id.UserID
	if portal != nil && portal.GuildID != "" {
		users = dma.bridge.DB.GetUsersInPortal(portal.GuildID)
	} else {
		users = dma.bridge.DB.GetUsersInPortal(channelIDStr)
	}
	for _, userID := range users {
		user := dma.bridge.GetCachedUserByMXID(userID)
		if user == nil || user.Session == nil {
			continue
		}
		perms, err := user.Session.State.UserChannelPermissions(user.DiscordID, channelIDStr)
		if err == nil && perms&discordgo.PermissionViewChannel == 0 {
			continue
		}
		if client == nil || err == nil {
			client = user.Session
			if !client.IsUser {
				break
			}
		}
	}
	if client == nil {
		return "", time.Time{}, ErrNoUsersWithAccessFound
	}
	var msgs []*discordgo.Message
	var err error
	messageIDStr := strconv.FormatUint(meta.MessageID, 10)
	if client.IsUser {
		var refs []discordgo.RequestOption
		if portal != nil {
			refs = append(refs, discordgo.WithChannelReferer(portal.GuildID, channelIDStr))
		}
		msgs, err = client.ChannelMessages(channelIDStr, 5, "", "", messageIDStr, refs...)
	} else {
		var msg *discordgo.Message
		msg, err = client.ChannelMessage(channelIDStr, messageIDStr)
		msgs = []*discordgo.Message{msg}
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to fetch message: %w", err)
	}
	attachmentIDStr := strconv.FormatUint(meta.AttachmentID, 10)
	var url string
	var expiry time.Time
	for _, item := range msgs {
		for _, att := range item.Attachments {
			thisExpiry := dma.addAttachmentToCache(meta.ChannelID, att)
			if att.ID == attachmentIDStr {
				url = att.URL
				expiry = thisExpiry
			}
		}
	}
	if url == "" {
		return "", time.Time{}, ErrAttachmentNotFound
	}
	return url, expiry, nil
}

func (dma *DirectMediaAPI) GetEmojiInfo(contentURI id.ContentURI) *EmojiMediaData {
	if dma == nil || contentURI.IsEmpty() || contentURI.Homeserver != dma.cfg.ServerName {
		return nil
	}
	mediaID, err := ParseMediaID(contentURI.FileID, dma.signatureKey)
	if err != nil {
		return nil
	}
	emojiData, ok := mediaID.Data.(*EmojiMediaData)
	if !ok {
		return nil
	}
	return emojiData

}

func (dma *DirectMediaAPI) getMediaURL(ctx context.Context, encodedMediaID string) (url string, expiry time.Time, err error) {
	var mediaID *MediaID
	mediaID, err = ParseMediaID(encodedMediaID, dma.signatureKey)
	if err != nil {
		err = &RespError{
			Code:    mautrix.MNotFound.ErrCode,
			Message: err.Error(),
			Status:  http.StatusNotFound,
		}
		return
	}
	switch mediaData := mediaID.Data.(type) {
	case *AttachmentMediaData:
		dma.attachmentCacheLock.Lock()
		defer dma.attachmentCacheLock.Unlock()
		cached, ok := dma.attachmentCache[mediaData.CacheKey()]
		if ok && time.Until(cached.Expiry) > 5*time.Minute {
			return cached.URL, cached.Expiry, nil
		}
		zerolog.Ctx(ctx).Debug().
			Uint64("channel_id", mediaData.ChannelID).
			Uint64("message_id", mediaData.MessageID).
			Uint64("attachment_id", mediaData.AttachmentID).
			Msg("Refreshing attachment URL")
		url, expiry, err = dma.fetchNewAttachmentURL(ctx, mediaData)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to refresh attachment URL")
			msg := "Failed to refresh attachment URL"
			if errors.Is(err, ErrNoUsersWithAccessFound) {
				msg = "No users found with access to the channel"
			} else if errors.Is(err, ErrAttachmentNotFound) {
				msg = "Attachment not found in message. Perhaps it was deleted?"
			}
			err = &RespError{
				Code:    mautrix.MNotFound.ErrCode,
				Message: msg,
				Status:  http.StatusNotFound,
			}
		} else {
			zerolog.Ctx(ctx).Debug().Time("expiry", expiry).Msg("Successfully refreshed attachment URL")
		}
	case *EmojiMediaData:
		if mediaData.Animated {
			url = discordgo.EndpointEmojiAnimated(strconv.FormatUint(mediaData.EmojiID, 10))
		} else {
			url = discordgo.EndpointEmoji(strconv.FormatUint(mediaData.EmojiID, 10))
		}
	case *StickerMediaData:
		url = discordgo.EndpointStickerImage(
			strconv.FormatUint(mediaData.StickerID, 10),
			discordgo.StickerFormat(mediaData.Format),
		)
	case *UserAvatarMediaData:
		if mediaData.Animated {
			url = discordgo.EndpointUserAvatarAnimated(
				strconv.FormatUint(mediaData.UserID, 10),
				fmt.Sprintf("a_%x", mediaData.AvatarID),
			)
		} else {
			url = discordgo.EndpointUserAvatar(
				strconv.FormatUint(mediaData.UserID, 10),
				fmt.Sprintf("%x", mediaData.AvatarID),
			)
		}
	case *GuildMemberAvatarMediaData:
		if mediaData.Animated {
			url = discordgo.EndpointGuildMemberAvatarAnimated(
				strconv.FormatUint(mediaData.GuildID, 10),
				strconv.FormatUint(mediaData.UserID, 10),
				fmt.Sprintf("a_%x", mediaData.AvatarID),
			)
		} else {
			url = discordgo.EndpointGuildMemberAvatar(
				strconv.FormatUint(mediaData.GuildID, 10),
				strconv.FormatUint(mediaData.UserID, 10),
				fmt.Sprintf("%x", mediaData.AvatarID),
			)
		}
	default:
		zerolog.Ctx(ctx).Error().Type("media_data_type", mediaData).Msg("Unrecognized media data struct")
		err = &RespError{
			Code:    "M_UNKNOWN",
			Message: "Unrecognized media data struct",
			Status:  http.StatusInternalServerError,
		}
	}
	return
}

func (dma *DirectMediaAPI) proxyDownload(ctx context.Context, w http.ResponseWriter, url, fileName string) {
	log := zerolog.Ctx(ctx)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Err(err).Str("url", url).Msg("Failed to create proxy request")
		jsonResponse(w, http.StatusInternalServerError, &mautrix.RespError{
			ErrCode: "M_UNKNOWN",
			Err:     "Failed to create proxy request",
		})
		return
	}
	for key, val := range discordgo.DroidDownloadHeaders {
		req.Header.Set(key, val)
	}
	resp, err := dma.proxy.Do(req)
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if err != nil {
		log.Err(err).Str("url", url).Msg("Failed to proxy download")
		jsonResponse(w, http.StatusServiceUnavailable, &mautrix.RespError{
			ErrCode: "M_UNKNOWN",
			Err:     "Failed to proxy download",
		})
		return
	} else if resp.StatusCode != http.StatusOK {
		log.Warn().Str("url", url).Int("status", resp.StatusCode).Msg("Unexpected status code proxying download")
		jsonResponse(w, resp.StatusCode, &mautrix.RespError{
			ErrCode: "M_UNKNOWN",
			Err:     "Unexpected status code proxying download",
		})
		return
	}
	w.Header()["Content-Type"] = resp.Header["Content-Type"]
	w.Header()["Content-Length"] = resp.Header["Content-Length"]
	w.Header()["Last-Modified"] = resp.Header["Last-Modified"]
	w.Header()["Cache-Control"] = resp.Header["Cache-Control"]
	contentDisposition := "attachment"
	switch resp.Header.Get("Content-Type") {
	case "text/css", "text/plain", "text/csv", "application/json", "application/ld+json", "image/jpeg", "image/gif",
		"image/png", "image/apng", "image/webp", "image/avif", "video/mp4", "video/webm", "video/ogg", "video/quicktime",
		"audio/mp4", "audio/webm", "audio/aac", "audio/mpeg", "audio/ogg", "audio/wave", "audio/wav", "audio/x-wav",
		"audio/x-pn-wav", "audio/flac", "audio/x-flac", "application/pdf":
		contentDisposition = "inline"
	}
	if fileName != "" {
		contentDisposition = mime.FormatMediaType(contentDisposition, map[string]string{
			"filename": fileName,
		})
	}
	w.Header().Set("Content-Disposition", contentDisposition)
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to write proxy response")
	}
}

func (dma *DirectMediaAPI) DownloadMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerolog.Ctx(ctx)
	isNewFederation := strings.HasPrefix(r.URL.Path, "/_matrix/federation/v1/media/download/")
	vars := mux.Vars(r)
	if !isNewFederation && vars["serverName"] != dma.cfg.ServerName {
		jsonResponse(w, http.StatusNotFound, &mautrix.RespError{
			ErrCode: mautrix.MNotFound.ErrCode,
			Err:     fmt.Sprintf("This is a Discord media proxy for %q, other media downloads are not available here", dma.cfg.ServerName),
		})
		return
	}
	// TODO check destination header in X-Matrix auth when isNewFederation

	url, expiresAt, err := dma.getMediaURL(ctx, vars["mediaID"])
	if err != nil {
		var respError *RespError
		if errors.As(err, &respError) {
			jsonResponse(w, respError.Status, &mautrix.RespError{
				ErrCode: respError.Code,
				Err:     respError.Message,
			})
		} else {
			log.Err(err).Str("media_id", vars["mediaID"]).Msg("Failed to get media URL")
			jsonResponse(w, http.StatusNotFound, &mautrix.RespError{
				ErrCode: mautrix.MNotFound.ErrCode,
				Err:     "Media not found",
			})
		}
		return
	}
	if isNewFederation {
		mp := multipart.NewWriter(w)
		w.Header().Set("Content-Type", strings.Replace(mp.FormDataContentType(), "form-data", "mixed", 1))
		var metaPart io.Writer
		metaPart, err = mp.CreatePart(textproto.MIMEHeader{
			"Content-Type": {"application/json"},
		})
		if err != nil {
			log.Err(err).Msg("Failed to create multipart metadata field")
			return
		}
		_, err = metaPart.Write([]byte(`{}`))
		if err != nil {
			log.Err(err).Msg("Failed to write multipart metadata field")
			return
		}
		_, err = mp.CreatePart(textproto.MIMEHeader{
			"Location": {url},
		})
		if err != nil {
			log.Err(err).Msg("Failed to create multipart redirect field")
			return
		}
		err = mp.Close()
		if err != nil {
			log.Err(err).Msg("Failed to close multipart writer")
			return
		}
		return
	}
	// Proxy if the config allows proxying and the request doesn't allow redirects.
	// In any other case, redirect to the Discord CDN.
	if dma.cfg.AllowProxy && r.URL.Query().Get("allow_redirect") != "true" {
		dma.proxyDownload(ctx, w, url, vars["fileName"])
		return
	}
	w.Header().Set("Location", url)
	expirySeconds := (time.Until(expiresAt) - 5*time.Minute).Seconds()
	if expiresAt.IsZero() {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if expirySeconds > 0 {
		cacheControl := fmt.Sprintf("public, max-age=%d, immutable", int(expirySeconds))
		w.Header().Set("Cache-Control", cacheControl)
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func (dma *DirectMediaAPI) UploadNotSupported(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusNotImplemented, &mautrix.RespError{
		ErrCode: mautrix.MUnrecognized.ErrCode,
		Err:     "This bridge only supports proxying Discord media downloads and does not support media uploads.",
	})
}

func (dma *DirectMediaAPI) PreviewURLNotSupported(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusNotImplemented, &mautrix.RespError{
		ErrCode: mautrix.MUnrecognized.ErrCode,
		Err:     "This bridge only supports proxying Discord media downloads and does not support URL previews.",
	})
}

func (dma *DirectMediaAPI) UnknownEndpoint(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusNotFound, &mautrix.RespError{
		ErrCode: mautrix.MUnrecognized.ErrCode,
		Err:     "Unrecognized endpoint",
	})
}

func (dma *DirectMediaAPI) UnsupportedMethod(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusMethodNotAllowed, &mautrix.RespError{
		ErrCode: mautrix.MUnrecognized.ErrCode,
		Err:     "Invalid method for endpoint",
	})
}

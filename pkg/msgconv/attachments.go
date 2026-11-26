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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type ExternalMediaGoneError struct {
	StatusCode int
}

func (e ExternalMediaGoneError) Error() string {
	return fmt.Sprintf("external media is no longer available (status code %d)", e.StatusCode)
}

func (e ExternalMediaGoneError) Unwrap() error {
	return bridgev2.ErrMediaDownloadFailed
}

func isGoneExternalMedia(downloadURL string, statusCode int) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound && statusCode != http.StatusUnsupportedMediaType {
		return false
	}
	parsed, err := url.Parse(downloadURL)
	return err == nil &&
		strings.HasPrefix(parsed.Host, "images-ext-") &&
		strings.HasSuffix(parsed.Host, ".discordapp.net") &&
		strings.HasPrefix(parsed.Path, "/external/")
}

func logReuploadError(ctx context.Context, err error, msg string) {
	if errors.As(err, &ExternalMediaGoneError{}) {
		zerolog.Ctx(ctx).Debug().Err(err).Msg(msg)
	} else {
		zerolog.Ctx(ctx).Warn().Err(err).Msg(msg)
	}
}

type ReuploadedAttachment struct {
	MXC      id.ContentURIString
	File     *event.EncryptedFileInfo
	Size     int
	MimeType string
}

func (mc *MessageConverter) ReuploadUnknownMedia(
	ctx context.Context,
	url string,
	allowEncryption bool,
) (*ReuploadedAttachment, error) {
	return mc.ReuploadMedia(ctx, url, "", "", -1, allowEncryption)
}

const maxReuploadCacheSize = 4096

type reuploadCache struct {
	lock    sync.Mutex
	entries map[string]*ReuploadedAttachment
}

func (rc *reuploadCache) get(url string) *ReuploadedAttachment {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	return rc.entries[url]
}

func (rc *reuploadCache) put(url string, reupload *ReuploadedAttachment) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	if rc.entries == nil {
		rc.entries = make(map[string]*ReuploadedAttachment)
	} else if len(rc.entries) >= maxReuploadCacheSize {
		for key := range rc.entries {
			delete(rc.entries, key)
			break
		}
	}
	rc.entries[url] = reupload
}

func mib(size int64) float64 {
	return float64(size) / 1024 / 1024
}

func (mc *MessageConverter) ReuploadMedia(
	ctx context.Context,
	downloadURL string,
	mimeType string,
	fileName string,
	estimatedSize int,
	allowEncryption bool,
) (*ReuploadedAttachment, error) {
	sess := ctx.Value(contextKeyDiscordClient).(*discordgo.Session)
	httpClient := sess.Client
	intent := ctx.Value(contextKeyIntent).(bridgev2.MatrixAPI)
	var roomID id.RoomID
	if allowEncryption {
		roomID = ctx.Value(contextKeyPortal).(*bridgev2.Portal).MXID
	}
	if !allowEncryption {
		if cached := mc.unencryptedReuploads.get(downloadURL); cached != nil {
			return cached, nil
		}
	}

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if sess.IsUser {
		for key, value := range discordgo.DroidDownloadHeaders {
			req.Header.Set(key, value)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 && isGoneExternalMedia(downloadURL, resp.StatusCode) {
		zerolog.Ctx(ctx).Debug().
			Str("media_url", downloadURL).
			Int("status_code", resp.StatusCode).
			Msg("External media is no longer available")
		return nil, ExternalMediaGoneError{StatusCode: resp.StatusCode}
	} else if resp.StatusCode > 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		logEvt := zerolog.Ctx(ctx).Error().
			Str("media_url", downloadURL).
			Int("status_code", resp.StatusCode)
		if json.Valid(errBody) {
			logEvt.RawJSON("error_json", errBody)
		} else {
			logEvt.Bytes("error_body", errBody)
		}
		logEvt.Msg("Media download failed")
		return nil, fmt.Errorf("%w: unexpected status code %d", bridgev2.ErrMediaDownloadFailed, resp.StatusCode)
	} else if resp.ContentLength > mc.MaxFileSize {
		return nil, fmt.Errorf("%w (%.2f MiB > %.2f MiB)", bridgev2.ErrMediaTooLarge, mib(resp.ContentLength), mib(mc.MaxFileSize))
	}

	requireFile := mimeType == ""
	var size int64
	mxc, file, err := intent.UploadMediaStream(ctx, roomID, int64(estimatedSize), requireFile, func(file io.Writer) (*bridgev2.FileStreamResult, error) {
		var mbe *http.MaxBytesError
		size, err = io.Copy(file, http.MaxBytesReader(nil, resp.Body, mc.MaxFileSize))
		if err != nil {
			if errors.As(err, &mbe) {
				return nil, fmt.Errorf("%w (over %.2f MiB)", bridgev2.ErrMediaTooLarge, mib(mc.MaxFileSize))
			}
			return nil, err
		}
		if mimeType == "" {
			mimeBuf := make([]byte, 512)
			n, err := file.(*os.File).ReadAt(mimeBuf, 0)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("couldn't read file for mime detection: %w", err)
			}
			mimeType = http.DetectContentType(mimeBuf[:n])
		}
		return &bridgev2.FileStreamResult{
			FileName: fileName,
			MimeType: mimeType,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	reupload := &ReuploadedAttachment{
		Size:     int(size),
		MXC:      mxc,
		File:     file,
		MimeType: mimeType,
	}
	if !allowEncryption {
		mc.unencryptedReuploads.put(downloadURL, reupload)
	}
	return reupload, nil
}

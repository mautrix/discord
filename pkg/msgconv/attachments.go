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
	"path"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type ReuploadedAttachment struct {
	MXC      id.ContentURIString
	File     *event.EncryptedFileInfo
	Size     int
	FileName string
	MimeType string
}

func (d *MessageConverter) ReuploadUnknownMedia(
	ctx context.Context,
	url string,
	allowEncryption bool,
) (*ReuploadedAttachment, error) {
	return d.ReuploadMedia(ctx, url, "", "", -1, allowEncryption)
}

func mib(size int64) float64 {
	return float64(size) / 1024 / 1024
}

func (d *MessageConverter) ReuploadMedia(
	ctx context.Context,
	downloadURL string,
	mimeType string,
	fileName string,
	estimatedSize int,
	allowEncryption bool,
) (*ReuploadedAttachment, error) {
	if fileName == "" {
		parsedURL, err := url.Parse(downloadURL)
		if err != nil {
			return nil, fmt.Errorf("couldn't parse URL to detect file name: %w", err)
		}
		fileName = path.Base(parsedURL.Path)
	}

	sess := ctx.Value(contextKeyDiscordClient).(*discordgo.Session)
	httpClient := sess.Client
	intent := ctx.Value(contextKeyIntent).(bridgev2.MatrixAPI)
	var roomID id.RoomID
	if allowEncryption {
		roomID = ctx.Value(contextKeyPortal).(*bridgev2.Portal).MXID
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
	if resp.StatusCode > 300 {
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
	} else if resp.ContentLength > d.MaxFileSize {
		return nil, fmt.Errorf("%w (%.2f MiB > %.2f MiB)", bridgev2.ErrMediaTooLarge, mib(resp.ContentLength), mib(d.MaxFileSize))
	}

	requireFile := mimeType == ""
	var size int64
	mxc, file, err := intent.UploadMediaStream(ctx, roomID, int64(estimatedSize), requireFile, func(file io.Writer) (*bridgev2.FileStreamResult, error) {
		var mbe *http.MaxBytesError
		size, err = io.Copy(file, http.MaxBytesReader(nil, resp.Body, d.MaxFileSize))
		if err != nil {
			if errors.As(err, &mbe) {
				return nil, fmt.Errorf("%w (over %.2f MiB)", bridgev2.ErrMediaTooLarge, mib(d.MaxFileSize))
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

	return &ReuploadedAttachment{
		Size:     int(size),
		MXC:      mxc,
		File:     file,
		FileName: fileName,
		MimeType: mimeType,
	}, nil
}

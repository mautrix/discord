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

package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

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
	if resp.Header.Get("Content-Length") != "" {
		length, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse content length: %w", err)
		} else if length > maxSize {
			return nil, fmt.Errorf("attachment too large (%d > %d)", length, maxSize)
		}
		return io.ReadAll(resp.Body)
	} else {
		var mbe *http.MaxBytesError
		data, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, maxSize))
		if err != nil && errors.As(err, &mbe) {
			return nil, fmt.Errorf("attachment too large (over %d)", maxSize)
		}
		return data, err
	}
}

type AttachmentReupload struct {
	DownloadingURL string
	FileName       string
	MimeType       string
}

type ReuploadedAttachment struct {
	AttachmentReupload
	DownloadedSize int
	MXC            id.ContentURIString
	EncryptedFile  *event.EncryptedFileInfo
}

func (d *DiscordConnector) ReuploadMedia(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, upload AttachmentReupload) (*ReuploadedAttachment, error) {
	log := zerolog.Ctx(ctx)
	// TODO(skip): Do we need to check if we've already downloaded this media before?
	// TODO(skip): Read a maximum size from the config.
	data, err := downloadDiscordAttachment(http.DefaultClient, upload.DownloadingURL, 1_024*1_024*50)
	if err != nil {
		return nil, fmt.Errorf("couldn't download attachment for reupload: %w", err)
	}

	if upload.FileName == "" {
		url, err := url.Parse(upload.DownloadingURL)
		if err != nil {
			return nil, fmt.Errorf("couldn't parse URL to download for media reupload: %w", err)
		}
		fileName := path.Base(url.Path)
		upload.FileName = fileName
		log.Trace().Str("detected_file_name", fileName).Msg("Inferred the file name of the media we're reuploading")
	}

	if upload.MimeType == "" {
		mime := http.DetectContentType(data)
		upload.MimeType = mime
		log.Trace().Str("detected_mime_type", mime).Msg("Inferred the mime type of the media we're reuploading")
	}

	log.Trace().Stringer("portal_mxid", portal.MXID).
		Int("attachment_size", len(data)).
		Str("file_name", upload.FileName).
		Str("mime_type", upload.MimeType).
		Msg("Uploading downloaded media")
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, upload.FileName, upload.MimeType)
	if err != nil {
		return nil, err
	}

	return &ReuploadedAttachment{
		AttachmentReupload: upload,
		DownloadedSize:     len(data),
		MXC:                mxc,
		EncryptedFile:      file,
	}, nil
}

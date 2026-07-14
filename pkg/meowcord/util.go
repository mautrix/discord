// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright 2015-2016 Bruce Marriner <bruce@sqls.net>.  All rights reserved.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This file is derived from discordgo (https://github.com/bwmarrin/discordgo),
// used under the BSD-3-Clause license; see README.md in this directory. This
// file is distributed under the GNU AGPLv3 as follows:
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

package meowcord

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// SnowflakeTimestamp returns the creation time of a Snowflake ID relative to the creation of Discord.
func SnowflakeTimestamp(ID string) (t time.Time, err error) {
	i, err := strconv.ParseInt(ID, 10, 64)
	if err != nil {
		return
	}
	timestamp := (i >> 22) + 1420070400000
	t = time.Unix(0, timestamp*1000000)
	return
}

// MultipartBodyWithJSON returns the contentType and body for a discord request
// data  : The object to encode for payload_json in the multipart request
// files : Files to include in the request
func MultipartBodyWithJSON(data interface{}, files []*File) (requestContentType string, requestBody []byte, err error) {
	body := &bytes.Buffer{}
	bodywriter := multipart.NewWriter(body)

	payload, err := Marshal(data)
	if err != nil {
		return
	}

	var p io.Writer

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="payload_json"`)
	h.Set("Content-Type", "application/json")

	p, err = bodywriter.CreatePart(h)
	if err != nil {
		return
	}

	if _, err = p.Write(payload); err != nil {
		return
	}

	for i, file := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[%d]"; filename="%s"`, i, quoteEscaper.Replace(file.Name)))
		contentType := file.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		h.Set("Content-Type", contentType)

		p, err = bodywriter.CreatePart(h)
		if err != nil {
			return
		}

		if _, err = io.Copy(p, file.Reader); err != nil {
			return
		}
	}

	err = bodywriter.Close()
	if err != nil {
		return
	}

	return bodywriter.FormDataContentType(), body.Bytes(), nil
}

func withSize(URL, size string) string {
	// FIXME This is gross. Ideally the endpoint functions themselves should be
	// changed.

	if size == "" {
		return URL
	}

	sep := "?"
	if strings.Contains(URL, "?") {
		// The URL already has query parameters.
		sep = "&"
	}
	return URL + sep + "size=" + size
}

func avatarURL(avatarHash, defaultAvatarURL, staticAvatarURL, animatedAvatarURL, size string) string {
	var URL string
	if avatarHash == "" {
		URL = defaultAvatarURL
	} else if strings.HasPrefix(avatarHash, "a_") {
		URL = animatedAvatarURL
	} else {
		URL = staticAvatarURL
	}

	return withSize(URL, size)
}

func bannerURL(bannerHash, staticBannerURL, animatedBannerURL, size string) string {
	var URL string
	if bannerHash == "" {
		return ""
	} else if strings.HasPrefix(bannerHash, "a_") {
		URL = animatedBannerURL
	} else {
		URL = staticBannerURL
	}

	return withSize(URL, size)
}

func iconURL(iconHash, staticIconURL, animatedIconURL, size string) string {
	var URL string
	if iconHash == "" {
		return ""
	} else if strings.HasPrefix(iconHash, "a_") {
		URL = animatedIconURL
	} else {
		URL = staticIconURL
	}

	return withSize(URL, size)
}

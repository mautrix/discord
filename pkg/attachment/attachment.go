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

package attachment

import (
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TODO(skip): These types are only in a leaf package to avoid import cycles.
// Perhaps figure out a better way to structure this so that this package is unnecessary.

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

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
	"math/rand"
	"strconv"
	"sync/atomic"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/attachment"
)

type MediaReuploader func(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, reupload attachment.AttachmentReupload) (*attachment.ReuploadedAttachment, error)

type MessageConverter struct {
	Bridge *bridgev2.Bridge

	nextDiscordUploadID atomic.Int32

	// ReuploadMedia is called when the message converter wants to upload some
	// media it is attempting to bridge.
	//
	// This can be directly forwarded to the ReuploadMedia method on DiscordConnector.
	// The indirection is only necessary to prevent an import cycle.
	ReuploadMedia MediaReuploader
}

func NewMessageConverter(bridge *bridgev2.Bridge, reuploader MediaReuploader) *MessageConverter {
	mc := &MessageConverter{
		Bridge:        bridge,
		ReuploadMedia: reuploader,
	}

	mc.nextDiscordUploadID.Store(rand.Int31n(100))
	return mc
}

func (mc *MessageConverter) NextDiscordUploadID() string {
	val := mc.nextDiscordUploadID.Add(2)
	return strconv.Itoa(int(val))
}

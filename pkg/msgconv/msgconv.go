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
	"math/rand"
	"strconv"
	"sync/atomic"

	"maunium.net/go/mautrix/bridgev2"
)

type MessageConverter struct {
	Bridge *bridgev2.Bridge

	nextDiscordUploadID atomic.Int32

	MaxFileSize int64
}

func NewMessageConverter(bridge *bridgev2.Bridge) *MessageConverter {
	mc := &MessageConverter{
		Bridge:      bridge,
		MaxFileSize: 50 * 1024 * 1024,
	}

	mc.nextDiscordUploadID.Store(rand.Int31n(100))
	return mc
}

func (mc *MessageConverter) NextDiscordUploadID() string {
	val := mc.nextDiscordUploadID.Add(2)
	return strconv.Itoa(int(val))
}

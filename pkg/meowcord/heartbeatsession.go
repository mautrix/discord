// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 The mautrix-discord contributors
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
	"time"

	"github.com/google/uuid"
)

type HeartbeatSession struct {
	CreatedAt         time.Time `json:"createdAtTimestamp"`
	LastUsedTimestamp time.Time `json:"lastUsedTimestamp"`
	ID                uuid.UUID `json:"uuid"`
	// "version": 1
}

func NewHeartbeatSession() HeartbeatSession {
	now := time.Now()
	return HeartbeatSession{
		CreatedAt:         now,
		LastUsedTimestamp: now,
		ID:                uuid.New(),
	}
}

// BumpLastUsed updates the last used timestamp to the current time.
func (hbs *HeartbeatSession) BumpLastUsed() {
	if hbs == nil {
		return
	}
	hbs.LastUsedTimestamp = time.Now()
}

// IsExpired reports whether the heartbeat session should be discarded in favor
// of a new one.
func (hbs *HeartbeatSession) IsExpired() bool {
	if hbs == nil {
		return true
	}
	return time.Since(hbs.LastUsedTimestamp) >= (time.Minute * 30)
}

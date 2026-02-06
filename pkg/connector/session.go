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

package connector

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
)

func NewDiscordSession(ctx context.Context, token string) (*discordgo.Session, error) {
	log := zerolog.Ctx(ctx)

	session, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("couldn't create discord session: %w", err)
	}

	// Don't bother tracking things we don't care/support right now. Presences
	// are especially expensive to track as they occur extremely frequently.
	session.State.TrackPresences = false
	session.State.TrackVoice = false

	// Set up logging.
	session.LogLevel = discordgo.LogInformational
	session.Logger = func(msgL, caller int, format string, a ...any) {
		// FIXME(skip): Hook up zerolog properly.
		log.Debug().Str("component", "discordgo").Msgf(strings.TrimSpace(format), a...) // zerolog-allow-msgf
	}

	return session, nil
}

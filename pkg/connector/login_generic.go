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

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// DiscordGenericLogin is embedded within each struct that implements
// bridgev2.LoginProcess in order to encapsulate the common behavior that needs
// to occur after procuring a valid user token. Namely, creating a gateway
// connection to Discord and an associated UserLogin to wrap things up.
//
// It also implements a baseline Cancel method that closes the gateway
// connection.
type DiscordGenericLogin struct {
	User      *bridgev2.User
	connector *DiscordConnector

	Session *discordgo.Session

	// The Discord user we've authenticated as. This is only non-nil if
	// a call to FinalizeCreatingLogin has succeeded.
	DiscordUser *discordgo.User
}

func (dl *DiscordGenericLogin) FinalizeCreatingLogin(ctx context.Context, token string) (*bridgev2.UserLogin, error) {
	log := zerolog.Ctx(ctx).With().Str("action", "finalize login").Logger()

	// TODO we don't need an entire discordgo session for this as we're just
	// interested in /users/@me
	log.Info().Msg("Creating initial session with provided token")
	session, err := NewDiscordSession(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("couldn't create discord session: %w", err)
	}
	dl.Session = session

	log.Info().Msg("Requesting @me with provided token")
	self, err := session.User("@me")
	if err != nil {
		return nil, fmt.Errorf("couldn't request self user (bad credentials?): %w", err)
	}
	dl.DiscordUser = self

	log.Info().Msg("Fetched @me")
	ul, err := dl.User.NewLogin(ctx, &database.UserLogin{
		ID: discordid.MakeUserLoginID(self.ID),
		Metadata: &discordid.UserLoginMetadata{
			Token:            token,
			HeartbeatSession: session.HeartbeatSession,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		dl.Cancel()
		return nil, fmt.Errorf("couldn't create login during finalization: %w", err)
	}

	(ul.Client.(*DiscordClient)).Connect(ctx)

	return ul, nil
}

func (dl *DiscordGenericLogin) CompleteInstructions() string {
	return fmt.Sprintf("Logged in as %s", dl.DiscordUser.Username)
}

func (dl *DiscordGenericLogin) Cancel() {
	if dl.Session != nil {
		dl.User.Log.Debug().Msg("Login cancelled, closing session")
		err := dl.Session.Close()
		if err != nil {
			dl.User.Log.Err(err).Msg("Couldn't close Discord session in response to login cancellation")
		}
	}
}

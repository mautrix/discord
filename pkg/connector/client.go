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
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/status"
)

type DiscordClient struct {
	UserLogin *bridgev2.UserLogin
	Session   *discordgo.Session
}

func (d *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	log := login.Log
	meta := login.Metadata.(*UserLoginMetadata)

	session, err := discordgo.New(meta.Token)
	if meta.HeartbeatSession.IsExpired() {
		log.Info().Msg("Heartbeat session expired, creating a new one")
		meta.HeartbeatSession = discordgo.NewHeartbeatSession()
	}
	meta.HeartbeatSession.BumpLastUsed()
	session.HeartbeatSession = meta.HeartbeatSession
	login.Save(ctx)

	if err != nil {
		return err
	}

	// FIXME(skip): Implement.
	session.EventHandler = func(evt any) {}

	login.Client = &DiscordClient{
		UserLogin: login,
		Session:   session,
	}

	return nil
}

var _ bridgev2.NetworkAPI = (*DiscordClient)(nil)

func (d *DiscordClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	if d.Session == nil {
		log.Error().Msg("No session present")
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "discord-not-logged-in",
		})
		return
	}

	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnecting,
	})
	if err := d.connect(ctx); err != nil {
		log.Err(err).Msg("Couldn't connect to Discord")
	}
	// TODO(skip): Use event handler and send this in response to READY/RESUMED instead?
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})
}

func (cl *DiscordClient) connect(ctx context.Context) error {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Opening session")

	err := cl.Session.Open()
	for attempts := 0; errors.Is(err, discordgo.ErrImmediateDisconnect) && attempts < 2; attempts += 1 {
		log.Err(err).Int("attempts", attempts).Msg("Immediately disconnected while trying to open session, trying again in 5 seconds")
		time.Sleep(5 * time.Second)
		err = cl.Session.Open()
	}
	if err != nil {
		log.Err(err).Msg("Failed to connect to Discord")
		return err
	}

	// Ensure that we actually have a user.
	if !cl.IsLoggedIn() {
		return fmt.Errorf("unknown identity even after connecting to Discord")
	}
	user := cl.Session.State.User
	log.Info().Str("user_id", user.ID).Str("user_username", user.Username).Msg("Connected to Discord")

	if cl.UserLogin != nil {
		// Feels a bit hacky to check for this here, but it should be true when
		// logging in initially. The UserLogin is only ever created if we know
		// that we connected successfully. We _do_ know that by now here, but we're
		// not tasked with creating the UserLogin; the login code is. Alas.

		// FIXME(skip): Avatar.
		cl.UserLogin.RemoteProfile = status.RemoteProfile{
			Email: user.Email,
			Phone: user.Phone,
			Name:  user.String(),
		}
		if err := cl.UserLogin.Save(ctx); err != nil {
			log.Err(err).Msg("Couldn't save UserLogin after connecting")
		}
	}

	return nil
}

func (d *DiscordClient) Disconnect() {
	d.UserLogin.Log.Info().Msg("Disconnecting session")
	d.Session.Close()
	d.Session = nil
}

func (d *DiscordClient) IsLoggedIn() bool {
	return d.Session != nil && d.Session.State != nil && d.Session.State.User != nil && d.Session.State.User.ID != ""
}

func (d *DiscordClient) LogoutRemote(ctx context.Context) {
	// FIXME(skip): Implement.
	d.Disconnect()
}

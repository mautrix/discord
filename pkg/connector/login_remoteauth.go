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

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/remoteauth"
)

const LoginFlowIDRemoteAuth = "fi.mau.discord.login.remote_auth"

type DiscordRemoteAuthLogin struct {
	*DiscordGenericLogin

	hasClosed        bool
	remoteAuthClient *remoteauth.Client
	qrChan           chan string
	doneChan         chan struct{}
}

var _ bridgev2.LoginProcessDisplayAndWait = (*DiscordRemoteAuthLogin)(nil)

func (dl *DiscordRemoteAuthLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	log := zerolog.Ctx(ctx)

	log.Debug().Msg("Creating new remoteauth client")
	client, err := remoteauth.New()
	if err != nil {
		return nil, fmt.Errorf("couldn't create Discord remoteauth client: %w", err)
	}

	dl.remoteAuthClient = client

	dl.qrChan = make(chan string)
	dl.doneChan = make(chan struct{})

	log.Info().Msg("Starting the QR code login process")
	err = client.Dial(ctx, dl.qrChan, dl.doneChan)
	if err != nil {
		log.Err(err).Msg("Couldn't connect to Discord remoteauth websocket")
		close(dl.qrChan)
		close(dl.doneChan)
		return nil, fmt.Errorf("couldn't connect to Discord remoteauth websocket: %w", err)
	}

	log.Info().Msg("Waiting for QR code to be ready")

	select {
	case qrCode := <-dl.qrChan:
		log.Info().Int("qr_code_data_len", len(qrCode)).Msg("Received QR code, creating login step")

		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeDisplayAndWait,
			StepID:       "fi.mau.discord.qr",
			Instructions: "On your phone, find “Scan QR Code” in Discord’s settings.",
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
				Type: bridgev2.LoginDisplayTypeQR,
				Data: qrCode,
			},
		}, nil
	case <-ctx.Done():
		log.Debug().Msg("Cancelled while waiting for QR code")
		return nil, nil
	}
}

// Wait implements bridgev2.LoginProcessDisplayAndWait.
func (dl *DiscordRemoteAuthLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if dl.doneChan == nil {
		panic("can't wait for discord remoteauth without a doneChan")
	}

	log := zerolog.Ctx(ctx)

	log.Debug().Msg("Waiting for remoteauth")
	select {
	case <-dl.doneChan:
		user, err := dl.remoteAuthClient.Result()
		if err != nil {
			log.Err(err).Msg("Discord remoteauth failed")
			return nil, fmt.Errorf("discord remoteauth failed: %w", err)
		}
		log.Debug().Msg("Discord remoteauth succeeded")

		return dl.finalizeSuccessfulLogin(ctx, user)
	case <-ctx.Done():
		log.Debug().Msg("Cancelled while waiting for remoteauth to complete")
		return nil, nil
	}
}

func (dl *DiscordRemoteAuthLogin) finalizeSuccessfulLogin(ctx context.Context, user remoteauth.User) (*bridgev2.LoginStep, error) {
	ul, err := dl.FinalizeCreatingLogin(ctx, user.Token)
	if err != nil {
		return nil, fmt.Errorf("couldn't log in via remoteauth: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: dl.CompleteInstructions(),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

func (dl *DiscordRemoteAuthLogin) Cancel() {
	// Tolerate multiple attempts to cancel.
	if dl.hasClosed {
		return
	}
	dl.hasClosed = true

	dl.User.Log.Debug().Msg("Discord remoteauth cancelled")
	dl.DiscordGenericLogin.Cancel()

	// remoteauth.Client doesn't seem to expose a cancellation method.
	close(dl.doneChan)
	close(dl.qrChan)
}

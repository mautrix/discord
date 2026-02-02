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
	"errors"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/status"
)

const (
	DiscordNotLoggedIn         status.BridgeStateErrorCode = "dc-not-logged-in"
	DiscordTransientDisconnect status.BridgeStateErrorCode = "dc-transient-disconnect"
	DiscordInvalidAuth         status.BridgeStateErrorCode = "dc-websocket-disconnect-4004"
	DiscordHTTP40002           status.BridgeStateErrorCode = "dc-http-40002"
	DiscordUnknownWebsocketErr status.BridgeStateErrorCode = "dc-unknown-websocket-error"
)

const discordDisconnectDebounce = 7 * time.Second

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		DiscordNotLoggedIn:         "You're not logged into Discord. Relogin to continue using the bridge.",
		DiscordTransientDisconnect: "Temporarily disconnected from Discord, trying to reconnect.",
		DiscordInvalidAuth:         "Discord access token is no longer valid, please log in again.",
		DiscordHTTP40002:           "Discord requires a verified account, please verify and log in again.",
		DiscordUnknownWebsocketErr: "Unknown Discord websocket error.",
	})
}

func (d *DiscordClient) resetBridgeStateTracking() {
	d.bridgeStateLock.Lock()
	if d.disconnectTimer != nil {
		d.disconnectTimer.Stop()
		d.disconnectTimer = nil
	}
	d.invalidAuthDetected = false
	d.bridgeStateLock.Unlock()
}

func (d *DiscordClient) markConnected() {
	if d.UserLogin == nil {
		return
	}
	d.bridgeStateLock.Lock()
	if d.disconnectTimer != nil {
		d.disconnectTimer.Stop()
		d.disconnectTimer = nil
	}
	d.invalidAuthDetected = false
	d.bridgeStateLock.Unlock()
	d.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

func (d *DiscordClient) markInvalidAuth(message string) {
	if d.UserLogin == nil {
		return
	}
	d.bridgeStateLock.Lock()
	d.invalidAuthDetected = true
	if d.disconnectTimer != nil {
		d.disconnectTimer.Stop()
		d.disconnectTimer = nil
	}
	d.bridgeStateLock.Unlock()
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      DiscordInvalidAuth,
		Message:    message,
		UserAction: status.UserActionRelogin,
	})
}

func (d *DiscordClient) scheduleTransientDisconnect(message string) {
	if d.UserLogin == nil {
		return
	}
	d.bridgeStateLock.Lock()
	if d.invalidAuthDetected {
		d.bridgeStateLock.Unlock()
		return
	}
	if d.disconnectTimer != nil {
		d.disconnectTimer.Stop()
	}
	login := d.UserLogin
	d.disconnectTimer = time.AfterFunc(discordDisconnectDebounce, func() {
		d.bridgeStateLock.Lock()
		d.disconnectTimer = nil
		invalidAuth := d.invalidAuthDetected
		d.bridgeStateLock.Unlock()
		if invalidAuth {
			return
		}
		login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      DiscordTransientDisconnect,
			Message:    message,
		})
	})
	d.bridgeStateLock.Unlock()
}

func (d *DiscordClient) sendConnectFailure(err error, final bool) {
	if d.UserLogin == nil || err == nil {
		return
	}
	stateEvent := status.StateTransientDisconnect
	if final {
		stateEvent = status.StateUnknownError
	}
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: stateEvent,
		Error:      DiscordUnknownWebsocketErr,
		Message:    err.Error(),
		Info: map[string]any{
			"go_error": err.Error(),
		},
	})
}

func (d *DiscordClient) handlePossible40002(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Message == nil || restErr.Message.Code != discordgo.ErrCodeActionRequiredVerifiedAccount {
		return false
	}
	if d.UserLogin == nil {
		return true
	}
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      DiscordHTTP40002,
		Message:    restErr.Message.Message,
		UserAction: status.UserActionRelogin,
	})
	return true
}

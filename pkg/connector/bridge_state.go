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
	"time"

	"maunium.net/go/mautrix/bridgev2/status"
)

const (
	DiscordNotLoggedIn   status.BridgeStateErrorCode = "discord-not-logged-in"
	DiscordInvalidAuth   status.BridgeStateErrorCode = "discord-invalid-auth"
	DiscordDisconnected  status.BridgeStateErrorCode = "discord-disconnected"
	DiscordConnectFailed status.BridgeStateErrorCode = "discord-connect-failed"
)

const discordDisconnectDebounce = 7 * time.Second

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		DiscordNotLoggedIn:   "You're not logged into Discord. Relogin to continue using the bridge.",
		DiscordInvalidAuth:   "You were logged out of Discord. Relogin to continue using the bridge.",
		DiscordDisconnected:  "Disconnected from Discord. Trying to reconnect.",
		DiscordConnectFailed: "Connecting to Discord failed.",
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
			Error:      DiscordDisconnected,
			Message:    message,
		})
	})
	d.bridgeStateLock.Unlock()
}

func (d *DiscordClient) sendConnectFailure(err error) {
	if d.UserLogin == nil || err == nil {
		return
	}
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      DiscordConnectFailed,
		Message:    err.Error(),
		Info: map[string]any{
			"go_error": err.Error(),
		},
	})
}

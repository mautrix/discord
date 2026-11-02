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
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

func (d *DiscordConnector) registerCommands() {
	d.Bridge.Commands.(*commands.Processor).AddHandlers(
		cmdSetRelay, cmdUnsetRelay,
		cmdGuilds, cmdBridge, cmdCreatePortal, cmdUnbridge, cmdDeletePortal,
		cmdLogin, cmdLoginQR, cmdLoginToken, cmdLogout, cmdPing, cmdDisconnect, cmdReconnect, cmdRejoinSpace,
	)
}

func commandClient(ce *commands.Event, connected bool) *DiscordClient {
	login := ce.User.GetDefaultLogin()
	if ce.Portal != nil {
		preferred, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
		if err == nil && preferred != nil {
			login = preferred
		}
	}
	if login == nil {
		ce.Reply("You're not logged in")
		return nil
	}
	client, ok := login.Client.(*DiscordClient)
	if !ok || (connected && !client.IsLoggedIn()) {
		ce.Reply("You're not connected to Discord. Use `$cmdprefix reconnect` to reconnect.")
		return nil
	}
	return client
}

var cmdLogin = &commands.FullHandler{
	Name:                    "login",
	Help:                    commands.CommandLogin.Help,
	RequiresLoginPermission: true,
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			ce.Args = []string{LoginFlowIDRemoteAuth}
		}
		commands.CommandLogin.Run(ce)
	},
}

var cmdLoginQR = &commands.FullHandler{
	Name:                    "login-qr",
	Help:                    commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Log in by scanning a QR code."},
	RequiresLoginPermission: true,
	Func: func(ce *commands.Event) {
		ce.Command = "login"
		ce.Args = []string{LoginFlowIDRemoteAuth}
		commands.CommandLogin.Run(ce)
	},
}

var cmdLoginToken = &commands.FullHandler{
	Name:                    "login-token",
	Help:                    commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Log in with a Discord token.", Args: "<user/bot/oauth> <token>"},
	RequiresLoginPermission: true,
	Func: func(ce *commands.Event) {
		ce.Redact()
		if len(ce.Args) != 2 {
			ce.Reply("Usage: `$cmdprefix login-token <user/bot/oauth> <token>`")
			return
		}
		if ce.User.GetDefaultLogin() != nil {
			ce.Reply("You're already logged in")
			return
		}
		token, err := commandToken(ce.Args[0], ce.Args[1])
		if err != nil {
			ce.Reply("%s", err)
			return
		}
		if ce.User.HasTooManyLogins() {
			ce.Reply("You have reached the maximum number of logins")
			return
		}
		login := &DiscordGenericLogin{connector: ce.Bridge.Network.(*DiscordConnector), User: ce.User}
		if _, err = login.FinalizeCreatingLogin(ce.Ctx, token); err != nil {
			ce.Reply("Error logging in: %v", err)
		} else {
			ce.Reply("%s", login.CompleteInstructions())
		}
	},
}

func commandToken(kind, token string) (string, error) {
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("invalid token")
	}
	switch strings.ToLower(kind) {
	case "user":
		return token, nil
	case "bot":
		return "Bot " + token, nil
	case "oauth":
		return "Bearer " + token, nil
	default:
		return "", fmt.Errorf("token type must be user, bot or oauth")
	}
}

var cmdLogout = &commands.FullHandler{
	Name: "logout",
	Help: commands.CommandLogout.Help,
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			login := ce.User.GetDefaultLogin()
			if login == nil {
				ce.Reply("You're not logged in")
				return
			}
			ce.Args = []string{string(login.ID)}
		}
		commands.CommandLogout.Run(ce)
	},
}

var cmdPing = &commands.FullHandler{
	Name: "ping",
	Help: commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Check your connection to Discord."},
	Func: func(ce *commands.Event) {
		client := commandClient(ce, false)
		if client == nil {
			return
		}
		if !client.gatewayReady.Load() {
			ce.Reply("You have a Discord token stored, but are not connected.")
		} else if !client.IsLoggedIn() {
			ce.Reply("You're connected, but your Discord account requires attention.")
		} else {
			ce.Reply("You're logged in as @%s (`%s`)", client.Session.State.User.Username, client.UserLogin.ID)
		}
	},
}

var cmdDisconnect = &commands.FullHandler{
	Name:          "disconnect",
	Help:          commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Disconnect from Discord without logging out."},
	RequiresLogin: true,
	Func: func(ce *commands.Event) {
		if client := commandClient(ce, false); client != nil {
			client.UserLogin.Disconnect()
			ce.Reply("Successfully disconnected")
		}
	},
}

var cmdReconnect = &commands.FullHandler{
	Name: "reconnect", Aliases: []string{"connect"},
	Help:          commands.HelpMeta{Section: commands.HelpSectionAuth, Description: "Reconnect to Discord after disconnecting."},
	RequiresLogin: true,
	Func: func(ce *commands.Event) {
		if client := commandClient(ce, false); client != nil {
			if client.gatewayReady.Load() {
				ce.Reply("You're already connected")
				return
			}
			client.Disconnect()
			client.Connect(ce.Ctx)
			ce.Reply("Reconnecting to Discord")
		}
	},
}

var cmdRejoinSpace = &commands.FullHandler{
	Name:          "rejoin-space",
	Help:          commands.HelpMeta{Section: commands.HelpSectionChats, Description: "Ask for an invite to your bridge space.", Args: "<main/dms>"},
	RequiresLogin: true,
	Func: func(ce *commands.Event) {
		if len(ce.Args) != 1 || (ce.Args[0] != "main" && ce.Args[0] != "dms") {
			ce.Reply("Usage: `$cmdprefix rejoin-space <main/dms>`")
			return
		}
		client := commandClient(ce, false)
		if client == nil {
			return
		}
		room, err := client.UserLogin.GetSpaceRoom(ce.Ctx)
		if err == nil && room != "" {
			err = ce.Bot.EnsureInvited(ce.Ctx, room, ce.User.MXID)
		}
		if err != nil {
			ce.Reply("Failed to invite you to your space: %v", err)
		} else if room == "" {
			ce.Reply("Spaces are disabled in this bridge")
		} else {
			ce.Reply("Invited you to your space ([link](%s))", room.URI().MatrixToURL())
		}
	},
}

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

package discordid

import "strings"

type GuildBridgingMode int

const (
	// GuildBridgeNothing tells the bridge to never bridge messages, not even checking if a portal exists.
	GuildBridgeNothing GuildBridgingMode = iota
	// GuildBridgeIfPortalExists tells the bridge to bridge messages in channels that already have portals.
	GuildBridgeIfPortalExists
	// GuildBridgeCreateOnMessage tells the bridge to create portals as soon as a message is received.
	GuildBridgeCreateOnMessage
	// GuildBridgeEverything tells the bridge to proactively create portals on startup and when receiving channel create notifications.
	GuildBridgeEverything

	GuildBridgeInvalid GuildBridgingMode = -1
)

func ParseGuildBridgingMode(str string) GuildBridgingMode {
	str = strings.ToLower(str)
	str = strings.ReplaceAll(str, "-", "")
	str = strings.ReplaceAll(str, "_", "")
	switch str {
	case "nothing", "0":
		return GuildBridgeNothing
	case "ifportalexists", "1":
		return GuildBridgeIfPortalExists
	case "createonmessage", "2":
		return GuildBridgeCreateOnMessage
	case "everything", "3":
		return GuildBridgeEverything
	default:
		return GuildBridgeInvalid
	}
}

func (gbm GuildBridgingMode) String() string {
	switch gbm {
	case GuildBridgeNothing:
		return "nothing"
	case GuildBridgeIfPortalExists:
		return "if-portal-exists"
	case GuildBridgeCreateOnMessage:
		return "create-on-message"
	case GuildBridgeEverything:
		return "everything"
	default:
		return ""
	}
}

func (gbm GuildBridgingMode) Description() string {
	switch gbm {
	case GuildBridgeNothing:
		return "never bridge messages"
	case GuildBridgeIfPortalExists:
		return "bridge messages in existing portals"
	case GuildBridgeCreateOnMessage:
		return "bridge all messages and create portals on first message"
	case GuildBridgeEverything:
		return "bridge all messages and create portals proactively"
	default:
		return ""
	}
}

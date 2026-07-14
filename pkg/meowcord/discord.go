// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright 2015-2016 Bruce Marriner <bruce@sqls.net>.  All rights reserved.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This file is derived from discordgo (https://github.com/bwmarrin/discordgo),
// used under the BSD-3-Clause license; see README.md in this directory. This
// file is distributed under the GNU AGPLv3 as follows:
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

// This file contains high level helper functions and easy entry points for the
// entire discordgo package.  These functions are being developed and are very
// experimental at this point.  They will most likely change so please use the
// low level functions if that's a problem.

// package meowcord provides Discord binding for Go
package meowcord

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// VERSION of DiscordGo, follows Semantic Versioning. (http://semver.org/)
const VERSION = "0.29.0"

// New creates a new Discord session with provided token.
// If the token is for a bot, it must be prefixed with "Bot "
//
//	e.g. "Bot ..."
//
// Or if it is an OAuth2 token, it must be prefixed with "Bearer "
//
//	e.g. "Bearer ..."
func New(token string) (s *Session, err error) {
	// Create an empty Session interface.
	s = &Session{
		State:                              NewState(),
		Ratelimiter:                        NewRatelimiter(),
		StateEnabled:                       true,
		Compress:                           true,
		ShouldReconnectOnError:             true,
		ShouldReconnectVoiceOnSessionError: true,
		ShouldRetryOnRateLimit:             true,
		ShardID:                            0,
		ShardCount:                         1,
		MaxRestRetries:                     3,
		Client:                             &http.Client{Timeout: (20 * time.Second)},
		// GatewayHTTPClient is only used to shake hands with the Discord
		// Gateway.
		GatewayHTTPClient:  http.DefaultClient,
		GatewayDialTimeout: 45 * time.Second,
		UserAgent:          "DiscordBot (https://github.com/bwmarrin/discordgo, v" + VERSION + ")",
		sequence:           new(int64),
		LastHeartbeatAck:   time.Now().UTC(),
	}

	// Initialize the Identify Package with defaults
	// These can be modified prior to calling Open()
	s.Identify.Compress = true
	s.Identify.LargeThreshold = 250
	s.Identify.Properties = &IdentifyProperties{
		OS:      runtime.GOOS,
		Browser: "DiscordGo v" + VERSION,
	}
	s.Identify.Intents = IntentsAll
	s.Identify.Token = token
	s.Token = token

	if token != "" && !strings.HasPrefix(token, "Bot ") {
		sig, err := NewVanillaSignature()
		if err != nil {
			return nil, err
		}

		s.Identify.Presence.Activities = make([]Activity, 0)
		s.Identify.Compress = false
		s.Identify.LargeThreshold = 0
		s.Identify.Presence.Status = droidStatus
		s.Identify.Presence.AFK = true

		s.Identify.Capabilities = droidCapabilities
		s.Identify.ClientState = &ClientState{
			//HighestLastMessageID:     "0",
			//ReadStateVersion:         0,
			//UserGuildSettingsVersion: -1,
			//UserSettingsVersion:      -1,
			//PrivateChannelsVersion:   "0",
			//APICodeVersion:           0,
		}
		s.Identify.Intents = 0

		s.UserAgent = DroidBrowserUserAgent

		s.launchSignature = sig
		s.launchID = uuid.New()
		s.HeartbeatSession = NewHeartbeatSession()
		s.UpdateUserHeaders()

		s.IsUser = true
	}

	return
}

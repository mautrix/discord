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
	_ "embed"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	Guilds struct {
		BridgingGuildIDs []string `yaml:"bridging_guild_ids"`
	} `yaml:"guilds"`

	CustomEmojiReactions *bool `yaml:"custom_emoji_reactions"`
	GuildAvatarsInRooms  *bool `yaml:"guild_avatars_in_rooms"`
}

func (c Config) CustomEmojiReactionsEnabled() bool {
	return c.CustomEmojiReactions == nil || *c.CustomEmojiReactions
}

func (c Config) GuildAvatarsInRoomsEnabled() bool {
	return c.GuildAvatarsInRooms != nil && *c.GuildAvatarsInRooms
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.List, "guilds", "bridging_guild_ids")
	helper.Copy(up.Bool, "guilds", "guild_avatars_in_rooms")
	helper.Copy(up.Bool, "custom_emoji_reactions")
}

func (d *DiscordConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &d.Config, up.SimpleUpgrader(upgradeConfig)
}

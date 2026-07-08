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
	"strings"
	"text/template"

	"github.com/bwmarrin/discordgo"
	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

const defaultChannelNameTemplate = `{{if and .IsGuildChannel (not .IsCategory)}}#{{end}}{{.Name}}`

type Config struct {
	Guilds struct {
		BridgingGuildIDs []string `yaml:"bridging_guild_ids"`
	} `yaml:"guilds"`

	// ChannelNameTemplate formats Matrix room names for Discord channels other
	// than 1:1 DMs, which intentionally use bridgev2's ghost-derived default.
	ChannelNameTemplate  string `yaml:"channel_name_template"`
	CustomEmojiReactions *bool  `yaml:"custom_emoji_reactions"`
	GuildAvatarsInRooms  *bool  `yaml:"guild_avatars_in_rooms"`

	PerMessageProfiles *bool `yaml:"per_message_profiles_on_every_message_hack"`

	ForbidDMingStrangers *bool `yaml:"forbid_dming_strangers"`

	LogWhenDroppingMessages bool `yaml:"log_when_dropping_messages"`

	// Proxy is a static proxy address (HTTP or SOCKS5) for connecting to
	// Discord. Ignored when GetProxyFrom is set.
	Proxy string `yaml:"proxy"`

	// ProxyLoginMachine and ProxyLoginRemoteAuth control whether the
	// respective login flows route through the configured proxy. See
	// example-config.yaml for the tradeoffs. Both default to true.
	ProxyLoginMachine    bool `yaml:"proxy_login_machine"`
	ProxyLoginRemoteAuth bool `yaml:"proxy_login_remoteauth"`

	// GetProxyFrom is an HTTP endpoint that returns a JSON body with a string
	// field called proxy_url, used to dynamically assign a proxy. It is
	// re-fetched on every (re)connect so each session egresses from one IP.
	GetProxyFrom string `yaml:"get_proxy_from"`

	// ProxyMedia controls whether avatar, icon, and attachment downloads also
	// go through the proxy. The gateway websocket and REST API always use it.
	ProxyMedia bool `yaml:"proxy_media"`

	ReportScrubbedAccountStanding bool `yaml:"report_scrubbed_account_standing"`

	channelNameTemplate *template.Template `yaml:"-"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	err := node.Decode((*umConfig)(c))
	if err != nil {
		return err
	}

	if c.ChannelNameTemplate == "" {
		c.ChannelNameTemplate = defaultChannelNameTemplate
	}

	c.channelNameTemplate, err = template.New("channel_name").Parse(c.ChannelNameTemplate)
	if err != nil {
		return err
	}

	return nil
}

// ChannelNameParams describes the values available to [Config.FormatChannelName].
//
// It intentionally includes both the raw Discord channel type and convenience
// booleans so templates can express v1-style naming rules without relying on
// numeric channel type constants.
type ChannelNameParams struct {
	Name           string
	ParentName     string
	GuildName      string
	Type           discordgo.ChannelType
	NSFW           bool
	IsDM           bool
	IsGroupDM      bool
	IsCategory     bool
	IsGuildChannel bool
}

// FormatChannelName renders [Config.ChannelNameTemplate] for non-guild-space
// channel portals. One-to-one DMs intentionally bypass this helper so bridgev2
// can derive the room name from the other user's ghost.
func (c *Config) FormatChannelName(params *ChannelNameParams) string {
	var buffer strings.Builder
	_ = c.channelNameTemplate.Execute(&buffer, params)
	return buffer.String()
}

func (c Config) ForbidDMingStrangersEnabled() bool {
	return c.ForbidDMingStrangers == nil || *c.ForbidDMingStrangers
}

func (c Config) CustomEmojiReactionsEnabled() bool {
	return c.CustomEmojiReactions == nil || *c.CustomEmojiReactions
}

func (c Config) GuildAvatarsInRoomsEnabled() bool {
	return c.GuildAvatarsInRooms != nil && *c.GuildAvatarsInRooms
}

func (c Config) PerMessageProfilesEnabled() bool {
	return c.PerMessageProfiles != nil && *c.PerMessageProfiles
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.List, "guilds", "bridging_guild_ids")
	helper.Copy(up.Bool, "guilds", "guild_avatars_in_rooms")
	helper.Copy(up.Bool, "forbid_dming_strangers")
	helper.Copy(up.Str, "channel_name_template")
	helper.Copy(up.Bool, "custom_emoji_reactions")
	helper.Copy(up.Bool, "per_message_profiles_on_every_message_hack")
	helper.Copy(up.Bool, "log_when_dropping_messages")
	helper.Copy(up.Str, "proxy")
	helper.Copy(up.Str, "get_proxy_from")
	helper.Copy(up.Bool, "proxy_media")
	helper.Copy(up.Bool, "proxy_login_machine")
	helper.Copy(up.Bool, "proxy_login_remoteauth")
	helper.Copy(up.Bool, "report_scrubbed_account_standing")
}

func (d *DiscordConnector) GetConfig() (example string, data any, upgrader up.Upgrader) {
	return ExampleConfig, &d.Config, up.SimpleUpgrader(upgradeConfig)
}

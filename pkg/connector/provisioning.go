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
	"errors"
	"net/http"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
)

const (
	ErrCodeNotConnected          = "FI.MAU.DISCORD.NOT_CONNECTED"
	ErrCodeAlreadyLoggedIn       = "FI.MAU.DISCORD.ALREADY_LOGGED_IN"
	ErrCodeAlreadyConnected      = "FI.MAU.DISCORD.ALREADY_CONNECTED"
	ErrCodeConnectFailed         = "FI.MAU.DISCORD.CONNECT_FAILED"
	ErrCodeDisconnectFailed      = "FI.MAU.DISCORD.DISCONNECT_FAILED"
	ErrCodeGuildBridgeFailed     = "M_UNKNOWN"
	ErrCodeGuildUnbridgeFailed   = "M_UNKNOWN"
	ErrCodeGuildNotBridged       = "FI.MAU.DISCORD.GUILD_NOT_BRIDGED"
	ErrCodeLoginPrepareFailed    = "FI.MAU.DISCORD.LOGIN_PREPARE_FAILED"
	ErrCodeLoginConnectionFailed = "FI.MAU.DISCORD.LOGIN_CONN_FAILED"
	ErrCodeLoginFailed           = "FI.MAU.DISCORD.LOGIN_FAILED"
	ErrCodePostLoginConnFailed   = "FI.MAU.DISCORD.POST_LOGIN_CONNECTION_FAILED"
)

type ProvisioningAPI struct {
	log       zerolog.Logger
	connector *DiscordConnector
	prov      bridgev2.IProvisioningAPI
}

func (d *DiscordConnector) setUpProvisioningAPIs() error {
	c, ok := d.Bridge.Matrix.(bridgev2.MatrixConnectorWithProvisioning)
	if !ok {
		return errors.New("matrix connector doesn't support provisioning; not setting up")
	}

	prov := c.GetProvisioning()
	r := prov.GetRouter()
	if r == nil {
		return errors.New("matrix connector's provisioning api didn't return a router")
	}

	log := d.Bridge.Log.With().Str("component", "provisioning").Logger()
	p := &ProvisioningAPI{
		connector: d,
		log:       log,
		prov:      prov,
	}

	// NOTE: aim to provide backwards compatibility with v1 provisioning APIs
	r.HandleFunc("GET /v1/guilds", p.makeHandler(p.guildsList))
	r.HandleFunc("POST /v1/guilds/{guildID}", p.makeHandler(p.bridgeGuild))
	r.HandleFunc("DELETE /v1/guilds/{guildID}", p.makeHandler(p.unbridgeGuild))

	return nil
}

type guildEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// TODO v1 uses `id.ContentURI` whereas we stuff the discord cdn url here
	AvatarURL string `json:"avatar_url"`

	// v1-compatible fields:
	MXID         string `json:"mxid"`
	AutoBridge   bool   `json:"auto_bridge_channels"`
	BridgingMode string `json:"bridging_mode"`

	Available bool `json:"available"`
}
type respGuildsList struct {
	Guilds []guildEntry `json:"guilds"`
}

func (p *ProvisioningAPI) makeHandler(handler func(http.ResponseWriter, *http.Request, *bridgev2.UserLogin, *DiscordClient)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := p.prov.GetUser(r)
		logins := user.GetUserLogins()

		if len(logins) < 1 {
			mautrix.RespError{
				ErrCode: ErrCodeNotConnected,
				Err:     "user has no logins",
			}.Write(w)
			return
		}

		login := logins[0]
		client := login.Client.(*DiscordClient)

		handler(w, r, login, client)
	}
}

func (p *ProvisioningAPI) guildsList(w http.ResponseWriter, r *http.Request, login *bridgev2.UserLogin, client *DiscordClient) {
	p.log.Info().Str("login_id", string(login.ID)).Msg("guilds list requested via provisioning api")

	var resp respGuildsList
	resp.Guilds = []guildEntry{}
	for _, guild := range client.Session.State.Guilds {
		resp.Guilds = append(resp.Guilds, guildEntry{
			ID:        guild.ID,
			Name:      guild.Name,
			AvatarURL: discordgo.EndpointGuildIcon(guild.ID, guild.Icon),

			BridgingMode: "everything",

			Available: !guild.Unavailable,
		})
	}

	exhttp.WriteJSONResponse(w, 200, resp)
}

func (p *ProvisioningAPI) bridgeGuild(w http.ResponseWriter, r *http.Request, login *bridgev2.UserLogin, client *DiscordClient) {
	guildID := r.PathValue("guildID")
	if guildID == "" {
		mautrix.MInvalidParam.WithMessage("no guild id").Write(w)
		return
	}

	p.log.Info().
		Str("login_id", string(login.ID)).
		Str("guild_id", guildID).
		Msg("requested to bridge guild via provisioning api")

	// TODO detect guild already bridged
	go client.bridgeGuild(context.TODO(), guildID)

	exhttp.WriteJSONResponse(w, 201, nil)
}

func (p *ProvisioningAPI) unbridgeGuild(w http.ResponseWriter, r *http.Request, login *bridgev2.UserLogin, client *DiscordClient) {
	guildID := r.PathValue("guildID")
	if guildID == "" {
		mautrix.MInvalidParam.WithMessage("no guild id").Write(w)
		return
	}

	p.log.Info().
		Str("login_id", string(login.ID)).
		Str("guild_id", guildID).
		Msg("requested to unbridge guild via provisioning api")

	ctx := context.TODO()

	portalKey := client.guildPortalKeyFromID(guildID)
	portal, err := p.connector.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		p.log.Err(err).Msg("Failed to get guild portal")
		mautrix.MUnknown.WithMessage("failed to get portal: %v", err).Write(w)
		return
	}
	if portal == nil || portal.MXID == "" {
		mautrix.RespError{
			ErrCode: ErrCodeGuildNotBridged,
			Err:     "guild is not bridged",
		}.Write(w)
		return
	}

	children, err := p.connector.Bridge.GetChildPortals(ctx, portalKey)
	if err != nil {
		p.log.Err(err).Msg("Failed to get child portals")
		mautrix.MUnknown.WithMessage("failed to get children: %v", err).Write(w)
		return
	}

	portalsToDelete := append(children, portal)
	bridgev2.DeleteManyPortals(ctx, portalsToDelete, func(portal *bridgev2.Portal, del bool, err error) {
		p.log.Err(err).
			Stringer("portal_mxid", portal.MXID).
			Bool("delete_room", del).
			Msg("Failed during portal cleanup")
	})

	p.log.Info().Int("children", len(children)).Msg("Finished unbridging")
	exhttp.WriteJSONResponse(w, 200, map[string]any{
		"success":         true,
		"deleted_portals": len(children) + 1,
	})
}

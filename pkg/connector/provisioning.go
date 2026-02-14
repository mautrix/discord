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
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-discord/pkg/discordid"
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

	// new in v2:
	Bridged   bool `json:"bridged"`
	Available bool `json:"available"`

	// legacy fields from v1:
	MXID         string `json:"mxid"`
	AutoBridge   bool   `json:"auto_bridge_channels"`
	BridgingMode string `json:"bridging_mode"`
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
	ctx := r.Context()
	p.log.Info().Str("login_id", discordid.ParseUserLoginID(login.ID)).Msg("guilds list requested via provisioning api")

	bridgedGuildIDs := client.bridgedGuildIDs()

	var resp respGuildsList
	resp.Guilds = []guildEntry{}
	for _, guild := range client.Session.State.Guilds {
		portalKey := client.guildPortalKeyFromID(guild.ID)
		portal, err := p.connector.Bridge.GetExistingPortalByKey(ctx, portalKey)
		if err != nil {
			p.log.Err(err).
				Str("guild_id", guild.ID).
				Msg("Failed to get guild portal for provisioning list")
		}

		_, beingBridged := bridgedGuildIDs[guild.ID]
		mxid := ""
		if portal != nil && portal.MXID != "" {
			mxid = portal.MXID.String()
		} else if beingBridged {
			// Beeper Desktop expects the space to exist by the time it receives
			// our HTTP response. If it doesn't, then the space won't appear
			// until the app is reloaded, and the toggle in the user interface
			// won't respond to the user's click.
			//
			// Pre-bridgev2, we synchronously bridged guilds. However, this
			// might take a while for guilds with many channels.
			//
			// To solve this, generate a deterministic room ID to use as the
			// MXID so that it recognizes the guild as bridged, even if the
			// portals haven't been created just yet. This lets us
			// asynchronously bridge guilds while keeping the UI responsive.
			mxid = p.connector.Bridge.Matrix.GenerateDeterministicRoomID(portalKey).String()
		}

		resp.Guilds = append(resp.Guilds, guildEntry{
			// For now, have the ID exactly correspond to the portal ID. This
			// practically means that the ID will begin with an asterisk (the
			// guild portal ID sigil).
			//
			// Otherwise, Beeper Desktop will show a duplicate space for every
			// guild, as it recognizes the guild returned from this HTTP
			// endpoint and the actual space itself as separate "entities".
			// (Despite this, they point to identical rooms.)
			ID:        string(discordid.MakeGuildPortalID(guild.ID)),
			Name:      guild.Name,
			AvatarURL: discordgo.EndpointGuildIcon(guild.ID, guild.Icon),
			Bridged:   beingBridged,
			Available: !guild.Unavailable,

			// v1 (legacy) backwards compat:
			MXID:         mxid,
			AutoBridge:   beingBridged,
			BridgingMode: "everything",
		})
	}

	exhttp.WriteJSONResponse(w, 200, resp)
}

// normalizeGuildID removes the guild portal sigil from a guild ID if it's
// there.
//
// This helps facilitate code that would like to accept portal keys
// corresponding to guilds as well as plain Discord guild IDs.
func normalizeGuildID(guildID string) string {
	return strings.TrimPrefix(guildID, discordid.GuildPortalKeySigil)
}

func (p *ProvisioningAPI) bridgeGuild(w http.ResponseWriter, r *http.Request, login *bridgev2.UserLogin, client *DiscordClient) {
	guildID := normalizeGuildID(r.PathValue("guildID"))
	if guildID == "" {
		mautrix.MInvalidParam.WithMessage("no guild id").Write(w)
		return
	}

	p.log.Info().
		Str("login_id", discordid.ParseUserLoginID(login.ID)).
		Str("guild_id", guildID).
		Msg("requested to bridge guild via provisioning api")

	meta := login.Metadata.(*discordid.UserLoginMetadata)

	if meta.BridgedGuildIDs == nil {
		meta.BridgedGuildIDs = map[string]bool{}
	}
	_, alreadyBridged := meta.BridgedGuildIDs[guildID]
	meta.BridgedGuildIDs[guildID] = true

	if err := login.Save(r.Context()); err != nil {
		p.log.Err(err).Msg("Failed to save login after guild bridge request")
		mautrix.MUnknown.WithMessage("failed to save login: %v", err).Write(w)
		return
	}

	go client.syncGuild(p.connector.Bridge.BackgroundCtx, guildID)

	responseStatus := 201
	if alreadyBridged {
		responseStatus = 200
	}
	exhttp.WriteJSONResponse(w, responseStatus, nil)
}

func (p *ProvisioningAPI) unbridgeGuild(w http.ResponseWriter, r *http.Request, login *bridgev2.UserLogin, client *DiscordClient) {
	guildID := normalizeGuildID(r.PathValue("guildID"))
	if guildID == "" {
		mautrix.MInvalidParam.WithMessage("no guild id").Write(w)
		return
	}

	p.log.Info().
		Str("login_id", discordid.ParseUserLoginID(login.ID)).
		Str("guild_id", guildID).
		Msg("requested to unbridge guild via provisioning api")

	meta := login.Metadata.(*discordid.UserLoginMetadata)
	if meta.BridgedGuildIDs != nil {
		delete(meta.BridgedGuildIDs, guildID)
	}
	if err := login.Save(r.Context()); err != nil {
		p.log.Err(err).Msg("Failed to save login after guild unbridge request")
		mautrix.MUnknown.WithMessage("failed to save login: %v", err).Write(w)
		return
	}

	ctx := login.Log.With().
		Str("component", "provisioning").
		Str("action", "unbridge guild").
		Str("guild_id", guildID).
		Logger().
		WithContext(context.Background())

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

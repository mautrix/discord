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
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/util/exmaps"

	"go.mau.fi/mautrix-discord/pkg/discordauth"
	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type DiscordClient struct {
	connector  *DiscordConnector
	UserLogin  *bridgev2.UserLogin
	Session    *discordgo.Session
	httpClient *http.Client

	stopConnecting  atomic.Pointer[context.CancelFunc]
	hasBegunSyncing bool

	// seenReady is used to discern the initial READY payload from ones
	// received during reconnections (where resumption is not possible).
	seenReady atomic.Bool

	markedOpened     map[string]time.Time
	markedOpenedLock sync.Mutex

	// A map of guild ID (or "" for the settings concerning private channels)
	// to its corresponding UserGuildSettings.
	guildSettings     map[string]*discordgo.UserGuildSettings
	guildSettingsLock sync.RWMutex

	// A map of resource (e.g. channel) ID to its corresponding read state.
	//
	// Since there can be thousands of read state entries, the map is to help
	// keep lookups by channel ID speedy by avoiding constant linear searching.
	readStates     map[string]*discordgo.ReadState
	readStatesLock sync.RWMutex

	relationshipLock sync.RWMutex
	relationships    map[string]*discordgo.Relationship

	userCache *UserCache

	lastSendAttemptMutex sync.Mutex
	lastSendAttempt      *SendAttempt
}

func (d *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*discordid.UserLoginMetadata)

	var session *discordgo.Session
	if meta.Token == "" {
		login.Log.Warn().Msg("Login has no token, not setting up a session")
		// Session on the UserLogin will be nil.
	} else {
		var err error
		session, err = NewDiscordSession(ctx, d.Bridge.GetHTTPClientSettings(), meta.Token)
		if err != nil {
			return err
		}
	}

	cl := DiscordClient{
		connector: d,
		UserLogin: login,
		Session:   session,
		// This HTTP client is quickly overridden by a proxied version (when
		// one is configured).
		httpClient:    d.Bridge.GetHTTPClientSettings().Compile(),
		userCache:     NewUserCache(session),
		guildSettings: make(map[string]*discordgo.UserGuildSettings),
		readStates:    make(map[string]*discordgo.ReadState),
		relationships: make(map[string]*discordgo.Relationship),
	}
	login.Client = &cl

	if session != nil {
		session.RESTResponseHook = cl.tapDiscordRESTResponse
		session.BeforeReconnect = func(*discordgo.Session) {
			c := login.Client.(*DiscordClient)
			if c.connector.proxyConfigured() && !c.updateProxy(c.connector.Bridge.BackgroundCtx, "reconnect") {
				// Failed to update the proxy. Continue reconnecting via the
				// last good proxy, but report the failure.
				c.UserLogin.BridgeState.Send(status.BridgeState{
					StateEvent: status.StateTransientDisconnect,
					Error:      DCProxyResolveFail,
				})
			}
		}
	}

	return nil
}

var _ bridgev2.NetworkAPI = (*DiscordClient)(nil)

func (d *DiscordClient) userLoginMetadata() *discordid.UserLoginMetadata {
	return d.UserLogin.Metadata.(*discordid.UserLoginMetadata)
}

func (d *DiscordClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	lacksToken := !d.HasToken()
	lacksSession := d.Session == nil
	if lacksToken || lacksSession {
		// (d.Session can be nil if we lacked credentials on startup.)
		log.Warn().Bool("lacking_token", lacksToken).
			Bool("lacking_session", lacksSession).
			Msg("Refusing to connect")

		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      DCNotLoggedIn,
			UserAction: status.UserActionRelogin,
		})
		return
	}

	meta := d.userLoginMetadata()
	if meta.HeartbeatSession.IsExpired() {
		log.Info().Msg("Heartbeat session expired, creating a new one")
		meta.HeartbeatSession = discordgo.NewHeartbeatSession()
	}
	meta.HeartbeatSession.BumpLastUsed()
	d.Session.HeartbeatSession = meta.HeartbeatSession

	d.markedOpened = make(map[string]time.Time)

	d.connectRetrying(ctx, 0)
}

const maxGatewayConnectRetries = 5

// tokenInvalidated responds to Discord invalidating our token.
func (d *DiscordClient) tokenInvalidated(ctx context.Context, circumstance string) {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Invalidating user login")

	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      DCWebsocketDisconnect4004,
		UserAction: status.UserActionRelogin,
	})

	props := d.baseAnalyticsProps(ctx)
	props["circumstance"] = circumstance
	d.UserLogin.TrackAnalytics("Discord auth invalidation", props)

	// Empty out the token.
	log.Debug().Msg("Emptying token")
	meta := d.UserLogin.Metadata.(*discordid.UserLoginMetadata)
	meta.Token = ""
	if err := d.UserLogin.Save(ctx); err != nil {
		log.Err(err).Msg("Failed to save user login in order to invalidate session")
	}
}

func (d *DiscordClient) connectRetrying(ctx context.Context, retryCount int) {
	retryCtx, cancel := context.WithCancel(ctx)
	oldStop := d.stopConnecting.Swap(&cancel)
	if oldStop != nil {
		(*oldStop)()
	}

	log := zerolog.Ctx(ctx).With().Int("retry_count", retryCount).Logger()

	log.Debug().Msg("Connecting to Discord")
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnecting,
	})

	if d.connector.proxyConfigured() && !d.updateProxy(ctx, "connect") {
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      DCProxyResolveFail,
		})
		return
	}

	err := d.connect(ctx)
	if err != nil {
		log.Err(err).Msg("Couldn't connect to Discord")

		if websocket.CloseStatus(err) == 4004 {
			// Effectively the same as *discordgo.InvalidAuth, but at connect
			// time. (discordgo only dispatches the synthetic InvalidAuth event
			// once you've already connected successfully.)
			//
			// Don't retry.
			d.tokenInvalidated(ctx, "when connecting")
		} else if retryCount <= maxGatewayConnectRetries {
			d.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateTransientDisconnect,
				Error:      DCUnknownWebsocketError,
				Message:    err.Error(),
			})

			sleepDuration := time.Second * time.Duration(2<<retryCount)
			log.Debug().Dur("retry_sleeping_seconds", sleepDuration).
				Msg("Sleeping and retrying gateway connection")

			select {
			case <-time.After(sleepDuration):
			case <-retryCtx.Done():
				log.Debug().Msg("Was told to stop connecting")
				return
			}
			d.connectRetrying(ctx, retryCount+1)
		} else {
			d.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateUnknownError,
				Error:      DCUnknownWebsocketError,
				Message:    err.Error(),
			})

			log.Error().Msg("Exhausted connect retries")
		}
	}
}

func (d *DiscordClient) handleDiscordEventSync(event any) {
	// Dispatch event handlers that maintain important state synchronously, or
	// else we might end up inadvertently relying on goroutine scheduling.
	d.handleDiscordStateEvent(event)

	go d.handleDiscordEvent(event)
}

func (d *DiscordClient) connect(ctx context.Context) error {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Opening session")

	d.Session.EventHandler = d.handleDiscordEventSync

	// Open() blocks until we have processed READY or we have resumed
	// successfully. This will also internally dispatch event handlers (so our
	// READY/etc. handlers will have completed once this method finishes).
	err := d.Session.Open()
	if err != nil {
		log.Err(err).Msg("Failed to connect to Discord")
		return err
	}

	// Ensure that we actually have a user.
	if !d.IsLoggedIn() {
		return fmt.Errorf("unknown identity even after connecting to Discord")
	}
	user := d.Session.State.User
	log.Info().Str("user_id", user.ID).Str("user_username", user.Username).Msg("Connected to Discord")

	d.BeginSyncing(ctx)

	return nil
}

func (d *DiscordClient) applyReadyPayload(
	ctx context.Context,
	ready *discordgo.Ready,
) {
	log := zerolog.Ctx(ctx)

	d.rebuildRelationships()

	log.Debug().Int("n_users", len(ready.Users)).
		Msg("Inserting users from READY into cache")
	// NOTE: This can potentially block for a while if the user cache is
	// concurrently resolving a user and internally performing an HTTP request
	// (the lock is held across it).
	d.userCache.UpdateWithReady(ready)

	readState := ready.ReadState
	if readState != nil {
		log.Debug().Int("n_read_states", len(readState.Entries)).
			Msg("Applying read states from READY")

		d.readStatesLock.Lock()
		if !readState.Partial {
			clear(d.readStates)
		}

		for _, state := range readState.Entries {
			d.readStates[state.ID] = state
		}
		d.readStatesLock.Unlock()
	}

	settings := ready.UserGuildSettings
	if settings != nil {
		log.Debug().Int("n_guild_settings", len(settings.Entries)).
			Msg("Applying guild settings from READY")

		d.bulkApplyGuildSettings(settings)
	}
}

func (d *DiscordClient) bulkApplyGuildSettings(sl *discordgo.UserGuildSettingsList) {
	d.guildSettingsLock.Lock()
	defer d.guildSettingsLock.Unlock()

	d.UserLogin.Log.Warn().
		Int("n_settings_entries", len(sl.Entries)).
		Int("settings_version", sl.Version).
		Bool("settings_is_partial", sl.Partial).
		Msg("Bulk applying partial guild settings")

	if !sl.Partial {
		clear(d.guildSettings)
	}

	for _, setting := range sl.Entries {
		d.guildSettings[setting.GuildID] = setting
	}
}

func (d *DiscordClient) applySingleGuildSettings(s *discordgo.UserGuildSettings) {
	d.guildSettingsLock.Lock()
	defer d.guildSettingsLock.Unlock()

	d.guildSettings[s.GuildID] = s
}

func (d *DiscordClient) Disconnect() {
	if stopConnecting := d.stopConnecting.Swap(nil); stopConnecting != nil {
		(*stopConnecting)()
	}
	d.UserLogin.Log.Info().Msg("Disconnecting session")
	if d.Session != nil {
		d.Session.Close()
	}
}

func (d *DiscordClient) HasToken() bool {
	meta := d.userLoginMetadata()
	return meta != nil && meta.Token != ""
}

func (d *DiscordClient) IsLoggedIn() bool {
	if !d.HasToken() {
		// If the token was emptied, immediately treat that as if we were
		// logged out, even if we still hold a connection to Discord. This is
		// less risky than nilling out Session entirely.
		return false
	}

	return d.Session != nil && d.Session.State != nil && d.Session.State.User != nil && d.Session.State.User.ID != ""
}

func (d *DiscordClient) LogoutRemote(ctx context.Context) {
	// FIXME(skip): Implement.
	d.Disconnect()
}

// BeginSyncing kicks off background sync of the remote profile, all private
// channels, and bridged guilds. This occurs asynchronously. This should only
// be called once the gateway connection is READY or RESUMED.
func (d *DiscordClient) BeginSyncing(ctx context.Context) {
	if d.hasBegunSyncing {
		d.connector.Bridge.Log.Warn().Msg("Not beginning sync more than once")
		return
	}
	d.hasBegunSyncing = true

	d.syncRemoteProfile(ctx)
	d.beginResyncingChatsAndSpaces(ctx)
}

// beginResyncingChatsAndSpaces reconciles guild spaces against the latest gateway state
// and pokes background resync and backfill for all private channels and
// bridged guilds.
//
// This should be called on every (re)connection where we may have missed
// gateway events.
func (d *DiscordClient) beginResyncingChatsAndSpaces(ctx context.Context) {
	// Build the set of IDs for all guilds the user is definitively a member
	// of. This includes guilds that are currently unavailable. Also note that
	// we pointedly don't do this in the goroutines we're about to spawn, in
	// order to avoid races.
	//
	// The READY payload is the only authoritative source on which guilds the
	// user is a member of, as guilds are evicted from discordgo's in-memory
	// state when they are "deleted", even if only due to unavailability (i.e.
	// a Discord service outage).
	//
	// In other words, a guild's absence from discordgo State is not enough to
	// determine if the user is a member of that guild or not.
	guildIDs := make(exmaps.Set[string])
	d.Session.State.RLock()
	for _, guild := range d.Session.State.Guilds {
		guildIDs.Add(guild.ID)
	}
	d.Session.State.RUnlock()

	go d.reconcileGuildSpaces(ctx, guildIDs)
	go d.syncPrivateChannels(ctx)
	go d.syncGuilds(ctx)
}

func (d *DiscordClient) existingPortals(ctx context.Context) iter.Seq[*bridgev2.Portal] {
	log := zerolog.Ctx(ctx)

	ups, err := d.connector.Bridge.DB.UserPortal.GetAllForLogin(ctx, d.UserLogin.UserLogin)
	if err != nil {
		log.Err(err).Msg("Failed to fetch all user portals, returning empty iterator")
		// Return a dummy iterator that is empty.
		return func(yield func(*bridgev2.Portal) bool) {}
	}

	return func(yield func(*bridgev2.Portal) bool) {
		seen := make(exmaps.Set[networkid.PortalKey])

		for _, up := range ups {
			portal, err := d.connector.Bridge.GetExistingPortalByKey(ctx, up.Portal)
			if err != nil {
				log.Err(err).Msg("Failed to fetch portal corresponding to user portal, proceeding")
				continue
			}
			if portal == nil {
				// ?
				continue
			}

			// Depending on how split portals are configured,
			// GetExistingPortalByKey can target the same portal from distinct
			// user portal rows.
			if seen.Has(portal.PortalKey) {
				continue
			}
			seen.Add(portal.PortalKey)

			if !yield(portal) {
				return
			}
		}
	}
}

func (d *DiscordClient) syncPrivateChannels(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	resyncedExistingPrivateChannels := make(exmaps.Set[string])

	// Queue resyncs for all private channel portals that are already bridged,
	// while queueing deletion for those that are not found in discordgo state.
	for portal := range d.existingPortals(ctx) {
		if !portalIsPrivate(portal) {
			continue
		}
		channelID := discordid.ParseChannelPortalID(portal.ID)

		// We could check State.PrivateChannels directly, but that would be
		// a linear search.
		channel := d.channelWithID(ctx, channelID)
		if channel == nil {
			log.Info().
				Str("deleting_channel_id", channelID).
				Str("deleting_portal_room_type", string(portal.RoomType)).
				Stringer("deleting_portal_key", portal.PortalKey).
				Msg("Deleting portal corresponding to a private channel that isn't in state")
			d.queueChatDelete(portal.PortalKey, "")
			continue
		}

		if portal.MXID != "" {
			log.Debug().
				Str("channel_id", channelID).
				Stringer("portal_key", portal.PortalKey).
				Msg("Resyncing existing private channel portal")
			resyncedExistingPrivateChannels.Add(channelID)
			d.queueExistingChannelResync(ctx, channel)
		}
	}

	d.Session.State.RLock()
	dms := slices.Clone(d.Session.State.PrivateChannels)
	d.Session.State.RUnlock()

	// Only queue portal-creating resyncs for the top n private channels with
	// recent activity (that haven't already been synced above).
	slices.SortFunc(dms, func(a, b *discordgo.Channel) int {
		ats, _ := discordgo.SnowflakeTimestamp(a.LastMessageID)
		bts, _ := discordgo.SnowflakeTimestamp(b.LastMessageID)
		return bts.Compare(ats)
	})
	// TODO(skip): This is startup_private_channel_create_limit. Support this
	// in the config.
	maxDms := min(10, len(dms))
	recentDmsSynced := 0
	for _, dm := range dms[:maxDms] {
		if resyncedExistingPrivateChannels.Has(dm.ID) {
			continue
		}
		log.Debug().Str("channel_id", dm.ID).Msg("Syncing private channel with recent activity")
		d.queueChannelResync(ctx, dm)
		recentDmsSynced++
	}

	log.Info().
		Int("existing_private_portals_synced", len(resyncedExistingPrivateChannels)).
		Int("recent_dms_synced", recentDmsSynced).
		Int("dms_total", len(dms)).
		Msg("Synced private channels")
}

func (d *DiscordClient) canSeeGuildChannel(ctx context.Context, ch *discordgo.Channel) bool {
	log := zerolog.Ctx(ctx).With().
		Str("channel_id", ch.ID).
		Int("channel_type", int(ch.Type)).
		Str("action", "determine guild channel visbility").Logger()

	sess := d.Session
	myDiscordUserID := d.Session.State.User.ID

	// To calculate guild channel visibility we need to know our effective permission
	// bitmask, which can only be truly determined when we know which roles we have
	// in the guild.
	//
	// To this end, make sure we have detailed information about ourselves in the
	// cache ("state").

	_, err := sess.State.Member(ch.GuildID, myDiscordUserID)
	if errors.Is(err, discordgo.ErrStateNotFound) {
		log.Debug().Msg("Fetching own membership in guild to check roles")

		member, err := sess.GuildMember(ch.GuildID, myDiscordUserID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get own membership in guild from server")
		} else {
			err = sess.State.MemberAdd(member)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to add own membership in guild to cache")
			}
		}
	} else if err != nil {
		log.Warn().Err(err).Msg("Failed to get own membership in guild from cache")
	}

	err = sess.State.ChannelAdd(ch)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to add channel to cache")
	}

	perms, err := sess.State.UserChannelPermissions(myDiscordUserID, ch.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get permissions in channel to determine if it's bridgeable")
		return true
	}

	canView := perms&discordgo.PermissionViewChannel > 0
	log.Trace().
		Int64("permissions", perms).
		Bool("channel_visible", canView).
		Msg("Computed visibility of guild channel")
	return canView
}

func (d *DiscordClient) makeAvatarForGuild(guild *discordgo.Guild) *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID: discordid.MakeAvatarID(guild.Icon),
		Get: func(ctx context.Context) ([]byte, error) {
			url := discordgo.EndpointGuildIcon(guild.ID, guild.Icon)
			return httpGet(ctx, d.httpClient, url, "guild icon")
		},
		Remove: guild.Icon == "",
	}
}

// bridgedGuildIDs returns a set of guild IDs that should be bridged. Note that
// presence in the returned set does not imply anything about the corresponding
// portals and rooms.
func (d *DiscordClient) bridgedGuildIDs() map[string]struct{} {
	meta := d.UserLogin.Metadata.(*discordid.UserLoginMetadata)
	bridgingGuildIDs := map[string]struct{}{}

	// guilds that were bridged via the provisioning api
	for guildID, bridged := range meta.BridgedGuildIDs {
		if bridged {
			bridgingGuildIDs[guildID] = struct{}{}
		}
	}

	// guilds that were declared in the configuration file
	for _, guildID := range d.connector.Config.Guilds.BridgingGuildIDs {
		bridgingGuildIDs[guildID] = struct{}{}
	}

	return bridgingGuildIDs
}

func (d *DiscordClient) syncGuilds(ctx context.Context) {
	guildIDs := slices.Sorted(maps.Keys(d.bridgedGuildIDs()))

	for _, guildID := range guildIDs {
		log := zerolog.Ctx(ctx).With().
			Str("guild_id", guildID).
			Str("action", "sync guild").
			Logger()

		err := d.syncGuild(log.WithContext(ctx), guildID)
		if err != nil {
			log.Err(err).Msg("Couldn't bridge guild during sync")
		}
	}
}

// ensurePortal synchronously guarantees the existence of a portal's Matrix
// room with up-to-date chat info.
//
// If info is nil, then the chat info is fetched from the NetworkAPI.
func (d *DiscordClient) ensurePortal(ctx context.Context, key networkid.PortalKey, info *bridgev2.ChatInfo) error {
	portal, err := d.connector.Bridge.GetPortalByKey(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get portal: %w", err)
	}

	if info == nil {
		info, err = d.GetChatInfo(ctx, portal)
		if err != nil {
			return fmt.Errorf("failed to get chat info: %w", err)
		}
	}

	if portal.MXID == "" {
		// CreateMatrixRoom will indirectly lead to UpdateInfo being called.
		if err := portal.CreateMatrixRoom(ctx, d.UserLogin, info); err != nil {
			return fmt.Errorf("failed to create matrix room: %w", err)
		}
	} else {
		portal.UpdateInfo(ctx, info, d.UserLogin, nil, time.Time{})
	}

	return nil
}

// queueGuildDeletion should be called to evict a guild from the bridge, e.g.
// whenever it is determined that the user has left a Discord guild that is
// currently bridged.
//
// The following occurs:
//
//   - An attempt is made to delete all of the roles associated with the given
//     guild. This will proceed even upon error.
//   - A Matrix event is queued to delete the guild space and all of its contained
//     rooms.
func (d *DiscordClient) queueGuildDeletion(
	ctx context.Context,
	guildID string,
) {
	log := zerolog.Ctx(ctx).With().
		Str("guild_id", guildID).
		Str("action", "queue guild deletion").
		Logger()

	// TODO: This is deleting roles globally. Other logins might still be in
	// the guild.
	if err := d.connector.DB.Role.DeleteByGuildID(ctx, guildID); err != nil {
		// Best effort.
		log.Err(err).Msg("Failed to delete guild roles from database, proceeding to delete guild space anyways")
	}

	log.Info().Msg("Queueing event to recursively delete the guild space")
	d.connector.Bridge.QueueRemoteEvent(d.UserLogin, &simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: d.guildPortalKey(guildID),
		},
		OnlyForMe: true,
		Children:  true,
	})
}

// reconcileGuildSpaces examines all existing guild spaces and deletes those
// that do not appear in the provided set of guild IDs.
func (d *DiscordClient) reconcileGuildSpaces(
	ctx context.Context,
	guildIDs exmaps.Set[string],
) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "reconcile guilds").
		Logger()
	ctx = log.WithContext(ctx)

	for portal := range d.existingPortals(ctx) {
		guildID := discordid.ParseGuildPortalID(portal.ID)
		if guildID == "" {
			// Portal isn't a guild space.
			continue
		}
		if guildIDs.Has(guildID) {
			// Still a member of the guild.
			continue
		}
		log := log.With().
			Str("guild_id", guildID).
			Logger()
		ctx := log.WithContext(ctx)

		log.Info().Msg("Guild no longer appears in READY payload (user has left), queueing deletion")
		d.queueGuildDeletion(ctx, guildID)
	}
}

// shouldBridgeChannel reports whether a channel should be bridged. This
// considers information such as the type of the channel, as well as the user's
// effective permissions within the guild.
func (d *DiscordClient) shouldBridgeChannel(
	ctx context.Context,
	ch *discordgo.Channel,
) bool {
	if ch == nil {
		return false
	}

	// Only ever bridge guild text channels.
	// TODO(skip): Consider bridging news channels (sparkling text channels)?
	// TODO(skip): Consider bridging voice channels (make sure to check for the
	// right permission bits)?
	if ch.Type != discordgo.ChannelTypeGuildText {
		return false
	}

	if !d.canSeeGuildChannel(ctx, ch) {
		return false
	}

	return true
}

func (d *DiscordClient) syncGuild(ctx context.Context, guildID string) error {
	log := zerolog.Ctx(ctx).With().
		Str("guild_id", guildID).
		Str("action", "bridge guild").
		Logger()
	ctx = log.WithContext(ctx)

	guild, err := d.Session.State.Guild(guildID)
	if errors.Is(err, discordgo.ErrStateNotFound) || guild == nil {
		// This isn't problematic per se, because we can get here if a guild is
		// unavailable due to an outage; when that happens, the guild is
		// removed from the state entirely (via GUILD_DELETE).
		log.Warn().Err(err).
			Msg("Cannot sync guild that is not present in state")
		return errors.New("couldn't find guild in state")
	}

	if err = d.syncGuildRoles(ctx, guildID, guild.Roles); err != nil {
		return fmt.Errorf("failed to sync guild roles during guild sync: %w", err)
	}

	// Synchronously guarantee the proper creation of the guild space portal so
	// child rooms are born with the correct `m.bridge` state.
	portalKey := d.guildPortalKey(guild.ID)
	if err := d.ensurePortal(ctx, portalKey, nil); err != nil {
		return fmt.Errorf("failed to ensure guild space portal: %w", err)
	}

	visibleCategoryIDs := make(exmaps.Set[string])
	visibleChannels := make([]*discordgo.Channel, 0, len(guild.Channels))
	for _, guildCh := range guild.Channels {
		if !d.shouldBridgeChannel(ctx, guildCh) {
			continue
		}
		visibleChannels = append(visibleChannels, guildCh)
		if guildCh.ParentID != "" {
			visibleCategoryIDs.Add(guildCh.ParentID)
		}
	}
	// Synchronously guarantee the proper creation of category space portals
	// for the same reason that we do so for guild space portals.
	//
	// Note that we only care about syncing categories that contain at least
	// one channel we can actually see. This matches the behavior of Discord's
	// first-party clients. The permission bits on the category channel
	// _itself_ are irrelevant.
	for categoryID := range visibleCategoryIDs.Iter() {
		category := d.channelWithID(ctx, categoryID)
		if category == nil {
			log.Error().Str("channel_id", categoryID).Msg("Failed to find category channel somehow, proceeding")
			continue
		}

		err := d.ensurePortal(ctx, d.portalKeyForChannel(category), nil)
		if err != nil {
			log.Err(err).Msg("Failed to ensure category space, proceeding")
			// FIXME The children of this category channel will still be synced
			// but with bogus `m.bridge` state.
		}
	}
	// Now that all possible parent spaces exist, we can fan out the syncing of
	// all guild channels we can see.
	for _, visibleCh := range visibleChannels {
		d.queueChannelResync(ctx, visibleCh)
	}

	for _, thread := range guild.Threads {
		err = d.upsertThreadInfoFromChannel(ctx, thread)
		if err != nil {
			log.Err(err).Str("thread_id", thread.ID).Msg("Failed to cache thread info during guild sync")
		}
	}

	d.subscribeGuild(ctx, guildID)

	return nil
}

func (d *DiscordClient) subscribeGuild(ctx context.Context, guildID string) {
	log := zerolog.Ctx(ctx)

	log.Debug().Msg("Subscribing to guild")
	err := d.Session.SubscribeGuild(discordgo.GuildSubscribeData{
		GuildID:    guildID,
		Typing:     true,
		Activities: true,
		Threads:    true,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to subscribe to guild, proceeding")
	}
}

func httpGet(ctx context.Context, httpClient *http.Client, url, thing string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s: %w", thing, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 {
		return nil, fmt.Errorf("failed to download %s: got HTTP %d", thing, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s data: %w", thing, err)
	}
	return data, nil
}

func (d *DiscordClient) makeEventSenderWithID(userID string) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    userID == d.Session.State.User.ID,
		SenderLogin: discordid.MakeUserLoginID(userID),
		Sender:      discordid.MakeUserID(userID),
	}
}

func (d *DiscordClient) selfEventSender() bridgev2.EventSender {
	return d.makeEventSenderWithID(d.Session.State.User.ID)
}

func (d *DiscordClient) makeEventSender(user *discordgo.User) bridgev2.EventSender {
	if user == nil {
		panic("DiscordClient makeEventSender was passed a nil user")
	}

	return d.makeEventSenderWithID(user.ID)
}

func (d *DiscordClient) queueChannelResync(_ context.Context, ch *discordgo.Channel) {
	d.connector.Bridge.QueueRemoteEvent(d.UserLogin, &DiscordChatResync{
		Client:       d,
		channel:      ch,
		createPortal: true,
	})
}

func (d *DiscordClient) queueExistingChannelResync(_ context.Context, ch *discordgo.Channel) {
	d.connector.Bridge.QueueRemoteEvent(d.UserLogin, &DiscordChatResync{
		Client:       d,
		channel:      ch,
		createPortal: false,
	})
}

func (d *DiscordClient) readStateForID(resourceID string) *discordgo.ReadState {
	d.readStatesLock.RLock()
	defer d.readStatesLock.RUnlock()

	return d.readStates[resourceID]
}

func (d *DiscordClient) computeMutedUntil(muted bool, cfg *discordgo.MuteConfig) time.Time {
	if !muted {
		return bridgev2.Unmuted
	}

	// If Muted is true but we don't have a MuteConfig, then the mute is
	// indefinite.
	if cfg == nil {
		return event.MutedForever
	}

	// Check for the explicit "forever" time window.
	if cfg.SelectedTimeWindow != nil && *cfg.SelectedTimeWindow == -1 {
		return event.MutedForever
	}

	endTime := cfg.EndTime
	if endTime == nil {
		d.UserLogin.Log.Warn().
			Bool("muted", muted).
			Any("mute_config", cfg).
			Msg("Encountered bogus mute state, falling back to indefinite mute")
		return event.MutedForever
	}
	return *endTime
}

// channelMutedUntil computes an appropriate UserLocalPortalInfo.MutedUntil time
// for a given channel.
//
// This method works with private channels if an empty string is passed as the
// guild ID.
func (d *DiscordClient) channelMutedUntil(guildID string, channelID string) time.Time {
	settings := d.guildSettingsForGuildID(guildID)
	if settings == nil {
		return bridgev2.Unmuted
	}

	// TODO: Might be worth speeding this up via map.
	for _, override := range settings.ChannelOverrides {
		if override.ChannelID == channelID {
			return d.computeMutedUntil(override.Muted, override.MuteConfig)
		}
	}

	return d.computeMutedUntil(settings.Muted, settings.MuteConfig)
}

func (d *DiscordClient) guildSettingsForGuildID(guildID string) *discordgo.UserGuildSettings {
	d.guildSettingsLock.RLock()
	defer d.guildSettingsLock.RUnlock()

	return d.guildSettings[guildID]
}

func (d *DiscordClient) channelWithID(ctx context.Context, channelID string) *discordgo.Channel {
	if !d.IsLoggedIn() {
		return nil
	}

	ch, err := d.Session.State.Channel(channelID)
	if err != nil {
		if errors.Is(err, discordgo.ErrStateNotFound) {
			return nil
		}

		// Some other weird error happened. This is currently impossible but it's
		// best to not rely on implementation details.
		zerolog.Ctx(ctx).Err(err).
			Str("channel_id", channelID).
			Msg("Failed to look up channel")
		return nil
	}

	return ch
}

func (d *DiscordClient) syncRemoteProfile(ctx context.Context) bool {
	if !d.IsLoggedIn() {
		return false
	}

	log := zerolog.Ctx(ctx).With().
		Str("action", "sync remote discord profile").
		Logger()
	ctx = log.WithContext(ctx)

	me := d.Session.State.User
	if me == nil {
		return false
	}

	log.Debug().Msg("Updating remote profile if needed")
	changed := false
	remoteName := makeRemoteName(me)

	// Try to update our own ghost, which should upload the avatar if
	// everything goes well.
	ghost, err := d.connector.Bridge.GetGhostByID(ctx, discordid.MakeUserID(me.ID))
	if err != nil {
		log.Err(err).Msg("Failed to get own ghost, remote profile will lack an avatar")
	} else if info, err := d.GetUserInfo(ctx, ghost); err != nil {
		// Shouldn't happen as the user cache shouldn't even reach out to the
		// network; our own user should be there by now.
		log.Err(err).Msg("Failed to get own user info")
	} else {
		log.Debug().Msg("Updating own ghost with user info")
		ghost.UpdateInfo(ctx, info)
	}

	profile := makeRemoteProfile(me, ghost)
	if d.UserLogin.RemoteName != remoteName {
		d.UserLogin.RemoteName = remoteName
		changed = true
	}
	if d.UserLogin.RemoteProfile != profile {
		d.UserLogin.RemoteProfile = profile
		changed = true
	}

	if changed {
		if err := d.UserLogin.Save(ctx); err != nil {
			log.Err(err).Msg("Failed to save UserLogin while updating remote profile")
		}
	}
	return changed
	// NOTE: For clients to immediately get the new remote profile, you need to
	// send a bridge state.
}

func (d *DiscordClient) resyncGhostsFromReady(ctx context.Context, ready *discordgo.Ready) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "resync ghosts from ready").
		Logger()
	ctx = log.WithContext(ctx)

	scanned := 0
	resynced := 0
	for _, user := range ready.Users {
		if ctx.Err() != nil {
			return
		}
		scanned++

		// TODO: For now, do not actively materialize ghosts by calling e.g.
		// GetGhostByID. Before we consider switching to that method, verify
		// the breadth of the users returned in READY by inspecting a payload.
		ghost, err := d.connector.Bridge.GetExistingGhostByID(ctx, discordid.MakeUserID(user.ID))
		if err != nil {
			log.Err(err).Str("user_id", user.ID).
				Msg("Failed to look up existing ghost while resyncing from READY")
			continue
		}
		if ghost == nil {
			// We've never bridged this user, so don't materialize a ghost.
			continue
		}

		ghost.UpdateInfo(ctx, d.getUserInfo(ctx, user))
		resynced++
	}

	log.Debug().
		Int("n_ghosts_scanned", scanned).
		Int("n_ghosts_resynced", resynced).
		Msg("Finished resyncing ghosts from READY")
}

func (d *DiscordClient) wrapReceived40002(ctx context.Context, err error) error {
	log := zerolog.Ctx(ctx)
	log.Err(err).Msg("Received 40002 from Discord")

	props := d.baseAnalyticsProps(ctx)
	props["errorMessage"] = err.Error()
	d.UserLogin.TrackAnalytics("Discord account verification required", props)

	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		UserAction: status.UserActionOpenNative,
		Error:      DCHTTP40002,
	})

	return bridgev2.WrapErrorInStatus(err).
		// Tell clients to not retry.
		WithStatus(event.MessageStatusFail).
		WithIsCertain(true).
		WithMessage(accountVerificationRequiredMessage).
		WithSendNotice(true)
}

func (d *DiscordClient) tryWrappingError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	var restErr *discordgo.RESTError

	if errors.As(err, &restErr) && restErr.Message != nil {
		if restErr.Message.Code == discordgo.ErrCodeActionRequiredVerifiedAccount {
			return d.wrapReceived40002(ctx, err)
		}
	}

	return err
}

var snowflakeish = regexp.MustCompile(`\d{17,}`)

func redactDiscordRESTPath(path string) string {
	return snowflakeish.ReplaceAllLiteralString(path, "...")
}

func dmChannelRecipientID(ch *discordgo.Channel) *string {
	if ch == nil {
		return nil
	}
	if ch.Type != discordgo.ChannelTypeDM {
		return nil
	}
	if len(ch.Recipients) != 1 {
		return nil
	}

	return &ch.Recipients[0].ID
}

func (d *DiscordClient) baseAnalyticsProps(ctx context.Context) map[string]any {
	props := make(map[string]any)
	if ctx == nil {
		return props
	}

	ch, ok := ctx.Value(contextKeyChannel).(*discordgo.Channel)
	if ok && ch != nil {
		risky := false
		props["channelType"] = readableChannelType(ch.Type)

		if recipientID := dmChannelRecipientID(ch); recipientID != nil {
			relationshipDesc := "none"
			if rel := d.relationshipWithUserID(*recipientID); rel != nil {
				relationshipDesc = readableRelationshipType(rel.Type)
			} else if ch.Type == discordgo.ChannelTypeDM {
				// No relationship with the recipient and it's a 1:1 DM.
				risky = true
			}

			props["relationshipWithRecipient"] = relationshipDesc
			props["risky"] = risky
		}
	}

	d.lastSendAttemptMutex.Lock()
	if attempt := d.lastSendAttempt; attempt != nil {
		props["lastInMemorySendAttemptAgeMs"] = time.Since(attempt.At).Milliseconds()
		props["lastInMemorySendAttemptChannelType"] = readableChannelType(attempt.ChannelType)
		if relType := attempt.RecipientRelationshipType; relType != nil {
			props["lastInMemorySendAttemptRecipientRelationshipType"] = readableRelationshipType(*relType)
		}
	}
	d.lastSendAttemptMutex.Unlock()

	return props
}

func (d *DiscordClient) tapDiscordRESTResponse(req *http.Request, resp *http.Response, body []byte) {
	// NOTE: discordgo calls this in a blocking fashion after reading the HTTP
	// response from Discord, so don't block here.
	ctx := context.Background()

	if d.Session != nil && !d.Session.IsUser {
		return
	}

	captcha := discordauth.TryUnmarshalingCaptcha(ctx, resp, body)
	if captcha == nil {
		return
	}

	redactedEndpoint := redactDiscordRESTPath(req.URL.Path)
	props := d.baseAnalyticsProps(req.Context())
	maps.Copy(props, map[string]any{
		"apiEndpoint":      redactedEndpoint,
		"httpMethod":       req.Method,
		"captchaService":   string(captcha.Service),
		"captchaInvisible": captcha.Invisible,
		"captchaUserFlow":  captcha.UserFlow,
	})

	// (This fires a goroutine under the hood so it's alright to call this from
	// here.)
	d.UserLogin.TrackAnalytics("Discord CAPTCHA challenge", props)
}

func (d *DiscordClient) relationshipWithUserID(userID string) *discordgo.Relationship {
	if d.Session == nil || d.Session.State == nil {
		return nil
	}

	d.relationshipLock.RLock()
	defer d.relationshipLock.RUnlock()

	return d.relationships[userID]
}

func (d *DiscordClient) relationshipWithDMRecipient(ch *discordgo.Channel) *discordgo.Relationship {
	if ch == nil {
		return nil
	}

	recip := dmChannelRecipientID(ch)
	if recip == nil {
		return nil
	}

	rel := d.relationshipWithUserID(*recip)
	return rel
}

// dmChannelForUserID finds the DM channel with the given user, if any.
func (d *DiscordClient) dmChannelForUserID(userID string) *discordgo.Channel {
	if d.Session == nil || d.Session.State == nil {
		return nil
	}

	d.Session.State.RLock()
	defer d.Session.State.RUnlock()

	for _, ch := range d.Session.State.PrivateChannels {
		if len(ch.Recipients) == 1 && ch.Recipients[0].ID == userID {
			return ch
		}
	}

	return nil
}

func (d *DiscordClient) rebuildRelationships() {
	if d.Session == nil || d.Session.State == nil {
		return
	}

	d.relationshipLock.Lock()
	defer d.relationshipLock.Unlock()

	clear(d.relationships)

	for _, rel := range d.Session.State.Relationships {
		if rel == nil {
			continue
		}
		d.relationships[rel.ID] = rel
	}
}

func (d *DiscordClient) upsertRelationship(rel *discordgo.Relationship) {
	if rel == nil {
		return
	}

	d.relationshipLock.Lock()
	defer d.relationshipLock.Unlock()

	d.relationships[rel.ID] = rel
}

func (d *DiscordClient) removeRelationship(userID string) {
	d.relationshipLock.Lock()
	defer d.relationshipLock.Unlock()

	delete(d.relationships, userID)
}

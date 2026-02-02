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
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"go.mau.fi/util/ptr"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

type DiscordClient struct {
	connector      *DiscordConnector
	usersFromReady map[string]*discordgo.User
	UserLogin      *bridgev2.UserLogin
	Session        *discordgo.Session

	hasBegunSyncing bool

	markedOpened     map[string]time.Time
	markedOpenedLock sync.Mutex

	bridgeStateLock     sync.Mutex
	disconnectTimer     *time.Timer
	invalidAuthDetected bool
}

func (d *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*discordid.UserLoginMetadata)

	session, err := NewDiscordSession(ctx, meta.Token)
	login.Save(ctx)

	if err != nil {
		return err
	}

	cl := DiscordClient{
		connector: d,
		UserLogin: login,
		Session:   session,
	}
	cl.SetUp(ctx, meta)

	login.Client = &cl

	return nil
}

var _ bridgev2.NetworkAPI = (*DiscordClient)(nil)

// SetUp performs basic bookkeeping and initialization that should be done
// immediately after a DiscordClient has been created.
//
// nil may be passed for meta, especially during provisioning where we need to
// connect to the Discord gateway, but don't have a UserLogin yet.
func (d *DiscordClient) SetUp(ctx context.Context, meta *discordid.UserLoginMetadata) {
	// TODO: Turn this into a factory function like `NewDiscordClient`.
	log := zerolog.Ctx(ctx)

	// We'll have UserLogin metadata if this UserLogin is being loaded from the
	// database, i.e. it hasn't just been provisioned.
	if meta != nil {
		if meta.HeartbeatSession.IsExpired() {
			log.Info().Msg("Heartbeat session expired, creating a new one")
			meta.HeartbeatSession = discordgo.NewHeartbeatSession()
		}
		meta.HeartbeatSession.BumpLastUsed()
		d.Session.HeartbeatSession = meta.HeartbeatSession
	}

	d.markedOpened = make(map[string]time.Time)
	d.resetBridgeStateTracking()
}

func (d *DiscordClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	if d.Session == nil {
		log.Error().Msg("No session present")
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      DiscordNotLoggedIn,
		})
		return
	}

	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnecting,
	})
	if err := d.connect(ctx); err != nil {
		log.Err(err).Msg("Couldn't connect to Discord")
	}
}

func (cl *DiscordClient) handleDiscordEventSync(event any) {
	go cl.handleDiscordEvent(event)
}

func (cl *DiscordClient) connect(ctx context.Context) error {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Opening session")

	cl.Session.EventHandler = cl.handleDiscordEventSync

	err := cl.Session.Open()
	for attempts := 0; errors.Is(err, discordgo.ErrImmediateDisconnect) && attempts < 2; attempts += 1 {
		log.Err(err).Int("attempts", attempts).Msg("Immediately disconnected while trying to open session, trying again in 5 seconds")
		time.Sleep(5 * time.Second)
		err = cl.Session.Open()
	}
	if err != nil {
		log.Err(err).Msg("Failed to connect to Discord")
		cl.sendConnectFailure(err)
		return err
	}

	// Ensure that we actually have a user.
	if !cl.IsLoggedIn() {
		err := fmt.Errorf("unknown identity even after connecting to Discord")
		log.Err(err).Msg("No Discord user available after connecting")
		cl.sendConnectFailure(err)
		return err
	}
	user := cl.Session.State.User
	log.Info().Str("user_id", user.ID).Str("user_username", user.Username).Msg("Connected to Discord")

	// Stash all of the users we received in READY so we can perform quick lookups
	// keyed by user ID.
	cl.usersFromReady = make(map[string]*discordgo.User)
	for _, user := range cl.Session.State.Ready.Users {
		cl.usersFromReady[user.ID] = user
	}

	// NOTE: We won't have a UserLogin during provisioning, because the UserLogin
	// can only be properly constructed once we know what the Discord user ID is
	// (i.e. we have returned from this function). We'll rely on the login
	// process calling this method manually instead.
	cl.BeginSyncingIfUserLoginPresent(ctx)

	return nil
}

func (d *DiscordClient) Disconnect() {
	d.UserLogin.Log.Info().Msg("Disconnecting session")
	d.Session.Close()
}

func (d *DiscordClient) IsLoggedIn() bool {
	return d.Session != nil && d.Session.State != nil && d.Session.State.User != nil && d.Session.State.User.ID != ""
}

func (d *DiscordClient) LogoutRemote(ctx context.Context) {
	// FIXME(skip): Implement.
	d.Disconnect()
}

func (cl *DiscordClient) BeginSyncingIfUserLoginPresent(ctx context.Context) {
	if cl.UserLogin == nil {
		cl.connector.Bridge.Log.Warn().Msg("Not syncing just yet as we don't have a UserLogin")
		return
	}
	if cl.hasBegunSyncing {
		cl.connector.Bridge.Log.Warn().Msg("Not beginning sync more than once")
		return
	}
	cl.hasBegunSyncing = true

	log := cl.UserLogin.Log
	user := cl.Session.State.User

	// FIXME(skip): Avatar.
	cl.UserLogin.RemoteProfile = status.RemoteProfile{
		Email: user.Email,
		Phone: user.Phone,
		Name:  user.String(),
	}
	if err := cl.UserLogin.Save(ctx); err != nil {
		log.Err(err).Msg("Couldn't save UserLogin after connecting")
	}

	go cl.syncPrivateChannels(ctx)
	go cl.syncGuilds(ctx)
}

func (d *DiscordClient) syncPrivateChannels(ctx context.Context) {
	dms := slices.Clone(d.Session.State.PrivateChannels)
	// Only sync the top n private channels with recent activity.
	slices.SortFunc(dms, func(a, b *discordgo.Channel) int {
		ats, _ := discordgo.SnowflakeTimestamp(a.LastMessageID)
		bts, _ := discordgo.SnowflakeTimestamp(b.LastMessageID)
		return bts.Compare(ats)
	})

	// TODO(skip): This is startup_private_channel_create_limit. Support this in the config.
	maxDms := 10
	if maxDms > len(dms) {
		maxDms = len(dms)
	}
	for _, dm := range dms[:maxDms] {
		zerolog.Ctx(ctx).Debug().Str("channel_id", dm.ID).Msg("Syncing private channel with recent activity")
		d.syncChannel(ctx, dm)
	}
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
	log.Debug().
		Int64("permissions", perms).
		Bool("channel_visible", canView).
		Msg("Computed visibility of guild channel")
	return canView
}

func (d *DiscordClient) guildPortalKeyFromID(guildID string) networkid.PortalKey {
	// TODO: Support configuring `split_portals`.
	return networkid.PortalKey{
		ID:       discordid.MakeGuildPortalID(guildID),
		Receiver: d.UserLogin.ID,
	}
}

func (d *DiscordClient) makeAvatarForGuild(guild *discordgo.Guild) *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID: discordid.MakeAvatarID(guild.Icon),
		Get: func(ctx context.Context) ([]byte, error) {
			url := discordgo.EndpointGuildIcon(guild.ID, guild.Icon)
			return simpleDownload(ctx, url, "guild icon")
		},
		Remove: guild.Icon == "",
	}
}

func (d *DiscordClient) syncGuildSpace(ctx context.Context, guild *discordgo.Guild) error {
	prt, err := d.connector.Bridge.GetPortalByKey(ctx, d.guildPortalKeyFromID(guild.ID))
	if err != nil {
		return fmt.Errorf("couldn't get/create portal corresponding to guild: %w", err)
	}

	selfEvtSender := d.selfEventSender()
	info := &bridgev2.ChatInfo{
		Name:  &guild.Name,
		Topic: nil,
		Members: &bridgev2.ChatMemberList{
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{selfEvtSender.Sender: {EventSender: selfEvtSender}},

			// As recommended by the spec, prohibit normal events by setting
			// `events_default` to a suitably high number.
			PowerLevels: &bridgev2.PowerLevelOverrides{EventsDefault: ptr.Ptr(100)},
		},
		Avatar: d.makeAvatarForGuild(guild),
		Type:   ptr.Ptr(database.RoomTypeSpace),
	}

	if prt.MXID == "" {
		err := prt.CreateMatrixRoom(ctx, d.UserLogin, info)

		if err != nil {
			return fmt.Errorf("couldn't create room in order to materialize guild portal: %w", err)
		}
	} else {
		prt.UpdateInfo(ctx, info, d.UserLogin, nil, time.Time{})
	}

	return nil
}

func (d *DiscordClient) syncGuilds(ctx context.Context) {
	guildIDs := d.connector.Config.Guilds.BridgingGuildIDs

	for _, guildID := range guildIDs {
		log := zerolog.Ctx(ctx).With().
			Str("guild_id", guildID).
			Str("action", "sync guild").
			Logger()

		err := d.bridgeGuild(log.WithContext(ctx), guildID)
		if err != nil {
			log.Err(err).Msg("Couldn't bridge guild during sync")
		}
	}
}

func (d *DiscordClient) bridgeGuild(ctx context.Context, guildID string) error {
	log := zerolog.Ctx(ctx)

	guild, err := d.Session.State.Guild(guildID)
	if errors.Is(err, discordgo.ErrStateNotFound) || guild == nil {
		log.Err(err).Msg("Couldn't find guild, user isn't a member?")
		return errors.New("couldn't find guild in state")
	}

	err = d.syncGuildSpace(ctx, guild)
	if err != nil {
		log.Err(err).Msg("Couldn't sync guild space portal")
		return fmt.Errorf("couldn't sync guild space portal: %w", err)
	}

	for _, guildCh := range guild.Channels {
		if guildCh.Type != discordgo.ChannelTypeGuildText {
			// TODO implement categories (spaces) and news channels
			log.Trace().
				Str("channel_id", guildCh.ID).
				Int("channel_type", int(guildCh.Type)).
				Msg("Not bridging guild channel due to type")
			continue
		}

		if !d.canSeeGuildChannel(ctx, guildCh) {
			log.Trace().
				Str("channel_id", guildCh.ID).
				Int("channel_type", int(guildCh.Type)).
				Msg("Not bridging guild channel that the user doesn't have permission to view")

			continue
		}

		d.syncChannel(ctx, guildCh)
	}

	log.Debug().Msg("Subscribing to guild after bridging")
	err = d.Session.SubscribeGuild(discordgo.GuildSubscribeData{
		GuildID:    guild.ID,
		Typing:     true,
		Activities: true,
		Threads:    true,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to subscribe to guild; proceeding")
	}

	return nil
}

func simpleDownload(ctx context.Context, url, thing string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s: %w", thing, err)
	}

	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
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
	return d.makeEventSenderWithID(user.ID)
}

func (d *DiscordClient) syncChannel(_ context.Context, ch *discordgo.Channel) {
	d.connector.Bridge.QueueRemoteEvent(d.UserLogin, &DiscordChatResync{
		Client:    d,
		channel:   ch,
		portalKey: discordid.MakePortalKey(ch, d.UserLogin.ID, true),
	})
}

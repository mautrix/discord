// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
)

type DiscordClient struct {
	connector       *DiscordConnector
	usersFromReady  map[string]*discordgo.User
	UserLogin       *bridgev2.UserLogin
	Session         *discordgo.Session
	hasBegunSyncing bool
}

func (d *DiscordConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	log := login.Log
	meta := login.Metadata.(*UserLoginMetadata)

	session, err := discordgo.New(meta.Token)
	if meta.HeartbeatSession.IsExpired() {
		log.Info().Msg("Heartbeat session expired, creating a new one")
		meta.HeartbeatSession = discordgo.NewHeartbeatSession()
	}
	meta.HeartbeatSession.BumpLastUsed()
	session.HeartbeatSession = meta.HeartbeatSession
	login.Save(ctx)

	if err != nil {
		return err
	}

	// FIXME(skip): Implement.
	session.EventHandler = func(evt any) {}

	login.Client = &DiscordClient{
		connector: d,
		UserLogin: login,
		Session:   session,
	}

	return nil
}

var _ bridgev2.NetworkAPI = (*DiscordClient)(nil)

func (d *DiscordClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	if d.Session == nil {
		log.Error().Msg("No session present")
		d.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "discord-not-logged-in",
		})
		return
	}

	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnecting,
	})
	if err := d.connect(ctx); err != nil {
		log.Err(err).Msg("Couldn't connect to Discord")
	}
	// TODO(skip): Use event handler and send this in response to READY/RESUMED instead?
	d.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})
}

func (cl *DiscordClient) connect(ctx context.Context) error {
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Opening session")

	err := cl.Session.Open()
	for attempts := 0; errors.Is(err, discordgo.ErrImmediateDisconnect) && attempts < 2; attempts += 1 {
		log.Err(err).Int("attempts", attempts).Msg("Immediately disconnected while trying to open session, trying again in 5 seconds")
		time.Sleep(5 * time.Second)
		err = cl.Session.Open()
	}
	if err != nil {
		log.Err(err).Msg("Failed to connect to Discord")
		return err
	}

	// Ensure that we actually have a user.
	if !cl.IsLoggedIn() {
		return fmt.Errorf("unknown identity even after connecting to Discord")
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
	d.Session = nil
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
		cl.connector.bridge.Log.Warn().Msg("Not syncing just yet as we don't have a UserLogin")
		return
	}
	if cl.hasBegunSyncing {
		cl.connector.bridge.Log.Warn().Msg("Not beginning sync more than once")
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
	for _, dm := range dms[:10] {
		zerolog.Ctx(ctx).Debug().Str("channel_id", dm.ID).Msg("Syncing private channel with recent activity")
		d.syncChannel(ctx, dm, true)
	}
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

func makeChannelAvatar(ch *discordgo.Channel) *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(ch.Icon),
		Get: func(ctx context.Context) ([]byte, error) {
			url := discordgo.EndpointGroupIcon(ch.ID, ch.Icon)
			return simpleDownload(ctx, url, "group dm icon")
		},
		Remove: ch.Icon == "",
	}
}

func (d *DiscordClient) syncChannel(_ context.Context, ch *discordgo.Channel, selfIsInChannel bool) {
	isGroup := len(ch.RecipientIDs) > 1

	var roomType database.RoomType
	if isGroup {
		roomType = database.RoomTypeGroupDM
	} else {
		roomType = database.RoomTypeDM
	}

	selfEventSender := d.makeEventSender(d.Session.State.User)

	var members bridgev2.ChatMemberList
	members.IsFull = true
	members.MemberMap = make(bridgev2.ChatMemberMap, len(ch.Recipients))
	if len(ch.Recipients) > 0 {
		// Private channels' array of participants doesn't include ourselves,
		// so this boolean can be used to inject ourselves as a member.
		if selfIsInChannel {
			members.MemberMap[selfEventSender.Sender] = bridgev2.ChatMember{EventSender: selfEventSender}
		}

		for _, recipient := range ch.Recipients {
			sender := d.makeEventSender(recipient)
			members.MemberMap[sender.Sender] = bridgev2.ChatMember{EventSender: sender}
		}

		members.TotalMemberCount = len(ch.Recipients)
	}

	d.connector.bridge.QueueRemoteEvent(d.UserLogin, &DiscordChatResync{
		channel:   ch,
		portalKey: MakePortalKey(ch, d.UserLogin.ID, true),
		info: &bridgev2.ChatInfo{
			Name:        &ch.Name,
			Members:     &members,
			Avatar:      makeChannelAvatar(ch),
			Type:        &roomType,
			CanBackfill: true,
		},
	})
}

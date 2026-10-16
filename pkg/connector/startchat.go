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
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*DiscordClient)(nil)
	_ bridgev2.GhostDMCreatingNetworkAPI     = (*DiscordClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*DiscordClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*DiscordClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*DiscordConnector)(nil)
)

func isSnowflake(id string) bool {
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

func (d *DiscordConnector) ValidateUserID(id networkid.UserID) bool {
	return isSnowflake(discordid.ParseUserID(id))
}

func (d *DiscordClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	identifier = strings.TrimSpace(identifier)
	identifier = strings.TrimPrefix(identifier, "discord:")
	identifier = strings.TrimPrefix(identifier, "@")

	var user *discordgo.User
	if isSnowflake(identifier) {
		user = d.userCache.Resolve(ctx, identifier)
	}
	if user == nil {
		user = d.knownUserWithUsername(ctx, identifier)
	}
	if user == nil {
		return nil, nil
	}

	resp, err := d.makeResolveIdentifierResponse(ctx, user)
	if err != nil {
		return nil, err
	}
	if createChat {
		resp.Chat, err = d.CreateChatWithGhost(ctx, resp.Ghost)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (d *DiscordClient) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	userID := discordid.ParseUserID(ghost.ID)
	ch := d.dmChannelForUserID(userID)
	if ch == nil {
		var err error
		ch, err = d.Session.UserChannelCreate(userID, discordgo.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("failed to create DM channel: %w", err)
		}
		if err = d.Session.State.ChannelAdd(ch); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).
				Str("channel_id", ch.ID).
				Msg("Failed to add created DM channel to state")
		}
	}

	chatInfo, err := d.getChannelChatInfo(ctx, ch)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat info: %w", err)
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:  d.portalKeyForChannel(ch),
		PortalInfo: chatInfo,
	}, nil
}

func (d *DiscordClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	friends := d.friends(ctx)
	results := make([]*bridgev2.ResolveIdentifierResponse, 0, len(friends))
	for _, user := range friends {
		resp, err := d.makeResolveIdentifierResponse(ctx, user)
		if err != nil {
			return nil, err
		}
		results = append(results, resp)
	}
	return results, nil
}

func (d *DiscordClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if d.Session == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}

	query = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if query == "" {
		return nil, nil
	}

	var results []*bridgev2.ResolveIdentifierResponse
	for _, user := range d.knownUsers(ctx) {
		if !d.userMatchesQuery(user, query) {
			continue
		}
		resp, err := d.makeResolveIdentifierResponse(ctx, user)
		if err != nil {
			return nil, err
		}
		results = append(results, resp)
	}
	return results, nil
}

func (d *DiscordClient) makeResolveIdentifierResponse(ctx context.Context, user *discordgo.User) (*bridgev2.ResolveIdentifierResponse, error) {
	userID := discordid.MakeUserID(user.ID)
	ghost, err := d.connector.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	return &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: d.getUserInfo(ctx, user),
	}, nil
}

func (d *DiscordClient) userMatchesQuery(user *discordgo.User, query string) bool {
	if strings.Contains(strings.ToLower(user.String()), query) ||
		strings.Contains(strings.ToLower(user.GlobalName), query) {
		return true
	}
	rel := d.relationshipWithUserID(user.ID)
	return rel != nil && strings.Contains(strings.ToLower(rel.Nickname), query)
}

func (d *DiscordClient) knownUserWithUsername(ctx context.Context, username string) *discordgo.User {
	if username == "" {
		return nil
	}
	for _, user := range d.knownUsers(ctx) {
		if strings.EqualFold(user.String(), username) || strings.EqualFold(user.Username, username) {
			return user
		}
	}
	return nil
}

func (d *DiscordClient) friendIDs() []string {
	d.relationshipLock.RLock()
	defer d.relationshipLock.RUnlock()

	ids := make([]string, 0, len(d.relationships))
	for id, rel := range d.relationships {
		if rel.Type == discordgo.RelationshipFriend {
			ids = append(ids, id)
		}
	}
	return ids
}

func (d *DiscordClient) friends(ctx context.Context) []*discordgo.User {
	ids := d.friendIDs()
	users := make([]*discordgo.User, 0, len(ids))
	for _, id := range ids {
		if user := d.userCache.Resolve(ctx, id); user != nil {
			users = append(users, user)
		}
	}
	return users
}

func (d *DiscordClient) knownUsers(ctx context.Context) []*discordgo.User {
	selfID := discordid.ParseUserLoginID(d.UserLogin.ID)
	seen := map[string]struct{}{selfID: {}}
	var users []*discordgo.User
	add := func(user *discordgo.User) {
		if user == nil || user.ID == "" {
			return
		}
		if _, ok := seen[user.ID]; ok {
			return
		}
		seen[user.ID] = struct{}{}
		users = append(users, user)
	}

	for _, user := range d.friends(ctx) {
		add(user)
	}

	d.Session.State.RLock()
	for _, ch := range d.Session.State.PrivateChannels {
		for _, recipient := range ch.Recipients {
			add(recipient)
		}
	}
	d.Session.State.RUnlock()

	return users
}

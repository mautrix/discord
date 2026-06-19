// GetChatInfo / GetUserInfo and ChatMemberList population.
// Implemented in Group 4 (Task 4.3).
package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// ErrDMingStranger is returned by GetChatInfo when forbid_dming_strangers is
// enabled and the target user is not a friend of the logged-in account (FR-65/66).
var ErrDMingStranger = errors.New("refusing to create DM: user is not a friend")

// ErrRelationshipsNotReady is returned when the relationship cache hasn't been
// fully populated yet from the READY payload. The caller should retry once
// RelationshipsReady() returns true.
var ErrRelationshipsNotReady = errors.New("refusing to create DM: relationship cache not yet ready")

// channelNameData is the data passed to channel_name_template.
// Fields mirror the legacy config.ChannelNameParams for template parity.
type channelNameData struct {
	Name       string
	ParentName string
	GuildName  string
	NSFW       bool
	Type       discordgo.ChannelType
}

// displaynameData is the data passed to displayname_template.
type displaynameData struct {
	ID            string
	Username      string
	GlobalName    string
	Discriminator string
	Bot           bool
	System        bool
	Webhook       bool
	Application   bool
}

// guildNameData is the data passed to guild_name_template.
type guildNameData struct {
	Name string
}

// GetChatInfo returns channel/DM/guild-space info for a portal. Dispatches by
// room type and channel type. Guild-space portals have RoomType==space and
// their PortalID is the guild snowflake. All other portals are fetched as
// Discord channels from the gateway state or REST.
func (dc *DiscordClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	meta, _ := portal.Metadata.(*PortalMeta)

	// Guild-space portals: RoomType is "space" and the PortalID holds the guild
	// snowflake (MakeGuildPortalID). Guild-category portals are also spaces but
	// have meta.GuildID set and meta.ChannelType == GuildCategory.
	if portal.RoomType == database.RoomTypeSpace {
		// Distinguish a guild-space (no GuildID in meta, or ChannelType==0)
		// from a category (ChannelType==GuildCategory).
		if meta == nil || meta.ChannelType != discordgo.ChannelTypeGuildCategory {
			// This is the top-level guild space.
			guildID := string(portal.ID) // guild portal ID == guild snowflake
			return dc.getGuildSpaceChatInfo(ctx, guildID, meta)
		}
		// Category: fall through to channel fetch below (treated as space).
	}

	// Fetch channel from gateway state, falling back to REST.
	channelID := discordid.ParsePortalID(portal.ID)
	channel, err := dc.fetchChannel(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch channel %s: %w", channelID, err)
	}

	switch channel.Type {
	case discordgo.ChannelTypeDM:
		return dc.getDMChatInfo(ctx, channel)
	case discordgo.ChannelTypeGroupDM:
		return dc.getGroupDMChatInfo(ctx, channel)
	case discordgo.ChannelTypeGuildCategory:
		return dc.getGuildCategoryChatInfo(ctx, channel)
	default:
		// Guild text, voice, news, forum, threads, etc.
		return dc.getGuildChannelChatInfo(ctx, channel)
	}
}

// fetchChannel retrieves a channel from the gateway state cache first, then
// falls back to a REST call when the state cache is empty.
func (dc *DiscordClient) fetchChannel(ctx context.Context, channelID string) (*discordgo.Channel, error) {
	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}
	// Try the in-memory state cache (cheap, no network).
	if ch, _ := sess.State.Channel(channelID); ch != nil {
		return ch, nil
	}
	// Fall back to REST.
	ch, err := sess.Channel(channelID)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// fetchGuild retrieves a guild from the gateway state cache, then falls back
// to a REST call.
func (dc *DiscordClient) fetchGuild(ctx context.Context, guildID string) (*discordgo.Guild, error) {
	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}
	if g, _ := sess.State.Guild(guildID); g != nil {
		return g, nil
	}
	g, err := sess.Guild(guildID)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// getGuildSpaceChatInfo builds a ChatInfo for a guild-space portal.
func (dc *DiscordClient) getGuildSpaceChatInfo(ctx context.Context, guildID string, meta *PortalMeta) (*bridgev2.ChatInfo, error) {
	guild, err := dc.fetchGuild(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch guild %s: %w", guildID, err)
	}

	roomType := database.RoomTypeSpace

	name, err := dc.connector.Config.FormatGuildName(guildNameData{Name: guild.Name})
	if err != nil {
		// Fall back to the raw name on template error.
		name = guild.Name
	}

	info := &bridgev2.ChatInfo{
		Name:  &name,
		Type:  &roomType,
		Topic: ptr(""),
	}

	// Guild icon as space avatar.
	if guild.Icon != "" {
		info.Avatar = makeGuildAvatar(guild.ID, guild.Icon)
	}

	// No members list for spaces — they are managed via space-child relationships.

	return info, nil
}

// getDMChatInfo builds a ChatInfo for a 1-to-1 DM portal (FR-65/66).
func (dc *DiscordClient) getDMChatInfo(ctx context.Context, channel *discordgo.Channel) (*bridgev2.ChatInfo, error) {
	// FR-65/66: gate DM creation on the relationship cache.
	if dc.connector.Config.ForbidDMingStrangers {
		if err := dc.checkDMRelationship(channel); err != nil {
			return nil, err
		}
	}

	// Find the other user in the DM.
	otherUserID := dc.findDMRecipient(channel)

	// Fetch recipient's profile to use as the room name/avatar.
	var name string
	var avatar *bridgev2.Avatar
	if otherUserID != "" {
		user, err := dc.fetchUser(ctx, otherUserID)
		if err == nil && user != nil {
			// Use friend nickname from relationship cache if set (ported from legacy UpdateInfo).
			rel, hasRel := dc.GetRelationship(otherUserID)
			if hasRel && rel.Nickname != "" {
				name = rel.Nickname
			} else {
				name = dc.formatDisplayname(user, false, false)
			}
			if user.Avatar != "" {
				avatar = makeUserAvatar("", user.ID, user.Avatar)
			}
		}
	}

	roomType := database.RoomTypeDM
	otherUID := discordid.MakeUserID(otherUserID)

	info := &bridgev2.ChatInfo{
		Name:   &name,
		Avatar: avatar,
		Type:   &roomType,
		Members: &bridgev2.ChatMemberList{
			IsFull:           true,
			OtherUserID:      otherUID,
			TotalMemberCount: 2,
			MemberMap:        makeDMMemberMap(dc.userLogin, otherUID),
		},
	}

	// Store the channel type in PortalMeta.
	info.ExtraUpdates = portalMetaUpdater(discordgo.ChannelTypeDM, "", false)

	return info, nil
}

// getGroupDMChatInfo builds a ChatInfo for a group DM portal.
func (dc *DiscordClient) getGroupDMChatInfo(ctx context.Context, channel *discordgo.Channel) (*bridgev2.ChatInfo, error) {
	name, err := dc.connector.Config.FormatChannelName(channelNameData{
		Name: channel.Name,
		Type: channel.Type,
	})
	if err != nil {
		name = channel.Name
	}

	roomType := database.RoomTypeGroupDM
	info := &bridgev2.ChatInfo{
		Name:  &name,
		Topic: ptr(channel.Topic),
		Type:  &roomType,
	}

	// Group DM icon.
	if channel.Icon != "" {
		info.Avatar = makeGroupDMAvatar(channel.ID, channel.Icon)
	}

	// Member list from recipients.
	info.Members = dc.makeGroupDMMemberList(channel)

	// Store the channel type in PortalMeta.
	info.ExtraUpdates = portalMetaUpdater(discordgo.ChannelTypeGroupDM, "", false)

	return info, nil
}

// getGuildCategoryChatInfo builds a ChatInfo for a guild category portal
// (which becomes a Matrix space that is a child of the guild space).
func (dc *DiscordClient) getGuildCategoryChatInfo(ctx context.Context, channel *discordgo.Channel) (*bridgev2.ChatInfo, error) {
	guild, err := dc.fetchGuild(ctx, channel.GuildID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch guild for category %s: %w", channel.ID, err)
	}

	name, err := dc.connector.Config.FormatChannelName(channelNameData{
		Name:      channel.Name,
		GuildName: guild.Name,
		NSFW:      channel.NSFW,
		Type:      channel.Type,
	})
	if err != nil {
		name = channel.Name
	}

	roomType := database.RoomTypeSpace
	guildPortalID := discordid.MakeGuildPortalID(channel.GuildID)

	info := &bridgev2.ChatInfo{
		Name:     &name,
		Type:     &roomType,
		ParentID: &guildPortalID,
	}

	info.JoinRule = dc.makeJoinRule(channel.GuildID)
	info.ExtraUpdates = portalMetaUpdater(discordgo.ChannelTypeGuildCategory, channel.GuildID, channel.NSFW)

	return info, nil
}

// getGuildChannelChatInfo builds a ChatInfo for a regular guild channel
// (text, voice, news, forum, stage, thread).
func (dc *DiscordClient) getGuildChannelChatInfo(ctx context.Context, channel *discordgo.Channel) (*bridgev2.ChatInfo, error) {
	guild, err := dc.fetchGuild(ctx, channel.GuildID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch guild for channel %s: %w", channel.ID, err)
	}

	var parentName string
	var parentPortalID *networkid.PortalID
	if channel.ParentID != "" {
		parentID := discordid.MakePortalID(channel.ParentID)
		parentPortalID = &parentID
		// Attempt to resolve the parent's name for the channel name template.
		if parent, err2 := dc.fetchChannel(ctx, channel.ParentID); err2 == nil {
			parentName = parent.Name
		}
	} else {
		// No category parent: parent is the guild space.
		guildPortalID := discordid.MakeGuildPortalID(channel.GuildID)
		parentPortalID = &guildPortalID
	}

	name, err := dc.connector.Config.FormatChannelName(channelNameData{
		Name:       channel.Name,
		ParentName: parentName,
		GuildName:  guild.Name,
		NSFW:       channel.NSFW,
		Type:       channel.Type,
	})
	if err != nil {
		name = channel.Name
	}

	roomType := database.RoomTypeDefault
	info := &bridgev2.ChatInfo{
		Name:     &name,
		Topic:    ptr(channel.Topic),
		Type:     &roomType,
		ParentID: parentPortalID,
	}

	info.JoinRule = dc.makeJoinRule(channel.GuildID)
	info.ExtraUpdates = portalMetaUpdater(channel.Type, channel.GuildID, channel.NSFW)

	// Bot power levels: the bot should have admin so it can manage the room.
	info.Members = &bridgev2.ChatMemberList{
		IsFull: false, // guild channels have large member lists; don't sync all
		PowerLevels: &bridgev2.PowerLevelOverrides{
			Custom: setBotAdminPowerLevel(dc),
		},
	}

	return info, nil
}

// checkDMRelationship enforces the ForbidDMingStrangers policy. It returns an
// error if the relationship cache is not ready or if the other user is not a
// friend of the logged-in account (FR-65/66).
func (dc *DiscordClient) checkDMRelationship(channel *discordgo.Channel) error {
	if !dc.RelationshipsReady() {
		return ErrRelationshipsNotReady
	}
	otherUserID := dc.findDMRecipient(channel)
	if otherUserID == "" {
		// Can't determine the other user — allow to avoid false positives.
		return nil
	}
	rel, ok := dc.GetRelationship(otherUserID)
	if !ok || rel.Type != discordgo.RelationshipFriend {
		return ErrDMingStranger
	}
	return nil
}

// findDMRecipient returns the Discord user ID of the other participant in a DM
// channel. For 1-to-1 DMs there is exactly one recipient; for group DMs we skip.
func (dc *DiscordClient) findDMRecipient(channel *discordgo.Channel) string {
	loginID := string(dc.userLogin.ID)
	for _, r := range channel.Recipients {
		if r != nil && r.ID != loginID {
			return r.ID
		}
	}
	// If the channel has no recipients (can happen on some REST calls), use
	// the PortalKey receiver which is the other user's ID for DMs.
	return ""
}

// fetchUser fetches a Discord user from the gateway state cache or via REST.
func (dc *DiscordClient) fetchUser(ctx context.Context, userID string) (*discordgo.User, error) {
	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}
	if u, _ := sess.State.Member(userID, userID); u != nil && u.User != nil {
		return u.User, nil
	}
	return sess.User(userID)
}

// makeJoinRule returns the join rules event content for a guild channel.
// When restricted_rooms is enabled the room uses "restricted" join rules that
// allow members of the guild space to join (FR-19). Otherwise it is invite-only.
func (dc *DiscordClient) makeJoinRule(guildID string) *event.JoinRulesEventContent {
	if !dc.connector.Config.RestrictedRooms || guildID == "" {
		return nil
	}
	// We can only set the restricted rule if the guild space has a Matrix room.
	// Return nil here and let the framework apply the default invite-only rule;
	// Group 4 handlediscord.go will update when the guild space MXID is known.
	guildPortalKey := networkid.PortalKey{ID: discordid.MakeGuildPortalID(guildID)}
	guildPortal, err := dc.br.GetExistingPortalByKey(context.Background(), guildPortalKey)
	if err != nil || guildPortal == nil || guildPortal.MXID == "" {
		return nil
	}
	return &event.JoinRulesEventContent{
		JoinRule: event.JoinRuleRestricted,
		Allow: []event.JoinRuleAllow{{
			RoomID: guildPortal.MXID,
			Type:   event.JoinRuleAllowRoomMembership,
		}},
	}
}

// makeDMMemberMap builds a minimal ChatMemberMap for a 1-to-1 DM: the bot
// (this login) and the other user.
func makeDMMemberMap(login *bridgev2.UserLogin, otherUserID networkid.UserID) bridgev2.ChatMemberMap {
	m := make(bridgev2.ChatMemberMap, 2)
	// The logged-in user (this login's ghost).
	m.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{
			IsFromMe:    true,
			SenderLogin: login.ID,
		},
		Membership: event.MembershipJoin,
	})
	// The remote user.
	m.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{
			Sender: otherUserID,
		},
		Membership: event.MembershipJoin,
	})
	return m
}

// makeGroupDMMemberList builds the ChatMemberList for a group DM from
// channel.Recipients. The logged-in user is also added as a full member.
func (dc *DiscordClient) makeGroupDMMemberList(channel *discordgo.Channel) *bridgev2.ChatMemberList {
	memberMap := make(bridgev2.ChatMemberMap, len(channel.Recipients)+1)
	// Add this login.
	memberMap.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{
			IsFromMe:    true,
			SenderLogin: dc.userLogin.ID,
		},
		Membership: event.MembershipJoin,
	})
	for _, r := range channel.Recipients {
		if r == nil {
			continue
		}
		uid := discordid.MakeUserID(r.ID)
		name := dc.formatDisplayname(r, false, false)
		memberMap.Set(bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{Sender: uid},
			Membership:  event.MembershipJoin,
			UserInfo: &bridgev2.UserInfo{
				Name: &name,
			},
		})
	}
	return &bridgev2.ChatMemberList{
		IsFull:           true,
		TotalMemberCount: len(memberMap),
		MemberMap:        memberMap,
	}
}

// setBotAdminPowerLevel returns a custom power-level modifier that grants
// the bridge bot admin (100) on the room. Used for guild channels.
func setBotAdminPowerLevel(dc *DiscordClient) func(*event.PowerLevelsEventContent) bool {
	return func(pl *event.PowerLevelsEventContent) bool {
		botMXID := dc.br.Bot.GetMXID()
		if pl.Users == nil {
			pl.Users = make(map[id.UserID]int)
		}
		if pl.Users[botMXID] == 100 {
			return false
		}
		pl.Users[botMXID] = 100
		return true
	}
}

// portalMetaUpdater returns an ExtraUpdater that writes the channel type, guild
// ID, and NSFW flag into PortalMeta. Called from GetChatInfo to keep the stored
// metadata in sync.
func portalMetaUpdater(chanType discordgo.ChannelType, guildID string, nsfw bool) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(ctx context.Context, p *bridgev2.Portal) bool {
		meta, ok := p.Metadata.(*PortalMeta)
		if !ok || meta == nil {
			return false
		}
		changed := false
		if meta.ChannelType != chanType {
			meta.ChannelType = chanType
			changed = true
		}
		if meta.GuildID != guildID {
			meta.GuildID = guildID
			changed = true
		}
		if meta.NSFW != nsfw {
			meta.NSFW = nsfw
			changed = true
		}
		return changed
	}
}

// makeGuildAvatar constructs an Avatar that lazily downloads the guild icon.
func makeGuildAvatar(guildID, iconHash string) *bridgev2.Avatar {
	var downloadURL string
	if strings.HasPrefix(iconHash, "a_") {
		downloadURL = discordgo.EndpointGuildIconAnimated(guildID, iconHash)
	} else {
		downloadURL = discordgo.EndpointGuildIcon(guildID, iconHash)
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(fmt.Sprintf("guild/%s/%s", guildID, iconHash)),
		Get: func(ctx context.Context) ([]byte, error) {
			return downloadMedia(ctx, downloadURL)
		},
	}
}

// makeGroupDMAvatar constructs an Avatar for a group DM icon.
func makeGroupDMAvatar(channelID, iconHash string) *bridgev2.Avatar {
	downloadURL := discordgo.EndpointGroupIcon(channelID, iconHash)
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(fmt.Sprintf("groupdm/%s/%s", channelID, iconHash)),
		Get: func(ctx context.Context) ([]byte, error) {
			return downloadMedia(ctx, downloadURL)
		},
	}
}

// makeUserAvatar constructs an Avatar for a Discord user. When guildID is
// non-empty, the guild-member avatar is used if available (FR-49).
func makeUserAvatar(guildID, userID, avatarID string) *bridgev2.Avatar {
	var downloadURL string
	if guildID != "" {
		if strings.HasPrefix(avatarID, "a_") {
			downloadURL = discordgo.EndpointGuildMemberAvatarAnimated(guildID, userID, avatarID)
		} else {
			downloadURL = discordgo.EndpointGuildMemberAvatar(guildID, userID, avatarID)
		}
	} else {
		if strings.HasPrefix(avatarID, "a_") {
			downloadURL = discordgo.EndpointUserAvatarAnimated(userID, avatarID)
		} else {
			downloadURL = discordgo.EndpointUserAvatar(userID, avatarID)
		}
	}
	avatarIDStr := fmt.Sprintf("avatar/%s/%s/%s", guildID, userID, avatarID)
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(avatarIDStr),
		Get: func(ctx context.Context) ([]byte, error) {
			return downloadMedia(ctx, downloadURL)
		},
	}
}

// downloadMedia fetches a URL and returns the raw bytes.
func downloadMedia(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, url)
	}
	buf := make([]byte, 0, resp.ContentLength)
	tmp := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if readErr != nil {
			break
		}
	}
	return buf, nil
}

// --- GetUserInfo ---

// GetUserInfo returns profile info for a ghost (Discord user), including name,
// avatar, IsBot flag, and Beeper identifiers (FR-48/49).
func (dc *DiscordClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	userID := string(ghost.ID)

	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}

	user, err := sess.User(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Discord user %s: %w", userID, err)
	}

	ghostMeta, _ := ghost.Metadata.(*GhostMeta)
	isWebhook := ghostMeta != nil && ghostMeta.IsWebhook

	name := dc.formatDisplayname(user, isWebhook, false)
	isBot := user.Bot

	info := &bridgev2.UserInfo{
		Name:  &name,
		IsBot: &isBot,
	}

	// Identifiers (FR-49, Beeper contact info).
	discordUsername := user.Username
	if user.Discriminator != "" && user.Discriminator != "0" {
		discordUsername += "#" + user.Discriminator
	}
	if isWebhook {
		// Webhooks have no stable Discord identity; omit the identifier.
		info.Identifiers = []string{}
	} else {
		info.Identifiers = []string{
			fmt.Sprintf("discord:%s", discordUsername),
		}
	}

	// Global avatar (FR-49, including animated avatars).
	if user.Avatar != "" {
		info.Avatar = makeUserAvatar("", user.ID, user.Avatar)
	}

	// GhostMeta: propagate IsWebhook.
	if ghostMeta != nil {
		info.ExtraUpdates = func(ctx context.Context, g *bridgev2.Ghost) bool {
			m, ok := g.Metadata.(*GhostMeta)
			if !ok || m == nil {
				return false
			}
			if m.IsWebhook != isWebhook {
				m.IsWebhook = isWebhook
				return true
			}
			return false
		}
	}

	return info, nil
}

// formatDisplayname executes the displayname_template against the user data.
// Falls back to GlobalName, then Username#Discriminator on template failure.
func (dc *DiscordClient) formatDisplayname(user *discordgo.User, isWebhook, isApplication bool) string {
	if user == nil {
		return ""
	}
	data := displaynameData{
		ID:            user.ID,
		Username:      user.Username,
		GlobalName:    user.GlobalName,
		Discriminator: user.Discriminator,
		Bot:           user.Bot,
		System:        user.System,
		Webhook:       isWebhook,
		Application:   isApplication,
	}
	name, err := dc.connector.Config.FormatDisplayname(data)
	if err != nil || name == "" {
		// Fallback: GlobalName or Username + discriminator.
		if user.GlobalName != "" {
			return user.GlobalName
		}
		if user.Discriminator != "" && user.Discriminator != "0" {
			return user.Username + "#" + user.Discriminator
		}
		return user.Username
	}
	return name
}

// ptr returns a pointer to the given value. Convenience helper for optional ChatInfo fields.
func ptr[T any](v T) *T { return &v }

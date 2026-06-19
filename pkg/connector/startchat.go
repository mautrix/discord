// IdentifierResolvingNetworkAPI + GhostDMCreatingNetworkAPI — start-chat and
// provisioning (FR-56, FR-57). Ported from the legacy FindPrivateChatWith /
// User.handlePrivateChannel pattern in portal.go / user.go.
package connector

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// resolveDiscordUser looks up a Discord user by snowflake or username.
//
//   - If identifier is a 17-to-20-digit decimal string it is treated as a
//     snowflake and fetched directly via REST.
//   - Otherwise we try the state-cache member lookup; if that misses we
//     return a descriptive error asking for a numeric snowflake (bot tokens
//     have no search API).
//
// The function requires an active gateway session.
func (dc *DiscordClient) resolveDiscordUser(ctx context.Context, identifier string) (*discordgo.User, error) {
	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}

	// Snowflake path: 17-20 decimal digit number.
	if isSnowflake(identifier) {
		user, err := sess.User(identifier)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch Discord user %s: %w", identifier, err)
		}
		return user, nil
	}

	// Username path: strip a leading "@" then probe the state cache.
	// discordgo has no public username-search API on bot tokens; on user tokens
	// the state cache is populated from READY private channels / guild members
	// but there is no dedicated lookup.  We try the state cache as a best-effort
	// and surface a clear error on miss so callers know to use a snowflake.
	username := identifier
	if len(username) > 0 && username[0] == '@' {
		username = username[1:]
	}
	if u, _ := sess.State.Member(username, username); u != nil && u.User != nil {
		return u.User, nil
	}
	return nil, fmt.Errorf("cannot resolve Discord username %q: provide a numeric snowflake ID instead", identifier)
}

// isSnowflake reports whether s looks like a Discord snowflake (17-20
// decimal digits).
func isSnowflake(s string) bool {
	if len(s) < 17 || len(s) > 20 {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// openDMChannel opens (or reuses) the Discord DM channel with the given user
// and returns it. discordgo.UserChannelCreate is idempotent — Discord returns
// the existing channel if one already exists. Ported from legacy
// user.handlePrivateChannel / portal.CreateMatrixRoom flow.
func (dc *DiscordClient) openDMChannel(ctx context.Context, userID string) (*discordgo.Channel, error) {
	dc.sessionLock.Lock()
	sess := dc.Session
	dc.sessionLock.Unlock()
	if sess == nil {
		return nil, errors.New("not connected to Discord")
	}
	ch, err := sess.UserChannelCreate(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to open DM channel with %s: %w", userID, err)
	}
	return ch, nil
}

// checkCanDMUser enforces the ForbidDMingStrangers policy. If the policy is
// disabled this is a no-op. Bot-token sessions are exempt because bots can DM
// any user. Ported from portal.go handleMatrixMessage stranger gate (FR-65/66).
func (dc *DiscordClient) checkCanDMUser(userID string) error {
	if !dc.connector.Config.ForbidDMingStrangers {
		return nil
	}
	// Bot tokens are exempt from the stranger gate.
	if dc.Meta().TokenType != TokenTypeUser {
		return nil
	}
	if !dc.RelationshipsReady() {
		return ErrRelationshipsNotReady
	}
	rel, ok := dc.GetRelationship(userID)
	if !ok || rel.Type != discordgo.RelationshipFriend {
		return ErrDMingStranger
	}
	return nil
}

// openDMForUser opens the DM channel with the given Discord user ID and
// returns the CreateChatResponse containing the portal key. Factored out so
// both ResolveIdentifier and CreateChatWithGhost share the REST call.
func (dc *DiscordClient) openDMForUser(ctx context.Context, discordUserID string) (*bridgev2.CreateChatResponse, error) {
	ch, err := dc.openDMChannel(ctx, discordUserID)
	if err != nil {
		return nil, err
	}
	portalKey := discordid.MakePortalKey(ch.ID, dc.userLogin.ID, true)
	return &bridgev2.CreateChatResponse{PortalKey: portalKey}, nil
}

// --- IdentifierResolvingNetworkAPI ---

// ResolveIdentifier looks up a Discord user by snowflake (or cached username
// where feasible). When createChat is true it additionally opens the DM
// channel via the Discord REST API and includes the portal key in the response.
//
// Accepted identifier formats:
//   - Numeric Discord snowflake (17-20 digits) — always supported.
//   - "@username" — resolved via gateway state cache; on bot tokens this
//     typically fails; callers should use snowflakes.
//
// The DM stranger gate (FR-65/66) is enforced when createChat is true.
func (dc *DiscordClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	user, err := dc.resolveDiscordUser(ctx, identifier)
	if err != nil {
		return nil, err
	}

	if createChat {
		if gateErr := dc.checkCanDMUser(user.ID); gateErr != nil {
			return nil, gateErr
		}
	}

	userID := discordid.MakeUserID(user.ID)
	ghost, err := dc.br.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost for user %s: %w", user.ID, err)
	}

	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: userID,
	}

	if createChat {
		chatResp, err := dc.openDMForUser(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		resp.Chat = chatResp
	}

	return resp, nil
}

// --- GhostDMCreatingNetworkAPI ---

// CreateChatWithGhost opens a DM with the Discord user identified by the
// ghost. The ghost's ID is the Discord user snowflake (as produced by
// MakeUserID). The DM stranger gate is enforced when ForbidDMingStrangers is
// set.
//
// The framework calls this when start-chat is invoked with a pre-validated
// user ID (one that passed ValidateUserID) or from the provisioning API.
func (dc *DiscordClient) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	discordUserID := string(ghost.ID)
	if err := dc.checkCanDMUser(discordUserID); err != nil {
		return nil, err
	}
	return dc.openDMForUser(ctx, discordUserID)
}

// makeResolveIdentifierResponseFromUserID is a convenience helper for other
// connector paths (e.g. member sync) that need a ResolveIdentifierResponse
// without opening a DM channel.
func (dc *DiscordClient) makeResolveIdentifierResponseFromUserID(ctx context.Context, discordUserID string) (*bridgev2.ResolveIdentifierResponse, error) {
	userID := networkid.UserID(discordUserID)
	ghost, err := dc.br.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost for user %s: %w", discordUserID, err)
	}
	return &bridgev2.ResolveIdentifierResponse{
		Ghost:  ghost,
		UserID: userID,
	}, nil
}

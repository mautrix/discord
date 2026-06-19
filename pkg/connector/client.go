// This file holds the DiscordClient struct (one per UserLogin) and its
// NetworkAPI implementation plus all the optional capability interfaces.
//
// Gateway lifecycle (Task 3.2) is implemented here, ported from the legacy
// top-level user.go. The Matrix→Discord handlers (Group 5), Discord→Matrix
// conversion (Group 4), chat info (Group 4) and backfill (Group 4) live in
// their own files; the methods kept here as stubs return errNotImplemented /
// zero values until those groups fill them in.
package connector

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// BotIntents are the gateway intents requested when connecting with a bot
// token. User tokens don't send an intents field at all (the gateway derives
// access from the account), so these only apply when TokenType == bot.
//
// Ported verbatim from legacy user.go.
const BotIntents = discordgo.IntentGuilds |
	discordgo.IntentGuildMessages |
	discordgo.IntentGuildMessageReactions |
	discordgo.IntentGuildMessageTyping |
	discordgo.IntentGuildBans |
	discordgo.IntentGuildEmojis |
	discordgo.IntentGuildIntegrations |
	discordgo.IntentGuildInvites |
	discordgo.IntentDirectMessages |
	discordgo.IntentDirectMessageTyping |
	// Privileged intents
	discordgo.IntentMessageContent |
	discordgo.IntentGuildMembers

// maxStartupRetries bounds the exponential backoff applied to the initial
// gateway connection. Ported from legacy startupTryConnect (retryCount < 6).
const maxStartupRetries = 6

// DiscordClient is the per-login Discord network client stored in
// UserLogin.Client. It owns one Discord gateway session.
type DiscordClient struct {
	br        *bridgev2.Bridge
	connector *DiscordConnector
	userLogin *bridgev2.UserLogin
	// meta is the typed UserLoginMeta from userLogin.Metadata; set by LoadUserLogin.
	meta *UserLoginMeta

	// Session is the active discordgo gateway session, nil when disconnected.
	// Guarded by sessionLock.
	Session     *discordgo.Session
	sessionLock sync.Mutex

	// stopConnect cancels an in-flight startup backoff loop on Disconnect.
	stopConnect chan struct{}

	// bridgeStateLock guards wasDisconnected / wasLoggedOut, which mirror the
	// legacy flags used to suppress duplicate bridge-state transitions.
	bridgeStateLock sync.Mutex
	wasDisconnected bool
	wasLoggedOut    bool

	// relationships caches the logged-in user's friend list (FR-12). It is
	// populated from READY and the RELATIONSHIP_* gateway events and gates DM
	// portal creation for strangers (FR-65/66). relationshipsReady covers the
	// brief window after READY where the map is populated but not yet
	// authoritative; both are guarded by relationshipLock.
	relationships      map[string]*discordgo.Relationship
	relationshipsReady bool
	relationshipLock   sync.RWMutex

	// pendingInteractions tracks exec command events waiting for
	// INTERACTION_SUCCESS, keyed by nonce. Guarded by pendingInteractionsLock.
	// Populated/consumed by commands.go (FR-54).
	pendingInteractions     map[string]interactionPending
	pendingInteractionsLock sync.Mutex

	// commandCache caches ApplicationCommands discovered via search, keyed by
	// channelID then command name. Guarded by commandCacheLock. Populated by
	// the exec/commands commands (FR-54).
	commandCache     map[string]map[string]*discordgo.ApplicationCommand
	commandCacheLock sync.Mutex
}

// Meta returns the typed UserLogin metadata for this client, preferring the
// field populated by LoadUserLogin and falling back to the live metadata.
func (dc *DiscordClient) Meta() *UserLoginMeta {
	if dc.meta != nil {
		return dc.meta
	}
	if m, ok := dc.userLogin.Metadata.(*UserLoginMeta); ok && m != nil {
		dc.meta = m
		return m
	}
	dc.meta = &UserLoginMeta{}
	return dc.meta
}

// logger returns the per-login logger. It returns a pointer because zerolog's
// level methods have pointer receivers in this version.
func (dc *DiscordClient) logger() *zerolog.Logger {
	return &dc.userLogin.Log
}

// --- NetworkAPI (required) ---

// Connect opens the Discord gateway. It returns promptly; the actual
// connection (with exponential backoff on transient failures) runs in a
// background goroutine and reports progress via the bridge-state queue. The
// framework calls Connect sequentially per login, so it must not block.
func (dc *DiscordClient) Connect(ctx context.Context) {
	if !dc.IsLoggedIn() {
		dc.userLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "dc-no-token",
			Message:    "User does not have a Discord token stored",
		})
		return
	}
	dc.bridgeStateLock.Lock()
	dc.wasLoggedOut = false
	dc.bridgeStateLock.Unlock()

	dc.sessionLock.Lock()
	if dc.relationships == nil {
		dc.relationships = make(map[string]*discordgo.Relationship)
	}
	if dc.stopConnect == nil {
		dc.stopConnect = make(chan struct{})
	}
	dc.sessionLock.Unlock()

	go dc.startupTryConnect(0)
}

// startupTryConnect opens the gateway, retrying transient failures with
// exponential backoff (ported from legacy user.startupTryConnect). Gateway
// close codes that indicate bad credentials short-circuit to BadCredentials.
func (dc *DiscordClient) startupTryConnect(retryCount int) {
	dc.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	err := dc.connectInternal()
	if err == nil {
		return
	}
	dc.logger().Error().Err(err).Msg("Error connecting on startup")
	if isInvalidAuthCloseCode(err) {
		dc.onInvalidAuth()
		return
	}
	select {
	case <-dc.stopConnect:
		dc.logger().Debug().Msg("Connect cancelled, not retrying")
		return
	default:
	}
	if retryCount < maxStartupRetries {
		dc.userLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      "dc-unknown-websocket-error",
			Message:    err.Error(),
		})
		retryInSeconds := 2 << retryCount
		dc.logger().Debug().Int("retry_in_seconds", retryInSeconds).Msg("Sleeping and retrying connection")
		select {
		case <-time.After(time.Duration(retryInSeconds) * time.Second):
			dc.startupTryConnect(retryCount + 1)
		case <-dc.stopConnect:
			dc.logger().Debug().Msg("Connect cancelled during backoff")
		}
	} else {
		dc.userLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      "dc-unknown-websocket-error",
			Message:    err.Error(),
		})
	}
}

// connectInternal builds the discordgo session and opens the gateway. It
// blocks until Open() returns (success or terminal error). Ported from legacy
// user.Connect.
func (dc *DiscordClient) connectInternal() error {
	dc.sessionLock.Lock()
	defer dc.sessionLock.Unlock()

	// Clear the in-memory relationship cache; READY repopulates it (FR-12).
	dc.reconstructRelationships(nil)

	meta := dc.Meta()
	if meta.Token == "" {
		return bridgev2.ErrNotLoggedIn
	}

	dc.logger().Debug().Msg("Connecting to discord")

	session, err := discordgo.New(meta.Token)
	if err != nil {
		return err
	}

	// Reuse (or mint) the heartbeat session so the gateway can RESUME instead
	// of a full re-IDENTIFY (FR-6). The heartbeat session is persisted in
	// UserLoginMeta as JSON (GatewaySessionID) and saved whenever it changes.
	hbs := dc.loadHeartbeatSession()
	if hbs == nil || hbs.IsExpired() {
		dc.logger().Debug().Msg("Creating new heartbeat session")
		newSess := discordgo.NewHeartbeatSession()
		hbs = &newSess
	}
	hbs.BumpLastUsed()
	dc.storeHeartbeatSession(hbs)
	session.HeartbeatSession = *hbs

	if proxy := dc.connector.Config.Proxy; proxy != "" {
		u, parseErr := url.Parse(proxy)
		if parseErr != nil {
			dc.logger().Warn().Err(parseErr).Msg("Failed to parse proxy URL")
		} else {
			tlsConf := &tls.Config{
				InsecureSkipVerify: os.Getenv("DISCORD_SKIP_TLS_VERIFICATION") == "true",
			}
			session.Client.Transport = &http.Transport{
				Proxy:             http.ProxyURL(u),
				TLSClientConfig:   tlsConf,
				ForceAttemptHTTP2: true,
			}
			session.Dialer.Proxy = http.ProxyURL(u)
			session.Dialer.TLSClientConfig = tlsConf
		}
	}

	if os.Getenv("DISCORD_DEBUG") == "1" {
		session.LogLevel = discordgo.LogDebug
	} else {
		session.LogLevel = discordgo.LogInformational
	}
	gwLog := dc.logger().With().
		Str("component", "discordgo").
		Str("heartbeat_session", session.HeartbeatSession.ID.String()).
		Logger()
	session.Logger = func(msgL, caller int, format string, a ...interface{}) {
		gwLog.WithLevel(discordToZeroLevel(msgL)).Caller(caller+1).Msgf(strings.TrimSpace(format), a...) // zerolog-allow-msgf
	}

	// Select intents by token type (FR-8). discordgo decides bot vs user from
	// the token, but only bot sessions send an intents field. We also gate on
	// the stored TokenType so a misclassified token doesn't request privileged
	// intents it can't have.
	if !session.IsUser && dc.Meta().TokenType != TokenTypeUser {
		session.Identify.Intents = BotIntents
	}
	session.EventHandler = dc.eventHandlerSync

	if session.IsUser {
		if err = session.LoadMainPage(context.TODO()); err != nil {
			dc.logger().Warn().Err(err).Msg("Failed to load main page")
		}
	}

	dc.Session = session

	for {
		err = dc.Session.Open()
		if errors.Is(err, discordgo.ErrImmediateDisconnect) {
			dc.logger().Warn().Err(err).Msg("Retrying initial connection in 5 seconds")
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-dc.stopConnect:
				return errors.New("connect cancelled")
			}
		}
		return err
	}
}

// Disconnect closes the Discord gateway.
func (dc *DiscordClient) Disconnect() {
	dc.sessionLock.Lock()
	defer dc.sessionLock.Unlock()
	if dc.stopConnect != nil {
		close(dc.stopConnect)
		dc.stopConnect = nil
	}
	if dc.Session == nil {
		return
	}
	dc.logger().Info().Msg("Disconnecting session")
	dc.reconstructRelationships(nil)
	if err := dc.Session.Close(); err != nil {
		dc.logger().Warn().Err(err).Msg("Error closing session")
	}
	dc.Session = nil
}

// IsLoggedIn returns whether the cached token is non-empty (no I/O).
func (dc *DiscordClient) IsLoggedIn() bool {
	return dc.Meta().Token != ""
}

// LogoutRemote closes the gateway and clears the stored credentials.
func (dc *DiscordClient) LogoutRemote(ctx context.Context) {
	dc.bridgeStateLock.Lock()
	dc.wasLoggedOut = true
	dc.bridgeStateLock.Unlock()

	dc.Disconnect()

	meta := dc.Meta()
	meta.Token = ""
	meta.GatewaySessionID = ""
	meta.GatewaySequenceNum = 0
	meta.ReadStateVersion = 0
	meta.RelationshipsReady = false
	if err := dc.userLogin.Save(ctx); err != nil {
		dc.logger().Err(err).Msg("Failed to save user login after clearing token")
	}
}

// IsThisUser reports whether the given user ID is this login's user (FR-50).
func (dc *DiscordClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return discordid.UserIDToUserLoginID(userID) == dc.userLogin.ID
}

// GetChatInfo and GetUserInfo are implemented in chatinfo.go (Task 4.3).

// GetCapabilities returns the room features for a portal (per channel type).
func (dc *DiscordClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	// TODO(group2)
	return &event.RoomFeatures{}
}

// HandleMatrixMessage is implemented in handlematrix.go (Task 5.2).

// --- NetworkAPIWithUserID ---

// GetUserID returns the Discord snowflake of the logged-in user.
func (dc *DiscordClient) GetUserID() networkid.UserID {
	return networkid.UserID(dc.userLogin.ID)
}

// --- status.BridgeStateFiller ---

// FillBridgeState lets the connector annotate bridge-state pings with
// Discord-specific info. The framework already fills the generic fields via
// UserLogin.FillBridgeState; this hook covers connector-owned state.
func (dc *DiscordClient) FillBridgeState(state status.BridgeState) status.BridgeState {
	if state.Info == nil {
		state.Info = make(map[string]any)
	}
	dc.relationshipLock.RLock()
	state.Info["relationships_ready"] = dc.relationshipsReady
	dc.relationshipLock.RUnlock()
	return state
}

// --- gateway event dispatch + bridge-state helpers ---

// eventHandlerSync is the discordgo EventHandler entry point. Each event is
// dispatched on its own goroutine to keep the gateway read loop unblocked
// (ported from legacy user.eventHandlerSync). The connect/disconnect/ready
// lifecycle events that drive bridge state are handled here; the message-style
// events are forwarded to handlediscord.go (Group 4).
func (dc *DiscordClient) eventHandlerSync(rawEvt any) {
	go dc.handleDiscordEvent(rawEvt)
}

func (dc *DiscordClient) handleDiscordEvent(rawEvt any) {
	defer func() {
		if err := recover(); err != nil {
			dc.logger().Error().
				Any(zerolog.ErrorFieldName, err).
				Msg("Panic in Discord event handler")
		}
	}()
	switch evt := rawEvt.(type) {
	case *discordgo.Ready:
		dc.readyHandler(evt)
	case *discordgo.Resumed:
		dc.resumeHandler(evt)
	case *discordgo.Connect:
		dc.connectedHandler(evt)
	case *discordgo.Disconnect:
		dc.disconnectedHandler(evt)
	case *discordgo.InvalidAuth:
		dc.onInvalidAuth()
	case *discordgo.RelationshipAdd:
		dc.relationshipAddHandler(evt)
	case *discordgo.RelationshipRemove:
		dc.relationshipRemoveHandler(evt)
	case *discordgo.RelationshipUpdate:
		dc.relationshipUpdateHandler(evt)
	case *discordgo.InteractionSuccess:
		dc.interactionSuccessHandler(evt)
	default:
		// Message/edit/reaction/typing/channel/guild events translate to
		// RemoteEvents and are handled in handlediscord.go (Group 4).
		dc.dispatchRemoteEvent(rawEvt)
	}
}

// interactionSuccessHandler is called when Discord acknowledges a slash-command
// interaction (FR-54). It resolves the matching pendingInteraction and reacts
// with ✅ on the Matrix command event.
func (dc *DiscordClient) interactionSuccessHandler(s *discordgo.InteractionSuccess) {
	dc.pendingInteractionsLock.Lock()
	defer dc.pendingInteractionsLock.Unlock()
	pending, ok := dc.pendingInteractions[s.Nonce]
	if !ok {
		dc.logger().Debug().
			Str("nonce", s.Nonce).
			Str("interaction_id", s.ID).
			Msg("Got InteractionSuccess for unknown nonce")
		return
	}
	dc.logger().Debug().
		Str("nonce", s.Nonce).
		Str("interaction_id", s.ID).
		Msg("Got InteractionSuccess for pending exec command")
	pending.react("✅")
	delete(dc.pendingInteractions, s.Nonce)
}

// interactionPending holds the react callback for a pending exec command.
// Defined here so commands.go can use it without a circular import.
type interactionPending struct {
	// react sends a reaction to the command event.
	react func(key string)
}

// connectedHandler fires when the websocket connects (before READY).
func (dc *DiscordClient) connectedHandler(_ *discordgo.Connect) {
	dc.bridgeStateLock.Lock()
	defer dc.bridgeStateLock.Unlock()
	dc.logger().Debug().Msg("Connected to Discord")
	dc.wasDisconnected = false
}

// disconnectedHandler fires on an unexpected websocket disconnect; discordgo
// reconnects automatically, so this only updates bridge state.
func (dc *DiscordClient) disconnectedHandler(_ *discordgo.Disconnect) {
	dc.bridgeStateLock.Lock()
	defer dc.bridgeStateLock.Unlock()
	if dc.wasLoggedOut {
		dc.logger().Debug().Msg("Disconnected from Discord (suppressing bridge state, user was logged out)")
		return
	}
	dc.logger().Debug().Msg("Disconnected from Discord")
	dc.wasDisconnected = true
	dc.userLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateTransientDisconnect,
		Error:      "dc-transient-disconnect",
		Message:    "Temporarily disconnected from Discord, trying to reconnect",
	})
}

// resumeHandler fires when the gateway RESUMEs an existing session (FR-6).
func (dc *DiscordClient) resumeHandler(_ *discordgo.Resumed) {
	dc.logger().Debug().Msg("Discord connection resumed")
	dc.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
}

// readyHandler fires on a fresh IDENTIFY. It seeds the relationship cache and
// transitions to Connected. The portal/guild resync the legacy bridge did
// inline is emitted as RemoteEvents by Group 4; here we only own connection
// state. Duplicate-login takeover is handled by the login flow (FR-68), since
// the framework keys logins by Discord user ID via UserLogin.ID.
func (dc *DiscordClient) readyHandler(r *discordgo.Ready) {
	dc.logger().Debug().Msg("Discord connection ready")
	dc.bridgeStateLock.Lock()
	dc.wasLoggedOut = false
	dc.bridgeStateLock.Unlock()

	if r.User != nil && networkid.UserLoginID(r.User.ID) != dc.userLogin.ID {
		dc.logger().Warn().
			Str("ready_user_id", r.User.ID).
			Str("login_id", string(dc.userLogin.ID)).
			Msg("READY user ID doesn't match login ID")
	}

	dc.reconstructRelationships(r.Relationships)

	dc.userLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})

	// Forward READY to handlediscord so Group 4 can emit chat-resync events
	// for guilds and private channels.
	dc.dispatchReady(r)
}

// onInvalidAuth handles a 4004-style logout: clears credentials and reports
// BadCredentials. Ported from legacy invalidAuthHandler.
func (dc *DiscordClient) onInvalidAuth() {
	dc.bridgeStateLock.Lock()
	dc.wasLoggedOut = true
	dc.bridgeStateLock.Unlock()
	dc.logger().Info().Msg("Got logged out from Discord due to invalid token")
	dc.userLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      "dc-websocket-disconnect-4004",
		Message:    "Discord access token is no longer valid, please log in again",
	})
	go dc.userLogin.Logout(dc.br.BackgroundCtx)
}

// isInvalidAuthCloseCode reports whether the gateway close error indicates a
// terminal authentication failure that must not be retried (FR-7). Codes:
//
//	4004 authentication failed
//	4010 invalid shard
//	4011 sharding required
//	4012 invalid API version
//	4013 invalid intents
//	4014 disallowed intents
func isInvalidAuthCloseCode(err error) bool {
	closeErr := &websocket.CloseError{}
	if !errors.As(err, &closeErr) {
		return false
	}
	switch closeErr.Code {
	case 4004, 4010, 4011, 4012, 4013, 4014:
		return true
	default:
		return false
	}
}

// --- relationship cache (FR-12/65/66) ---

// reconstructRelationships replaces the in-memory friend cache. A nil slice
// clears it and marks it not-ready (e.g. on disconnect); a non-nil slice is
// the authoritative list from READY and marks the cache ready. The
// RelationshipsReady flag is mirrored into UserLoginMeta so it survives
// restarts. Ported from legacy user.reconstructRelationships.
func (dc *DiscordClient) reconstructRelationships(relationships []*discordgo.Relationship) {
	dc.relationshipLock.Lock()
	defer dc.relationshipLock.Unlock()

	if dc.relationships == nil {
		dc.relationships = make(map[string]*discordgo.Relationship)
	}
	clear(dc.relationships)
	if relationships == nil {
		dc.relationshipsReady = false
	} else {
		for _, relationship := range relationships {
			dc.relationships[relationship.ID] = relationship
		}
		dc.relationshipsReady = true
	}
	dc.persistRelationshipsReady()
}

func (dc *DiscordClient) relationshipAddHandler(r *discordgo.RelationshipAdd) {
	dc.relationshipLock.Lock()
	defer dc.relationshipLock.Unlock()
	dc.logger().Debug().Str("other_user_id", r.ID).Msg("Relationship added")
	dc.relationships[r.ID] = r.Relationship
}

func (dc *DiscordClient) relationshipUpdateHandler(r *discordgo.RelationshipUpdate) {
	dc.relationshipLock.Lock()
	defer dc.relationshipLock.Unlock()
	dc.logger().Debug().Str("other_user_id", r.ID).Msg("Relationship updated")
	dc.relationships[r.ID] = r.Relationship
}

func (dc *DiscordClient) relationshipRemoveHandler(r *discordgo.RelationshipRemove) {
	dc.relationshipLock.Lock()
	defer dc.relationshipLock.Unlock()
	dc.logger().Debug().Str("other_user_id", r.ID).Msg("Relationship removed")
	delete(dc.relationships, r.ID)
}

// GetRelationship returns the cached relationship for a Discord user ID, if any.
// Used by DM stranger gating in Group 4 (FR-65/66).
func (dc *DiscordClient) GetRelationship(userID string) (*discordgo.Relationship, bool) {
	dc.relationshipLock.RLock()
	defer dc.relationshipLock.RUnlock()
	rel, ok := dc.relationships[userID]
	return rel, ok
}

// RelationshipsReady reports whether the friend cache is authoritative.
func (dc *DiscordClient) RelationshipsReady() bool {
	dc.relationshipLock.RLock()
	defer dc.relationshipLock.RUnlock()
	return dc.relationshipsReady
}

// persistRelationshipsReady writes the ready flag into UserLoginMeta and saves
// if it changed. Caller must hold relationshipLock.
func (dc *DiscordClient) persistRelationshipsReady() {
	meta := dc.Meta()
	if meta.RelationshipsReady == dc.relationshipsReady {
		return
	}
	meta.RelationshipsReady = dc.relationshipsReady
	if err := dc.userLogin.Save(dc.br.BackgroundCtx); err != nil {
		dc.logger().Err(err).Msg("Failed to persist relationships-ready flag")
	}
}

// --- heartbeat session persistence (FR-6) ---

// loadHeartbeatSession decodes the heartbeat session stored as JSON in
// UserLoginMeta.GatewaySessionID. Returns nil if none is stored or it can't be
// decoded (a fresh one is then minted).
func (dc *DiscordClient) loadHeartbeatSession() *discordgo.HeartbeatSession {
	raw := dc.Meta().GatewaySessionID
	if raw == "" {
		return nil
	}
	var hbs discordgo.HeartbeatSession
	if err := json.Unmarshal([]byte(raw), &hbs); err != nil {
		dc.logger().Warn().Err(err).Msg("Failed to decode stored heartbeat session, creating a new one")
		return nil
	}
	return &hbs
}

// storeHeartbeatSession serializes the heartbeat session into UserLoginMeta and
// saves the login if it changed.
func (dc *DiscordClient) storeHeartbeatSession(hbs *discordgo.HeartbeatSession) {
	encoded, err := json.Marshal(hbs)
	if err != nil {
		dc.logger().Warn().Err(err).Msg("Failed to encode heartbeat session")
		return
	}
	meta := dc.Meta()
	if meta.GatewaySessionID == string(encoded) {
		return
	}
	meta.GatewaySessionID = string(encoded)
	if err := dc.userLogin.Save(dc.br.BackgroundCtx); err != nil {
		dc.logger().Err(err).Msg("Failed to persist heartbeat session")
	}
}

// discordToZeroLevel maps discordgo log levels to zerolog levels. Ported from
// legacy user.go.
func discordToZeroLevel(level int) zerolog.Level {
	switch level {
	case discordgo.LogError:
		return zerolog.ErrorLevel
	case discordgo.LogWarning:
		return zerolog.WarnLevel
	case discordgo.LogInformational:
		return zerolog.InfoLevel
	case discordgo.LogDebug:
		fallthrough
	default:
		return zerolog.DebugLevel
	}
}

// FetchMessages and GetBackfillMaxBatchCount are implemented in backfill.go.

// The Matrix→Discord handlers below are implemented in handlematrix.go (Group 5):
//   - EditHandlingNetworkAPI:      HandleMatrixEdit (Task 5.2)
//   - RedactionHandlingNetworkAPI: HandleMatrixMessageRemove (Task 5.2)
//   - ReactionHandlingNetworkAPI:  PreHandleMatrixReaction/HandleMatrixReaction/HandleMatrixReactionRemove (Task 5.3)
//   - ReadReceiptHandlingNetworkAPI: HandleMatrixReadReceipt (Task 5.3)
//   - TypingHandlingNetworkAPI:    HandleMatrixTyping (Task 5.3)
//   - RoomNameHandlingNetworkAPI:  HandleMatrixRoomName (Task 5.3, returns (bool, error) per ar H8)
//   - RoomTopicHandlingNetworkAPI: HandleMatrixRoomTopic (Task 5.3, returns (bool, error) per ar H8)

// ResolveIdentifier and CreateChatWithGhost are implemented in startchat.go (Task 6.4).

// Compile-time interface assertions — the forcing function for Group 1.
var (
	_ bridgev2.NetworkAPI                      = (*DiscordClient)(nil)
	_ bridgev2.NetworkAPIWithUserID            = (*DiscordClient)(nil)
	_ bridgev2.BackfillingNetworkAPI           = (*DiscordClient)(nil)
	_ bridgev2.BackfillingNetworkAPIWithLimits = (*DiscordClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI          = (*DiscordClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI      = (*DiscordClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI     = (*DiscordClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI   = (*DiscordClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI        = (*DiscordClient)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI      = (*DiscordClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI     = (*DiscordClient)(nil)
	_ bridgev2.IdentifierResolvingNetworkAPI   = (*DiscordClient)(nil)
	_ bridgev2.GhostDMCreatingNetworkAPI       = (*DiscordClient)(nil)
	_ status.BridgeStateFiller                 = (*DiscordClient)(nil)
)

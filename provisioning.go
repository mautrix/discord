package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
	"go.mau.fi/mautrix-discord/remoteauth"
)

const (
	SecWebSocketProtocol = "com.gitlab.beeper.discord"
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
	bridge *DiscordBridge
	log    log.Logger
}

func newProvisioningAPI(br *DiscordBridge) *ProvisioningAPI {
	p := &ProvisioningAPI{
		bridge: br,
		log:    br.Log.Sub("Provisioning"),
	}

	prefix := br.Config.Bridge.Provisioning.Prefix

	p.log.Debugln("Enabling provisioning API at", prefix)

	r := br.AS.Router.PathPrefix(prefix).Subrouter()

	r.Use(p.authMiddleware)

	r.HandleFunc("/v1/disconnect", p.disconnect).Methods(http.MethodPost)
	r.HandleFunc("/v1/ping", p.ping).Methods(http.MethodGet)
	r.HandleFunc("/v1/login/qr", p.qrLogin).Methods(http.MethodGet)
	r.HandleFunc("/v1/login/token", p.tokenLogin).Methods(http.MethodPost)
	r.HandleFunc("/v1/logout", p.logout).Methods(http.MethodPost)
	r.HandleFunc("/v1/reconnect", p.reconnect).Methods(http.MethodPost)

	r.HandleFunc("/v1/guilds", p.guildsList).Methods(http.MethodGet)
	r.HandleFunc("/v1/guilds/{guildID}", p.guildsBridge).Methods(http.MethodPost)
	r.HandleFunc("/v1/guilds/{guildID}", p.guildsUnbridge).Methods(http.MethodDelete)

	r.HandleFunc("/v1/delete-portals", p.deletePortals).Methods(http.MethodPost)

	if p.bridge.Config.Bridge.Provisioning.DebugEndpoints {
		p.log.Debugln("Enabling debug API at /debug")
		r := p.bridge.AS.Router.PathPrefix("/debug").Subrouter()
		r.Use(p.authMiddleware)
		r.PathPrefix("/pprof").Handler(http.DefaultServeMux)
	}

	return p
}

func jsonResponse(w http.ResponseWriter, status int, response interface{}) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

// Response structs
type Response struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

// Wrapped http.ResponseWriter to capture the status code
type responseWrap struct {
	http.ResponseWriter
	statusCode int
}

var _ http.Hijacker = (*responseWrap)(nil)

func (rw *responseWrap) WriteHeader(statusCode int) {
	rw.ResponseWriter.WriteHeader(statusCode)
	rw.statusCode = statusCode
}

func (rw *responseWrap) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// Middleware
func (p *ProvisioningAPI) authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		// Special case the login endpoint to use the discord qrcode auth
		if auth == "" && strings.HasSuffix(r.URL.Path, "/login") {
			authParts := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
			for _, part := range authParts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, SecWebSocketProtocol+"-") {
					auth = part[len(SecWebSocketProtocol+"-"):]

					break
				}
			}
		} else if strings.HasPrefix(auth, "Bearer ") {
			auth = auth[len("Bearer "):]
		}

		if auth != p.bridge.Config.Bridge.Provisioning.SharedSecret {
			jsonResponse(w, http.StatusUnauthorized, map[string]interface{}{
				"error":   "Invalid auth token",
				"errcode": mautrix.MUnknownToken.ErrCode,
			})

			return
		}

		userID := r.URL.Query().Get("user_id")
		user := p.bridge.GetUserByMXID(id.UserID(userID))

		start := time.Now()
		wWrap := &responseWrap{w, 200}
		h.ServeHTTP(wWrap, r.WithContext(context.WithValue(r.Context(), "user", user)))
		duration := time.Now().Sub(start).Seconds()

		p.log.Infofln("%s %s from %s took %.2f seconds and returned status %d", r.Method, r.URL.Path, user.MXID, duration, wWrap.statusCode)
	})
}

// websocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	Subprotocols: []string{SecWebSocketProtocol},
}

// Handlers
func (p *ProvisioningAPI) disconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	if !user.Connected() {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "You're not connected to discord",
			ErrCode: ErrCodeNotConnected,
		})
		return
	}

	if err := user.Disconnect(); err != nil {
		p.log.Errorfln("Failed to disconnect %s: %v", user.MXID, err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to disconnect from discord",
			ErrCode: ErrCodeDisconnectFailed,
		})
	} else {
		jsonResponse(w, http.StatusOK, Response{
			Success: true,
			Status:  "Disconnected from Discord",
		})
	}
}

type respPing struct {
	Discord struct {
		ID        string `json:"id,omitempty"`
		LoggedIn  bool   `json:"logged_in"`
		Connected bool   `json:"connected"`
		Conn      struct {
			LastHeartbeatAck  int64 `json:"last_heartbeat_ack,omitempty"`
			LastHeartbeatSent int64 `json:"last_heartbeat_sent,omitempty"`
		} `json:"conn"`
	}
	MXID           id.UserID `json:"mxid"`
	ManagementRoom id.RoomID `json:"management_room"`
}

func (p *ProvisioningAPI) ping(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	resp := respPing{
		MXID:           user.MXID,
		ManagementRoom: user.ManagementRoom,
	}
	resp.Discord.LoggedIn = user.IsLoggedIn()
	resp.Discord.Connected = user.Connected()
	resp.Discord.ID = user.DiscordID
	if user.Session != nil {
		resp.Discord.Conn.LastHeartbeatAck = user.Session.LastHeartbeatAck.UnixMilli()
		resp.Discord.Conn.LastHeartbeatSent = user.Session.LastHeartbeatSent.UnixMilli()
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (p *ProvisioningAPI) logout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	var msg string
	if user.DiscordID != "" {
		msg = "Logged out successfully."
	} else {
		msg = "User wasn't logged in."
	}
	user.Logout(false)
	jsonResponse(w, http.StatusOK, Response{true, msg})
}

func (p *ProvisioningAPI) qrLogin(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := p.bridge.GetUserByMXID(id.UserID(userID))

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.log.Errorln("Failed to upgrade connection to websocket:", err)
		return
	}

	log := p.log.Sub("QRLogin").Sub(user.MXID.String())

	defer func() {
		err := c.Close()
		if err != nil {
			log.Debugln("Error closing websocket:", err)
		}
	}()

	go func() {
		// Read everything so SetCloseHandler() works
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				break
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	c.SetCloseHandler(func(code int, text string) error {
		log.Debugfln("Login websocket closed (%d), cancelling login", code)
		cancel()
		return nil
	})

	if user.IsLoggedIn() {
		_ = c.WriteJSON(Error{
			Error:   "You're already logged into Discord",
			ErrCode: ErrCodeAlreadyLoggedIn,
		})
		return
	}

	client, err := remoteauth.New()
	if err != nil {
		log.Errorln("Failed to prepare login:", err)
		_ = c.WriteJSON(Error{
			Error:   "Failed to prepare login",
			ErrCode: ErrCodeLoginPrepareFailed,
		})
		return
	}

	qrChan := make(chan string)
	doneChan := make(chan struct{})

	log.Debugln("Started login via provisioning API")

	err = client.Dial(ctx, qrChan, doneChan)
	if err != nil {
		log.Errorln("Failed to connect to Discord login websocket:", err)
		close(qrChan)
		close(doneChan)
		_ = c.WriteJSON(Error{
			Error:   "Failed to connect to Discord login websocket",
			ErrCode: ErrCodeLoginConnectionFailed,
		})
		return
	}

	for {
		select {
		case qrCode, ok := <-qrChan:
			if !ok {
				continue
			}
			err = c.WriteJSON(map[string]interface{}{
				"code":    qrCode,
				"timeout": 120, // TODO: move this to the library or something
			})
			if err != nil {
				log.Errorln("Failed to write QR code to websocket:", err)
			}
		case <-doneChan:
			var discordUser remoteauth.User
			discordUser, err = client.Result()
			if err != nil {
				log.Errorln("Discord login websocket returned error:", err)
				_ = c.WriteJSON(Error{
					Error:   "Failed to log in",
					ErrCode: ErrCodeLoginFailed,
				})
				return
			}

			log.Infofln("Logged in as %s#%s (%s)", discordUser.Username, discordUser.Discriminator, discordUser.UserID)

			if err = user.Login(discordUser.Token); err != nil {
				log.Errorln("Failed to connect after logging in:", err)
				_ = c.WriteJSON(Error{
					Error:   "Failed to connect to Discord after logging in",
					ErrCode: ErrCodePostLoginConnFailed,
				})
				return
			}

			err = c.WriteJSON(respLogin{
				Success:       true,
				ID:            user.DiscordID,
				Username:      discordUser.Username,
				Discriminator: discordUser.Discriminator,
			})
			if err != nil {
				log.Errorln("Failed to write login success to websocket:", err)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

type respLogin struct {
	Success       bool   `json:"success"`
	ID            string `json:"id"`
	Username      string `json:"username"`
	Discriminator string `json:"discriminator"`
}

type reqTokenLogin struct {
	Token string `json:"token"`
}

func (p *ProvisioningAPI) tokenLogin(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := p.bridge.GetUserByMXID(id.UserID(userID))
	log := p.log.Sub("TokenLogin").Sub(user.MXID.String())
	if user.IsLoggedIn() {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "You're already logged into Discord",
			ErrCode: ErrCodeAlreadyLoggedIn,
		})
		return
	}
	var body reqTokenLogin
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Errorln("Failed to parse login request:", err)
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to parse request body",
			ErrCode: mautrix.MBadJSON.ErrCode,
		})
		return
	}
	if err := user.Login(body.Token); err != nil {
		log.Errorln("Failed to connect with provided token:", err)
		jsonResponse(w, http.StatusUnauthorized, Error{
			Error:   "Failed to connect to Discord",
			ErrCode: ErrCodePostLoginConnFailed,
		})
		return
	}
	log.Infoln("Successfully logged in")
	jsonResponse(w, http.StatusOK, respLogin{
		Success:       true,
		ID:            user.DiscordID,
		Username:      user.Session.State.User.Username,
		Discriminator: user.Session.State.User.Discriminator,
	})
}

func (p *ProvisioningAPI) reconnect(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	if user.Connected() {
		jsonResponse(w, http.StatusConflict, Error{
			Error:   "You're already connected to discord",
			ErrCode: ErrCodeAlreadyConnected,
		})

		return
	}

	if err := user.Connect(); err != nil {
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Failed to connect to discord",
			ErrCode: ErrCodeConnectFailed,
		})
	} else {
		jsonResponse(w, http.StatusOK, Response{
			Success: true,
			Status:  "Connected to Discord",
		})
	}
}

type guildEntry struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	AvatarURL    id.ContentURI `json:"avatar_url"`
	MXID         id.RoomID     `json:"mxid"`
	AutoBridge   bool          `json:"auto_bridge_channels"`
	BridgingMode string        `json:"bridging_mode"`
}

type respGuildsList struct {
	Guilds []guildEntry `json:"guilds"`
}

func (p *ProvisioningAPI) guildsList(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	var resp respGuildsList
	resp.Guilds = []guildEntry{}
	for _, userGuild := range user.GetPortals() {
		guild := p.bridge.GetGuildByID(userGuild.DiscordID, false)
		if guild == nil {
			continue
		}
		resp.Guilds = append(resp.Guilds, guildEntry{
			ID:           guild.ID,
			Name:         guild.PlainName,
			AvatarURL:    guild.AvatarURL,
			MXID:         guild.MXID,
			AutoBridge:   guild.BridgingMode == database.GuildBridgeEverything,
			BridgingMode: guild.BridgingMode.String(),
		})
	}

	jsonResponse(w, http.StatusOK, resp)
}

type reqBridgeGuild struct {
	AutoCreateChannels bool `json:"auto_create_channels"`
}

type respBridgeGuild struct {
	Success bool      `json:"success"`
	MXID    id.RoomID `json:"mxid"`
}

func (p *ProvisioningAPI) guildsBridge(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	guildID := mux.Vars(r)["guildID"]

	var body reqBridgeGuild
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		p.log.Errorln("Failed to parse bridge request:", err)
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to parse request body",
			ErrCode: mautrix.MBadJSON.ErrCode,
		})
		return
	}

	guild := user.bridge.GetGuildByID(guildID, false)
	if guild == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Guild not found",
			ErrCode: mautrix.MNotFound.ErrCode,
		})
		return
	}
	alreadyExists := guild.MXID == ""
	if err := user.bridgeGuild(guildID, body.AutoCreateChannels); err != nil {
		p.log.Errorfln("Error bridging %s: %v", guildID, err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal error while trying to bridge guild",
			ErrCode: ErrCodeGuildBridgeFailed,
		})
	} else if alreadyExists {
		jsonResponse(w, http.StatusOK, respBridgeGuild{
			Success: true,
			MXID:    guild.MXID,
		})
	} else {
		jsonResponse(w, http.StatusCreated, respBridgeGuild{
			Success: true,
			MXID:    guild.MXID,
		})
	}
}

func (p *ProvisioningAPI) guildsUnbridge(w http.ResponseWriter, r *http.Request) {
	guildID := mux.Vars(r)["guildID"]
	user := r.Context().Value("user").(*User)
	if user.PermissionLevel < bridgeconfig.PermissionLevelAdmin {
		jsonResponse(w, http.StatusForbidden, Error{
			Error:   "Only bridge admins can unbridge guilds",
			ErrCode: mautrix.MForbidden.ErrCode,
		})
	} else if guild := user.bridge.GetGuildByID(guildID, false); guild == nil {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "Guild not found",
			ErrCode: mautrix.MNotFound.ErrCode,
		})
	} else if guild.BridgingMode == database.GuildBridgeNothing && guild.MXID == "" {
		jsonResponse(w, http.StatusNotFound, Error{
			Error:   "That guild is not bridged",
			ErrCode: ErrCodeGuildNotBridged,
		})
	} else if err := user.unbridgeGuild(guildID); err != nil {
		p.log.Errorfln("Error unbridging %s: %v", guildID, err)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal error while trying to unbridge guild",
			ErrCode: ErrCodeGuildUnbridgeFailed,
		})
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

type reqDeletePortals struct {
	DiscordID string `json:"discord_id"`
}

type respDeletePortals struct {
	Success     bool   `json:"success"`
	PortalCount int    `json:"portal_count"`
	GuildCount  int    `json:"guild_count"`
	DiscordID   string `json:"discord_id"`
}

func (p *ProvisioningAPI) deletePortals(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	var body reqDeletePortals
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		p.log.Errorln("Failed to parse delete-portals request:", err)
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "Failed to parse request body",
			ErrCode: mautrix.MBadJSON.ErrCode,
		})
		return
	}

	discordID := user.DiscordID
	if discordID == "" {
		discordID = body.DiscordID
	}
	if discordID == "" {
		jsonResponse(w, http.StatusBadRequest, Error{
			Error:   "User is not logged in and no discord_id provided in request body",
			ErrCode: mautrix.MBadJSON.ErrCode,
		})
		return
	}

	var portalCount, guildCount int
	var panicErr interface{}
	func() {
		defer func() { panicErr = recover() }()
		portalCount, guildCount = user.deleteAllPortals(discordID)
	}()
	if panicErr != nil {
		p.log.Errorfln("Panic during deletePortals for %s/%s: %v", user.MXID, discordID, panicErr)
		jsonResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal error while deleting portals",
			ErrCode: "M_UNKNOWN",
		})
		return
	}

	p.log.Infofln("Deleted %d portals and %d guilds for %s (discord: %s)", portalCount, guildCount, user.MXID, discordID)
	jsonResponse(w, http.StatusOK, respDeletePortals{
		Success:     true,
		PortalCount: portalCount,
		GuildCount:  guildCount,
		DiscordID:   discordID,
	})
}

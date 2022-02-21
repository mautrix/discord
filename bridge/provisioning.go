package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/remoteauth"
)

const (
	SecWebSocketProtocol = "com.gitlab.beeper.discord"
)

type ProvisioningAPI struct {
	bridge *Bridge
	log    log.Logger
}

func newProvisioningAPI(bridge *Bridge) *ProvisioningAPI {
	p := &ProvisioningAPI{
		bridge: bridge,
		log:    bridge.log.Sub("Provisioning"),
	}

	prefix := bridge.Config.Appservice.Provisioning.Prefix

	p.log.Debugln("Enabling provisioning API at", prefix)

	r := bridge.as.Router.PathPrefix(prefix).Subrouter()

	r.Use(p.authMiddleware)

	r.HandleFunc("/ping", p.Ping).Methods(http.MethodGet)
	r.HandleFunc("/login", p.Login).Methods(http.MethodGet)
	r.HandleFunc("/logout", p.Logout).Methods(http.MethodPost)

	return p
}

func jsonResponse(w http.ResponseWriter, status int, response interface{}) {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
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

		if auth != p.bridge.Config.Appservice.Provisioning.SharedSecret {
			jsonResponse(w, http.StatusForbidden, map[string]interface{}{
				"error":   "Invalid auth token",
				"errcode": "M_FORBIDDEN",
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
func (p *ProvisioningAPI) Ping(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)

	discord := map[string]interface{}{
		"has_session":     user.Session != nil,
		"management_room": user.ManagementRoom,
		"conn":            nil,
	}

	if user.ID != "" {
		discord["id"] = user.ID
	}

	if user.Session != nil {
		discord["conn"] = map[string]interface{}{
			"last_heartbeat_ack":  user.Session.LastHeartbeatAck,
			"last_heartbeat_sent": user.Session.LastHeartbeatSent,
		}
	}

	resp := map[string]interface{}{
		"mxid":    user.MXID,
		"discord": discord,
	}

	jsonResponse(w, http.StatusOK, resp)
}

func (p *ProvisioningAPI) Logout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user").(*User)
	force := strings.ToLower(r.URL.Query().Get("force")) != "false"

	if user.Session == nil {
		if force {
			jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})
		} else {
			jsonResponse(w, http.StatusNotFound, Error{
				Error:   "You're not logged in",
				ErrCode: "not logged in",
			})
		}

		return
	}

	err := user.DeleteSession()
	if err != nil {
		user.log.Warnln("Error while logging out:", err)

		if !force {
			jsonResponse(w, http.StatusInternalServerError, Error{
				Error:   fmt.Sprintf("Unknown error while logging out: %v", err),
				ErrCode: err.Error(),
			})

			return
		}
	}

	jsonResponse(w, http.StatusOK, Response{true, "Logged out successfully."})
}

func (p *ProvisioningAPI) Login(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	user := p.bridge.GetUserByMXID(id.UserID(userID))

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.log.Errorln("Failed to upgrade connection to websocket:", err)
		return
	}

	defer func() {
		err := c.Close()
		if err != nil {
			user.log.Debugln("Error closing websocket:", err)
		}
	}()

	go func() {
		// Read everything so SetCloseHandler() works
		for {
			_, _, err = c.ReadMessage()
			if err != nil {
				break
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	c.SetCloseHandler(func(code int, text string) error {
		user.log.Debugfln("Login websocket closed (%d), cancelling login", code)

		cancel()

		return nil
	})

	client, err := remoteauth.New()
	if err != nil {
		user.log.Errorf("Failed to log in from provisioning API:", err)

		c.WriteJSON(Error{
			Error:   "Failed to connect to Discord",
			ErrCode: "connection error",
		})
	}

	qrChan := make(chan string)
	doneChan := make(chan struct{})

	user.log.Debugln("Started login via provisioning API")

	err = client.Dial(ctx, qrChan, doneChan)
	if err != nil {
		close(qrChan)
		close(doneChan)
	}

	for {
		select {
		case qrCode, ok := <-qrChan:
			if !ok {
				continue
			}
			c.WriteJSON(map[string]interface{}{
				"code":    qrCode,
				"timeout": 120, // TODO: move this to the library or something
			})
		case <-doneChan:
			discordUser, err := client.Result()
			if err != nil {
				c.WriteJSON(Error{
					Error:   "Failed to connect to Discord",
					ErrCode: "connection error",
				})

				p.log.Errorfln("failed to login via qrcode:", err)

				return
			}

			if err := user.Login(discordUser.Token); err != nil {
				c.WriteJSON(Error{
					Error:   "Failed to connect to Discord",
					ErrCode: "connection error",
				})

				p.log.Errorfln("failed to login via qrcode:", err)

				return
			}

			user.ID = discordUser.UserID
			user.Update()

			c.WriteJSON(map[string]interface{}{
				"success": true,
				"id":      user.ID,
			})

			return
		case <-ctx.Done():
			return
		}
	}
}

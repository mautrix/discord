// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright 2015-2016 Bruce Marriner <bruce@sqls.net>.  All rights reserved.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This file is derived from discordgo (https://github.com/bwmarrin/discordgo),
// used under the BSD-3-Clause license; see README.md in this directory. This
// file is distributed under the GNU AGPLv3 as follows:
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

// This file contains low level functions for interacting with the Discord
// data websocket interface.

package meowcord

import (
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// ErrWSAlreadyOpen is thrown when you attempt to open
// a websocket that already is open.
var ErrWSAlreadyOpen = errors.New("web socket already opened")

// ErrWSNotFound is thrown when you attempt to use a websocket
// that doesn't exist
var ErrWSNotFound = errors.New("no websocket connection exists")

// ErrWSShardBounds is thrown when you try to use a shard ID that is
// more than the total shard count
var ErrWSShardBounds = errors.New("ShardID must be less than ShardCount")

type resumePacket struct {
	Op   int `json:"op"`
	Data struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		Sequence  int64  `json:"seq"`
	} `json:"d"`
}

func (s *Session) closeZLib() {
	if s.zlibReader != nil {
		_ = s.zlibReader.Close()
	}
	if s.zlibReader != nil {
		_ = s.zlibPipeReader.Close()
		_ = s.zlibPipeWriter.Close()
	}
	s.zlibJSON = nil
	s.zlibPipeReader = nil
	s.zlibPipeWriter = nil
	s.zlibReader = nil
}

// Open creates a websocket connection to Discord.
// See: https://discord.com/developers/docs/topics/gateway#connecting
func (s *Session) Open() error {
	s.log(LogInformational, "called")

	var err error

	// Prevent Open or other major Session functions from
	// being called while Open is still running.
	s.Lock()
	defer s.Unlock()

	// If the websock is already open, bail out here.
	if s.wsConn != nil {
		return ErrWSAlreadyOpen
	}

	sequence := atomic.LoadInt64(s.sequence)

	var gateway string
	// Get the gateway to use for the Websocket connection
	if sequence != 0 && s.sessionID != "" && s.resumeGatewayURL != "" {
		s.log(LogDebug, "using resume gateway %s", s.resumeGatewayURL)
		gateway = s.resumeGatewayURL
	} else {
		if s.gateway == "" {
			s.gateway, err = s.Gateway()
			if err != nil {
				return err
			}
		}

		gateway = s.gateway
	}

	s.zlibPipeReader, s.zlibPipeWriter = io.Pipe()

	// Add the version and encoding to the URL
	gateway += "?v=" + APIVersion + "&encoding=json&compress=zlib-stream"

	// Connect to the Gateway
	s.log(LogInformational, "connecting to gateway %s", gateway)
	header := http.Header{}
	if s.IsUser {
		for k, v := range DroidWSHeaders {
			header.Add(k, v)
		}
	} else {
		header.Add("accept-encoding", "zlib")
	}

	dialCtx := context.Background()
	if s.GatewayDialTimeout > 0 {
		var cancelDial context.CancelFunc
		dialCtx, cancelDial = context.WithTimeout(dialCtx, s.GatewayDialTimeout)
		defer cancelDial()
	}

	s.wsConn, _, err = websocket.Dial(dialCtx, gateway, &websocket.DialOptions{
		HTTPClient: s.GatewayHTTPClient,
		HTTPHeader: header,
		// Discord uses its own app-level compression (e.g. zlib-stream).
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.log(LogError, "error connecting to gateway %s, %s", s.gateway, err)
		if !s.noClearGateway {
			s.gateway = "" // clear cached gateway
		}
		s.wsConn = nil // Just to be safe.
		s.closeZLib()
		return err
	}

	// coder/websocket defaults to a 32 KiB read limit, which is far too small
	// for Discord's READY messages, which can be massive... disable the limit
	// entirely.
	s.wsConn.SetReadLimit(-1)

	s.wsConnCtx, s.wsConnCancel = context.WithCancel(context.Background())

	defer func() {
		// because of this, all code below must set err to the error
		// when exiting with an error :)  Maybe someone has a better
		// way :)
		if err != nil {
			s.wsConnCancel()
			s.wsConn.CloseNow()
			s.wsConn = nil
			s.wsConnCtx = nil
			s.wsConnCancel = nil
			s.closeZLib()
		}
	}()

	// The first response from Discord should be an Op 10 (Hello) Packet.
	// When processed by onEvent the heartbeat goroutine will be started.
	mt, m, err := s.wsConn.Read(s.wsConnCtx)
	if err != nil {
		return err
	}
	e, err := s.onEvent(mt, m, true)
	if err != nil {
		return err
	}
	if e.Operation != 10 {
		err = fmt.Errorf("expecting Op 10, got Op %d instead", e.Operation)
		return err
	}
	s.log(LogInformational, "Op 10 Hello Packet received from Discord")
	s.LastHeartbeatAck = time.Now().UTC()
	var h helloOp
	if err = json.Unmarshal(e.RawData, &h); err != nil {
		err = fmt.Errorf("error unmarshalling helloOp, %s", err)
		return err
	}

	// Now we send either an Op 2 Identity if this is a brand new
	// connection or Op 6 Resume if we are resuming an existing connection.
	if s.sessionID == "" && sequence == 0 {

		// Send Op 2 Identity Packet
		err = s.identify()
		if err != nil {
			err = fmt.Errorf("error sending identify packet to gateway, %s, %s", s.gateway, err)
			return err
		}

	} else {

		// Send Op 6 Resume Packet
		p := resumePacket{}
		p.Op = 6
		p.Data.Token = s.Token
		p.Data.SessionID = s.sessionID
		p.Data.Sequence = sequence

		s.log(LogInformational, "sending resume packet to gateway")
		s.wsMutex.Lock()
		err = wsjson.Write(s.wsConnCtx, s.wsConn, p)
		s.wsMutex.Unlock()
		if err != nil {
			err = fmt.Errorf("error sending gateway resume packet, %s, %s", s.gateway, err)
			return err
		}

	}

	// A basic state is a hard requirement for Voice.
	// We create it here so the below READY/RESUMED packet can populate
	// the state :)
	// XXX: Move to New() func?
	if s.State == nil {
		state := NewState()
		state.TrackChannels = false
		state.TrackEmojis = false
		state.TrackMembers = false
		state.TrackRoles = false
		state.TrackVoice = false
		s.State = state
	}

	// Now Discord should send us a READY or RESUMED packet.
	mt, m, err = s.wsConn.Read(s.wsConnCtx)
	if err != nil {
		return err
	}
	e, err = s.onEvent(mt, m, true)
	if err != nil {
		return err
	}
	if s.IsUser && e.Type == "READY" {
		s.wsMutex.Lock()
		err := wsjson.Write(s.wsConnCtx, s.wsConn,
			updateTimeSpentSessionOp{
				Op: 41,
				Data: updateTimeSpentSessionData{
					InitializationTimestamp: s.HeartbeatSession.LastUsedTimestamp.UnixMilli(),
					SessionID:               s.HeartbeatSession.ID,
					ClientLaunchID:          s.launchID,
				},
			})
		s.wsMutex.Unlock()
		if err != nil {
			s.log(LogError, "Failed to send UPDATE_TIME_SPENT_SESSION_ID, continuing: %v", err)
		}
	}
	if e.Type != `READY` && e.Type != `RESUMED` {
		// This is not fatal, but it does not follow their API documentation.
		s.log(LogWarning, "Expected READY/RESUMED, instead got:\n%#v\n", e)
	}
	//s.log(LogInformational, "First Packet:\n%#v\n", e)

	s.log(LogInformational, "We are now connected to Discord, emitting connect event")
	s.handleEvent(connectEventType, &Connect{})

	// A VoiceConnections map is a hard requirement for Voice.
	// XXX: can this be moved to when opening a voice connection?
	if s.VoiceConnections == nil {
		s.log(LogInformational, "creating new VoiceConnections map")
		s.VoiceConnections = make(map[string]*VoiceConnection)
	}

	// Create listening chan outside of listen, as it needs to happen inside the
	// mutex lock and needs to exist before calling heartbeat and listen
	// go rountines.
	s.listening = make(chan interface{})

	// Start sending heartbeats and reading messages from Discord. Capture a
	// fresh context for each goroutine.
	go s.heartbeat(s.wsConnCtx, s.wsConn, s.listening, h.HeartbeatInterval)
	go s.listen(s.wsConnCtx, s.wsConn, s.listening)

	s.log(LogInformational, "exiting")
	return nil
}

// listen polls the websocket connection for events, it will stop when the
// listening channel is closed, or an error occurs.
func (s *Session) listen(ctx context.Context, wsConn *websocket.Conn, listening <-chan interface{}) {

	s.log(LogInformational, "called")

	for {

		messageType, message, err := wsConn.Read(ctx)

		if err != nil {

			// Detect if we have been closed manually. If a Close() has already
			// happened, the websocket we are listening on will be different to
			// the current session.
			s.RLock()
			sameConnection := s.wsConn == wsConn
			s.RUnlock()

			if sameConnection {

				s.log(LogWarning, "error reading from gateway %s websocket, %s", s.gateway, err)

				closeCode := websocket.CloseStatus(err)

				// There has been an error reading, close the websocket so that
				// OnDisconnect event is emitted.
				err := s.Close()
				if err != nil {
					s.log(LogWarning, "error closing session connection, %s", err)
				}

				switch closeCode {
				case 4004:
					s.log(LogInformational, "emit invalid auth event")
					s.handleEvent(invalidAuthEventType, &InvalidAuth{})
					return
				}

				s.log(LogInformational, "calling reconnect() now")
				s.reconnect()
			}

			return
		}

		select {

		case <-listening:
			return

		default:
			s.onEvent(messageType, message, false)

		}
	}
}

type heartbeatOp struct {
	Op   int   `json:"op"`
	Data int64 `json:"d"`
}

func (s *Session) newHeartbeatOp(seq int64) interface{} {
	if s.IsUser {
		return newForegroundedQosHeartbeatOp(seq)
	}
	return heartbeatOp{Op: 1, Data: seq}
}

func newForegroundedQosHeartbeatOp(seq int64) qosHeartbeatOp {
	return qosHeartbeatOp{
		Op: 40,
		Data: qosHeartbeatData{
			Seq: seq,
			Qos: qos{
				Active:  true,
				Ver:     28,
				Reasons: []string{"foregrounded"},
			},
		},
	}
}

type qos struct {
	Active  bool     `json:"active"`
	Ver     int      `json:"ver"`
	Reasons []string `json:"reasons"`
}

type qosHeartbeatData struct {
	Seq int64 `json:"seq"`
	Qos qos   `json:"qos"`
}

type qosHeartbeatOp struct {
	Op   int              `json:"op"`
	Data qosHeartbeatData `json:"d"`
}

type updateTimeSpentSessionData struct {
	InitializationTimestamp int64     `json:"initialization_timestamp"`
	SessionID               uuid.UUID `json:"session_id"`
	ClientLaunchID          uuid.UUID `json:"client_launch_id"`
}

type updateTimeSpentSessionOp struct {
	Op   int                        `json:"op"`
	Data updateTimeSpentSessionData `json:"d"`
}

type helloOp struct {
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`
}

// FailedHeartbeatAcks is the Number of heartbeat intervals to wait until forcing a connection restart.
const FailedHeartbeatAcks time.Duration = 5 * time.Millisecond

// HeartbeatLatency returns the latency between heartbeat acknowledgement and heartbeat send.
func (s *Session) HeartbeatLatency() time.Duration {

	return s.LastHeartbeatAck.Sub(s.LastHeartbeatSent)

}

// heartbeat sends regular heartbeats to Discord so it knows the client
// is still connected.  If you do not send these heartbeats Discord will
// disconnect the websocket connection after a few seconds.
func (s *Session) heartbeat(ctx context.Context, wsConn *websocket.Conn, listening <-chan interface{}, heartbeatInterval time.Duration) {

	s.log(LogInformational, "called")

	if listening == nil || wsConn == nil {
		return
	}

	var err error
	ticker := time.NewTicker(heartbeatInterval * time.Millisecond)
	defer ticker.Stop()

	for {
		s.RLock()
		last := s.LastHeartbeatAck
		s.RUnlock()
		sequence := atomic.LoadInt64(s.sequence)
		s.log(LogDebug, "sending gateway websocket heartbeat seq %d", sequence)
		s.wsMutex.Lock()
		s.LastHeartbeatSent = time.Now().UTC()
		err = wsjson.Write(ctx, wsConn, s.newHeartbeatOp(sequence))
		s.wsMutex.Unlock()
		if err != nil || time.Now().UTC().Sub(last) > (heartbeatInterval*FailedHeartbeatAcks) {
			if err != nil {
				s.log(LogError, "error sending heartbeat to gateway %s, %s", s.gateway, err)
			} else {
				s.log(LogError, "haven't gotten a heartbeat ACK in %v, triggering a reconnection", time.Now().UTC().Sub(last))
			}
			s.Close()
			s.reconnect()
			return
		}
		s.Lock()
		s.DataReady = true
		s.Unlock()

		select {
		case <-ticker.C:
			// continue loop and send heartbeat
		case <-listening:
			return
		}
	}
}

// UpdateStatusData is provided to UpdateStatusComplex()
type UpdateStatusData struct {
	IdleSince  *int        `json:"since"`
	Activities []*Activity `json:"activities"`
	AFK        bool        `json:"afk"`
	Status     string      `json:"status"`
}

type updateStatusOp struct {
	Op   int              `json:"op"`
	Data UpdateStatusData `json:"d"`
}

func newUpdateStatusData(idle int, activityType ActivityType, name, url string) *UpdateStatusData {
	usd := &UpdateStatusData{
		Status: "online",
	}

	if idle > 0 {
		usd.IdleSince = &idle
	}

	if name != "" {
		usd.Activities = []*Activity{{
			Name: name,
			Type: activityType,
			URL:  url,
		}}
	}

	return usd
}

// UpdateGameStatus is used to update the user's status.
// If idle>0 then set status to idle.
// If name!="" then set game.
// if otherwise, set status to active, and no activity.
func (s *Session) UpdateGameStatus(idle int, name string) (err error) {
	return s.UpdateStatusComplex(*newUpdateStatusData(idle, ActivityTypeGame, name, ""))
}

// UpdateWatchStatus is used to update the user's watch status.
// If idle>0 then set status to idle.
// If name!="" then set movie/stream.
// if otherwise, set status to active, and no activity.
func (s *Session) UpdateWatchStatus(idle int, name string) (err error) {
	return s.UpdateStatusComplex(*newUpdateStatusData(idle, ActivityTypeWatching, name, ""))
}

// UpdateStreamingStatus is used to update the user's streaming status.
// If idle>0 then set status to idle.
// If name!="" then set game.
// If name!="" and url!="" then set the status type to streaming with the URL set.
// if otherwise, set status to active, and no game.
func (s *Session) UpdateStreamingStatus(idle int, name string, url string) (err error) {
	gameType := ActivityTypeGame
	if url != "" {
		gameType = ActivityTypeStreaming
	}
	return s.UpdateStatusComplex(*newUpdateStatusData(idle, gameType, name, url))
}

// UpdateListeningStatus is used to set the user to "Listening to..."
// If name!="" then set to what user is listening to
// Else, set user to active and no activity.
func (s *Session) UpdateListeningStatus(name string) (err error) {
	return s.UpdateStatusComplex(*newUpdateStatusData(0, ActivityTypeListening, name, ""))
}

// UpdateCustomStatus is used to update the user's custom status.
// If state!="" then set the custom status.
// Else, set user to active and remove the custom status.
func (s *Session) UpdateCustomStatus(state string) (err error) {
	data := UpdateStatusData{
		Status: "online",
	}

	if state != "" {
		// Discord requires a non-empty activity name, therefore we provide "Custom Status" as a placeholder.
		data.Activities = []*Activity{{
			Name:  "Custom Status",
			Type:  ActivityTypeCustom,
			State: state,
		}}
	}

	return s.UpdateStatusComplex(data)
}

// UpdateStatusComplex allows for sending the raw status update data untouched by discordgo.
func (s *Session) UpdateStatusComplex(usd UpdateStatusData) (err error) {
	// The comment does say "untouched by discordgo", but we might need to lie a bit here.
	// The Discord documentation lists `activities` as being nullable, but in practice this
	// doesn't seem to be the case. I had filed an issue about this at
	// https://github.com/discord/discord-api-docs/issues/2559, but as of writing this
	// haven't had any movement on it, so at this point I'm assuming this is an error,
	// and am fixing this bug accordingly. Because sending `null` for `activities` instantly
	// disconnects us, I think that disallowing it from being sent in `UpdateStatusComplex`
	// isn't that big of an issue.
	if usd.Activities == nil {
		usd.Activities = make([]*Activity, 0)
	}

	s.RLock()
	defer s.RUnlock()
	if s.wsConn == nil {
		return ErrWSNotFound
	}

	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, updateStatusOp{3, usd})
	s.wsMutex.Unlock()

	return
}

type requestGuildMembersData struct {
	// TODO: Deprecated. Use string instead of []string
	GuildIDs  []string  `json:"guild_id"`
	Query     *string   `json:"query,omitempty"`
	UserIDs   *[]string `json:"user_ids,omitempty"`
	Limit     int       `json:"limit"`
	Nonce     string    `json:"nonce,omitempty"`
	Presences bool      `json:"presences"`
}

type requestGuildMembersOp struct {
	Op   int                     `json:"op"`
	Data requestGuildMembersData `json:"d"`
}

type markViewingData struct {
	ChannelID string `json:"channel_id"`
}

type markViewingOp struct {
	Op   int             `json:"op"`
	Data markViewingData `json:"d"`
}

type GuildSubscribeData struct {
	GuildID           string             `json:"guild_id"`
	Channels          map[string][][]int `json:"channels,omitempty"`
	Typing            bool               `json:"typing,omitempty"`
	Activities        bool               `json:"activities,omitempty"`
	Threads           bool               `json:"threads,omitempty"`
	Members           []string           `json:"members,omitempty"`
	ThreadMemberLists []string           `json:"thread_member_lists,omitempty"`
}

type guildSubscribeOp struct {
	Op   int                `json:"op"`
	Data GuildSubscribeData `json:"d"`
}

// RequestGuildMembers requests guild members from the gateway
// The gateway responds with GuildMembersChunk events
// guildID   : Single Guild ID to request members of
// query     : String that username starts with, leave empty to return all members
// limit     : Max number of items to return, or 0 to request all members matched
// nonce     : Nonce to identify the Guild Members Chunk response
// presences : Whether to request presences of guild members
func (s *Session) RequestGuildMembers(guildID, query string, limit int, nonce string, presences bool) error {
	return s.RequestGuildMembersBatch([]string{guildID}, query, limit, nonce, presences)
}

// RequestGuildMembersList requests guild members from the gateway
// The gateway responds with GuildMembersChunk events
// guildID   : Single Guild ID to request members of
// userIDs   : IDs of users to fetch
// limit     : Max number of items to return, or 0 to request all members matched
// nonce     : Nonce to identify the Guild Members Chunk response
// presences : Whether to request presences of guild members
func (s *Session) RequestGuildMembersList(guildID string, userIDs []string, limit int, nonce string, presences bool) error {
	return s.RequestGuildMembersBatchList([]string{guildID}, userIDs, limit, nonce, presences)
}

// RequestGuildMembersBatch requests guild members from the gateway
// The gateway responds with GuildMembersChunk events
// guildID   : Slice of guild IDs to request members of
// query     : String that username starts with, leave empty to return all members
// limit     : Max number of items to return, or 0 to request all members matched
// nonce     : Nonce to identify the Guild Members Chunk response
// presences : Whether to request presences of guild members
//
// NOTE: this function is deprecated, please use RequestGuildMembers instead
func (s *Session) RequestGuildMembersBatch(guildIDs []string, query string, limit int, nonce string, presences bool) (err error) {
	data := requestGuildMembersData{
		GuildIDs:  guildIDs,
		Query:     &query,
		Limit:     limit,
		Nonce:     nonce,
		Presences: presences,
	}
	err = s.requestGuildMembers(data)
	return
}

// RequestGuildMembersBatchList requests guild members from the gateway
// The gateway responds with GuildMembersChunk events
// guildID   : Slice of guild IDs to request members of
// userIDs   : IDs of users to fetch
// limit     : Max number of items to return, or 0 to request all members matched
// nonce     : Nonce to identify the Guild Members Chunk response
// presences : Whether to request presences of guild members
//
// NOTE: this function is deprecated, please use RequestGuildMembersList instead
func (s *Session) RequestGuildMembersBatchList(guildIDs []string, userIDs []string, limit int, nonce string, presences bool) (err error) {
	data := requestGuildMembersData{
		GuildIDs:  guildIDs,
		UserIDs:   &userIDs,
		Limit:     limit,
		Nonce:     nonce,
		Presences: presences,
	}
	err = s.requestGuildMembers(data)
	return
}

// GatewayWriteStruct allows for sending raw gateway structs over the gateway.
func (s *Session) GatewayWriteStruct(data interface{}) (err error) {
	s.RLock()
	defer s.RUnlock()
	if s.wsConn == nil {
		return ErrWSNotFound
	}

	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, data)
	s.wsMutex.Unlock()

	return err
}

func (s *Session) requestGuildMembers(data requestGuildMembersData) (err error) {
	s.log(LogInformational, "called")

	s.RLock()
	defer s.RUnlock()
	if s.wsConn == nil {
		return ErrWSNotFound
	}

	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, requestGuildMembersOp{8, data})
	s.wsMutex.Unlock()

	return
}

func (s *Session) MarkViewing(channelID string) (err error) {
	if !s.IsUser {
		s.log(LogWarning, "ignoring call")
		return
	}
	s.log(LogInformational, "called")

	s.RLock()
	defer s.RUnlock()
	if s.wsConn == nil {
		return ErrWSNotFound
	}

	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, markViewingOp{13, markViewingData{channelID}})
	s.wsMutex.Unlock()

	return
}

func (s *Session) SubscribeGuild(dat GuildSubscribeData) (err error) {
	if !s.IsUser {
		s.log(LogWarning, "ignoring call")
		return
	}
	s.log(LogInformational, "called")

	s.RLock()
	defer s.RUnlock()
	if s.wsConn == nil {
		return ErrWSNotFound
	}

	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, guildSubscribeOp{14, dat})
	s.wsMutex.Unlock()

	return
}

// onEvent is the "event handler" for all messages received on the
// Discord Gateway API websocket connection.
//
// If you use the AddHandler() function to register a handler for a
// specific event this function will pass the event along to that handler.
//
// If you use the AddHandler() function to register a handler for the
// "OnEvent" event then all events will be passed to that handler.
func (s *Session) onEvent(messageType websocket.MessageType, message []byte, isOnConnect bool) (*Event, error) {
	var err error

	// Decode the event into an Event struct.
	var e *Event

	// If this is a compressed message, uncompress it.
	if messageType == websocket.MessageBinary {
		go func() {
			_, innerErr := s.zlibPipeWriter.Write(message)
			if innerErr != nil {
				s.log(LogError, "error writing websocket message to zlib pipe: %v", innerErr)
			}
		}()

		if s.zlibReader == nil {
			s.zlibReader, err = zlib.NewReader(s.zlibPipeReader)
			if err != nil {
				s.log(LogError, "error preparing zlib reader: %v", err)
				s.zlibReader = nil
				return nil, err
			}
			s.zlibJSON = json.NewDecoder(s.zlibReader)
		}

		if err = s.zlibJSON.Decode(&e); err != nil {
			s.log(LogError, "error decoding websocket message, %s", err)
			return e, err
		}
	} else {
		if err = json.Unmarshal(message, &e); err != nil {
			s.log(LogError, "error decoding websocket message, %s", err)
			return e, err
		}
	}

	s.log(LogDebug, "Op: %d, Seq: %d, Type: %s, Data: %s\n\n", e.Operation, e.Sequence, e.Type, string(e.RawData))

	// Ping request.
	// Must respond with a heartbeat packet within 5 seconds
	if e.Operation == 1 {
		s.log(LogInformational, "sending heartbeat in response to Op1")
		s.wsMutex.Lock()
		err = wsjson.Write(s.wsConnCtx, s.wsConn, s.newHeartbeatOp(atomic.LoadInt64(s.sequence)))
		s.wsMutex.Unlock()
		if err != nil {
			s.log(LogError, "error sending heartbeat in response to Op1")
			return e, err
		}

		return e, nil
	}

	// Reconnect
	// Must immediately disconnect from gateway and reconnect to new gateway.
	if e.Operation == 7 {
		if isOnConnect {
			s.log(LogInformational, "Got Op7 in connect handler, returning error")
			return e, ErrImmediateDisconnect
		} else {
			s.log(LogInformational, "Closing and reconnecting in response to Op7")
			s.CloseWithCode(websocket.StatusServiceRestart)
			s.reconnect()
			return e, nil
		}
	}

	// Invalid Session
	// Must respond with a Identify packet.
	if e.Operation == 9 {
		var resumable bool
		if err := json.Unmarshal(e.RawData, &resumable); err != nil {
			s.log(LogError, "error unmarshalling invalid session event, %s", err)
			return e, err
		}

		if !resumable {
			s.log(LogInformational, "Gateway session is not resumable, discarding its information")
			s.resumeGatewayURL = ""
			s.sessionID = ""
			atomic.StoreInt64(s.sequence, 0)
		}

		if isOnConnect {
			// The Session's lock is already held; calling CloseWithCode or
			// reconnect at this point would deadlock. Rely on the caller
			// (Open) to respond appropriately.
			s.log(LogInformational, "Got Op9 in connect handler, returning error")
			return e, ErrInvalidSessionOnConnect
		}

		s.log(LogInformational, "Closing and reconnecting in response to Op9")
		s.CloseWithCode(websocket.StatusServiceRestart)
		s.reconnect()
		return e, nil
	}

	if e.Operation == 10 {
		// Op10 is handled by Open()
		return e, nil
	}

	if e.Operation == 11 {
		s.Lock()
		s.LastHeartbeatAck = time.Now().UTC()
		s.Unlock()
		s.log(LogDebug, "got heartbeat ACK")
		return e, nil
	}

	// Do not try to Dispatch a non-Dispatch Message
	if e.Operation != 0 {
		// But we probably should be doing something with them.
		// TEMP
		s.log(LogWarning, "unknown Op: %d, Seq: %d, Type: %s, Data: %s, message: %s", e.Operation, e.Sequence, e.Type, string(e.RawData), string(message))
		return e, nil
	}

	// Store the message sequence
	atomic.StoreInt64(s.sequence, e.Sequence)

	// Map event to registered event handlers and pass it along to any registered handlers.
	if eh, ok := registeredInterfaceProviders[e.Type]; ok {
		e.Struct = eh.New()

		// Attempt to unmarshal our event.
		if err = json.Unmarshal(e.RawData, e.Struct); err != nil {
			s.log(LogError, "error unmarshalling %s event, %s", e.Type, err)
		}

		// Send event to any registered event handlers for it's type.
		// Because the above doesn't cancel this, in case of an error
		// the struct could be partially populated or at default values.
		// However, most errors are due to a single field and I feel
		// it's better to pass along what we received than nothing at all.
		// TODO: Think about that decision :)
		// Either way, READY events must fire, even with errors.
		s.handleEvent(e.Type, e.Struct)
	} else {
		s.log(LogWarning, "unknown event: Op: %d, Seq: %d, Type: %s", e.Operation, e.Sequence, e.Type)
	}

	// For legacy reasons, we send the raw event also, this could be useful for handling unknown events.
	s.handleEvent(eventEventType, e)

	return e, nil
}

// ------------------------------------------------------------------------------------------------
// Code related to voice connections that initiate over the data websocket
// ------------------------------------------------------------------------------------------------

type voiceChannelJoinData struct {
	GuildID   *string `json:"guild_id"`
	ChannelID *string `json:"channel_id"`
	SelfMute  bool    `json:"self_mute"`
	SelfDeaf  bool    `json:"self_deaf"`
}

type voiceChannelJoinOp struct {
	Op   int                  `json:"op"`
	Data voiceChannelJoinData `json:"d"`
}

// ChannelVoiceJoin joins the session user to a voice channel.
//
//	gID     : Guild ID of the channel to join.
//	cID     : Channel ID of the channel to join.
//	mute    : If true, you will be set to muted upon joining.
//	deaf    : If true, you will be set to deafened upon joining.
func (s *Session) ChannelVoiceJoin(gID, cID string, mute, deaf bool) (voice *VoiceConnection, err error) {

	s.log(LogInformational, "called")

	s.RLock()
	voice = s.VoiceConnections[gID]
	s.RUnlock()

	if voice == nil {
		voice = &VoiceConnection{}
		s.Lock()
		s.VoiceConnections[gID] = voice
		s.Unlock()
	}

	voice.Lock()
	voice.GuildID = gID
	voice.ChannelID = cID
	voice.deaf = deaf
	voice.mute = mute
	voice.session = s
	voice.Unlock()

	err = s.ChannelVoiceJoinManual(gID, cID, mute, deaf)
	if err != nil {
		return
	}

	// doesn't exactly work perfect yet.. TODO
	err = voice.waitUntilConnected()
	if err != nil {
		s.log(LogWarning, "error waiting for voice to connect, %s", err)
		voice.Close()
		return
	}

	return
}

// ChannelVoiceJoinManual initiates a voice session to a voice channel, but does not complete it.
//
// This should only be used when the VoiceServerUpdate will be intercepted and used elsewhere.
//
//	gID     : Guild ID of the channel to join.
//	cID     : Channel ID of the channel to join, leave empty to disconnect.
//	mute    : If true, you will be set to muted upon joining.
//	deaf    : If true, you will be set to deafened upon joining.
func (s *Session) ChannelVoiceJoinManual(gID, cID string, mute, deaf bool) (err error) {

	s.log(LogInformational, "called")

	var channelID *string
	if cID == "" {
		channelID = nil
	} else {
		channelID = &cID
	}

	// Send the request to Discord that we want to join the voice channel
	data := voiceChannelJoinOp{4, voiceChannelJoinData{&gID, channelID, mute, deaf}}
	s.wsMutex.Lock()
	err = wsjson.Write(s.wsConnCtx, s.wsConn, data)
	s.wsMutex.Unlock()
	return
}

// onVoiceStateUpdate handles Voice State Update events on the data websocket.
func (s *Session) onVoiceStateUpdate(st *VoiceStateUpdate) {

	// If we don't have a connection for the channel, don't bother
	if st.ChannelID == "" {
		return
	}

	// Check if we have a voice connection to update
	s.RLock()
	voice, exists := s.VoiceConnections[st.GuildID]
	s.RUnlock()
	if !exists {
		return
	}

	// We only care about events that are about us.
	if s.State.User.ID != st.UserID {
		return
	}

	// Store the SessionID for later use.
	voice.Lock()
	voice.UserID = st.UserID
	voice.sessionID = st.SessionID
	voice.ChannelID = st.ChannelID
	voice.Unlock()
}

// onVoiceServerUpdate handles the Voice Server Update data websocket event.
//
// This is also fired if the Guild's voice region changes while connected
// to a voice channel.  In that case, need to re-establish connection to
// the new region endpoint.
func (s *Session) onVoiceServerUpdate(st *VoiceServerUpdate) {

	s.log(LogInformational, "called")

	s.RLock()
	voice, exists := s.VoiceConnections[st.GuildID]
	s.RUnlock()

	// If no VoiceConnection exists, just skip this
	if !exists {
		return
	}

	// If currently connected to voice ws/udp, then disconnect.
	// Has no effect if not connected.
	voice.Close()

	// Store values for later use
	voice.Lock()
	voice.token = st.Token
	voice.endpoint = st.Endpoint
	voice.GuildID = st.GuildID
	voice.Unlock()

	// Open a connection to the voice server
	err := voice.open()
	if err != nil {
		s.log(LogError, "onVoiceServerUpdate voice.open, %s", err)
	}
}

type identifyOp struct {
	Op   int      `json:"op"`
	Data Identify `json:"d"`
}

// identify sends the identify packet to the gateway
func (s *Session) identify() error {
	s.log(LogDebug, "called")

	// TODO: This is a temporary block of code to help
	// maintain backwards compatibility
	if !s.Compress {
		s.Identify.Compress = false
	}

	// TODO: This is a temporary block of code to help
	// maintain backwards compatibility
	if s.Token != "" && s.Identify.Token == "" {
		s.Identify.Token = s.Token
	}

	// TODO: Below block should be refactored so ShardID and ShardCount
	// can be deprecated and their usage moved to the Session.Identify
	// struct
	if s.ShardCount > 1 {

		if s.ShardID >= s.ShardCount {
			return ErrWSShardBounds
		}

		s.Identify.Shard = &[2]int{s.ShardID, s.ShardCount}
	}

	// Send Identify packet to Discord
	op := identifyOp{2, s.Identify}
	dat, _ := json.Marshal(s.Identify)
	s.log(LogDebug, "Identify Packet: %s", dat)
	s.wsMutex.Lock()
	err := wsjson.Write(s.wsConnCtx, s.wsConn, op)
	s.wsMutex.Unlock()

	return err
}

func (s *Session) reconnect() {

	s.log(LogInformational, "called")

	var err error

	if s.ShouldReconnectOnError {

		wait := time.Duration(1)

		for {
			if s.BeforeReconnect != nil {
				s.BeforeReconnect(s)
			}

			s.log(LogInformational, "trying to reconnect to gateway")

			err = s.Open()
			if err == nil {
				s.log(LogInformational, "successfully reconnected to gateway")

				// I'm not sure if this is actually needed.
				// if the gw reconnect works properly, voice should stay alive
				// However, there seems to be cases where something "weird"
				// happens.  So we're doing this for now just to improve
				// stability in those edge cases.
				if s.ShouldReconnectVoiceOnSessionError {
					s.RLock()
					defer s.RUnlock()
					for _, v := range s.VoiceConnections {

						s.log(LogInformational, "reconnecting voice connection to guild %s", v.GuildID)
						go v.reconnect()

						// This is here just to prevent violently spamming the
						// voice reconnects
						time.Sleep(1 * time.Second)
					}
				}
				return
			}

			// Certain race conditions can call reconnect() twice. If this happens, we
			// just break out of the reconnect loop
			if err == ErrWSAlreadyOpen {
				s.log(LogInformational, "Websocket already exists, no need to reconnect")
				return
			}

			s.log(LogError, "error reconnecting to gateway, %s", err)

			if websocket.CloseStatus(err) == 4004 {
				s.log(LogInformational, "emit invalid auth event")
				s.handleEvent(invalidAuthEventType, &InvalidAuth{})
				return
			}

			<-time.After(wait * time.Second)
			wait *= 2
			if wait > 600 {
				wait = 600
			}
		}
	}
}

// Close closes a websocket and stops all listening/heartbeat goroutines.
// TODO: Add support for Voice WS/UDP
func (s *Session) Close() error {
	return s.CloseWithCode(websocket.StatusNormalClosure)
}

// CloseWithCode closes a websocket using the provided closeCode and stops all
// listening/heartbeat goroutines.
// TODO: Add support for Voice WS/UDP connections
func (s *Session) CloseWithCode(closeCode websocket.StatusCode) (err error) {

	s.log(LogInformational, "called")
	s.Lock()

	s.DataReady = false

	if s.listening != nil {
		s.log(LogInformational, "closing listening channel")
		close(s.listening)
		s.listening = nil
	}

	// TODO: Close all active Voice Connections too
	// this should force stop any reconnecting voice channels too

	if s.wsConn != nil {

		s.log(LogInformational, "closing gateway websocket")
		s.wsMutex.Lock()
		// Close _before_ canceling the wsConnCtx, as cancelling triggers an
		// asynchronous teardown of the underlying connection, which would race
		// with our intentional Close.
		err := s.wsConn.Close(closeCode, "")
		s.wsMutex.Unlock()
		if err != nil {
			s.log(LogInformational, "error closing websocket, %s", err)
		}

		if s.wsConnCancel != nil {
			s.wsConnCancel()
		}

		s.wsConn = nil
		s.wsConnCtx = nil
		s.wsConnCancel = nil
		s.closeZLib()
	}

	s.Unlock()

	s.log(LogInformational, "emit disconnect event")
	s.handleEvent(disconnectEventType, &Disconnect{})

	return
}

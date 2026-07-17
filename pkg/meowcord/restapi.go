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

// This file contains functions for interacting with the Discord REST/JSON API
// at the lowest level.

package meowcord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // For JPEG decoding
	_ "image/png"  // For PNG decoding
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// All error constants
var (
	ErrJSONUnmarshal           = errors.New("json unmarshal")
	ErrStatusOffline           = errors.New("you can't set your Status to offline")
	ErrVerificationLevelBounds = errors.New("VerificationLevel out of bounds, should be between 0 and 3")
	ErrPruneDaysBounds         = errors.New("the number of days should be more than or equal to 1")
	ErrGuildNoIcon             = errors.New("guild does not have an icon set")
	ErrGuildNoSplash           = errors.New("guild does not have a splash set")
	ErrUnauthorized            = errors.New("HTTP request was unauthorized. This could be because the provided token was not a bot token. Please add \"Bot \" to the start of your token. https://discord.com/developers/docs/reference#authentication-example-bot-token-authorization-header")
	ErrImmediateDisconnect     = errors.New("got op7 reconnect while connecting")
	ErrInvalidSessionOnConnect = errors.New("got op9 invalid session while connecting")
)

var (
	// Marshal defines function used to encode JSON payloads
	Marshal func(v interface{}) ([]byte, error) = json.Marshal
	// Unmarshal defines function used to decode JSON payloads
	Unmarshal func(src []byte, v interface{}) error = json.Unmarshal
)

// RESTError stores error information about a request with a bad response code.
// Message is not always present, there are cases where api calls can fail
// without returning a json message.
type RESTError struct {
	Request      *http.Request
	Response     *http.Response
	ResponseBody []byte

	Message *APIErrorMessage // Message may be nil.
}

// newRestError returns a new REST API error.
func newRestError(req *http.Request, resp *http.Response, body []byte) *RESTError {
	restErr := &RESTError{
		Request:      req,
		Response:     resp,
		ResponseBody: body,
	}

	// Attempt to decode the error and assume no message was provided if it fails
	var msg *APIErrorMessage
	err := Unmarshal(body, &msg)
	if err == nil {
		restErr.Message = msg
	}

	return restErr
}

// Error returns a Rest API Error with its status code and body.
func (r RESTError) Error() string {
	return "HTTP " + r.Response.Status + ", " + string(r.ResponseBody)
}

// RateLimitError is returned when a request exceeds a rate limit
// and ShouldRetryOnRateLimit is false. The request may be manually
// retried after waiting the duration specified by RetryAfter.
type RateLimitError struct {
	*RateLimit
}

// Error returns a rate limit error with rate limited endpoint and retry time.
func (e RateLimitError) Error() string {
	return "Rate limit exceeded on " + e.URL + ", retry after " + e.RetryAfter.String()
}

// RequestConfig is an HTTP request configuration.
type RequestConfig struct {
	Request                *http.Request
	ShouldRetryOnRateLimit bool
	MaxRestRetries         int
	Client                 *http.Client
}

// newRequestConfig returns a new HTTP request configuration based on parameters in Session.
func newRequestConfig(s *Session, req *http.Request) *RequestConfig {
	return &RequestConfig{
		ShouldRetryOnRateLimit: s.ShouldRetryOnRateLimit,
		MaxRestRetries:         s.MaxRestRetries,
		Client:                 s.Client,
		Request:                req,
	}
}

// RequestOption is a function which mutates request configuration.
// It can be supplied as an argument to any REST method.
type RequestOption func(cfg *RequestConfig)

// WithClient changes the HTTP client used for the request.
func WithClient(client *http.Client) RequestOption {
	return func(cfg *RequestConfig) {
		if client != nil {
			cfg.Client = client
		}
	}
}

// WithRetryOnRatelimit controls whether session will retry the request on rate limit.
func WithRetryOnRatelimit(retry bool) RequestOption {
	return func(cfg *RequestConfig) {
		cfg.ShouldRetryOnRateLimit = retry
	}
}

// WithRestRetries changes maximum amount of retries if request fails.
func WithRestRetries(max int) RequestOption {
	return func(cfg *RequestConfig) {
		cfg.MaxRestRetries = max
	}
}

// WithHeader sets a header in the request.
func WithHeader(key, value string) RequestOption {
	return func(cfg *RequestConfig) {
		cfg.Request.Header.Set(key, value)
	}
}

// WithAuditLogReason changes audit log reason associated with the request.
func WithAuditLogReason(reason string) RequestOption {
	return WithHeader("X-Audit-Log-Reason", reason)
}

// WithLocale changes accepted locale of the request.
func WithLocale(locale Locale) RequestOption {
	return WithHeader("X-Discord-Locale", string(locale))
}

// WithContextProperties changes the X-Context-Properties header sent with the request.
func WithContextProperties(location string) RequestOption {
	jsonText, err := json.Marshal(map[string]string{
		"location": location,
	})
	if err != nil {
		panic(err)
	}
	return WithHeader("X-Context-Properties", base64.StdEncoding.EncodeToString(jsonText))
}

func WithReferer(referer string, args ...any) RequestOption {
	if len(args) > 0 {
		referer = fmt.Sprintf(referer, args...)
	}
	return WithHeader("Referer", referer)
}

func WithChannelReferer(guildID, channelID string) RequestOption {
	if guildID == "" {
		guildID = "@me"
	}
	return WithReferer("https://discord.com/channels/%s/%s", guildID, channelID)
}

func WithThreadReferer(guildID, channelID, threadID string) RequestOption {
	return WithReferer("https://discord.com/channels/%s/%s/threads/%s", guildID, channelID, threadID)
}

func WithQueryParam(key, value string) RequestOption {
	return func(cfg *RequestConfig) {
		q := cfg.Request.URL.Query()
		q.Add(key, value)
		cfg.Request.URL.RawQuery = q.Encode()
	}
}

func WithLocationParam(loc string) RequestOption {
	return WithQueryParam("location", loc)
}

// WithContext changes context of the request.
func WithContext(ctx context.Context) RequestOption {
	return func(cfg *RequestConfig) {
		cfg.Request = cfg.Request.WithContext(ctx)
	}
}

// Request is the same as RequestWithBucketID but the bucket id is the same as the urlStr
func (s *Session) Request(method, urlStr string, data interface{}, options ...RequestOption) (response []byte, err error) {
	return s.RequestWithBucketID(method, urlStr, data, strings.SplitN(urlStr, "?", 2)[0], options...)
}

// RequestWithBucketID makes a (GET/POST/...) Requests to Discord REST API with JSON data.
func (s *Session) RequestWithBucketID(method, urlStr string, data interface{}, bucketID string, options ...RequestOption) (response []byte, err error) {
	var body []byte
	if data != nil {
		body, err = Marshal(data)
		if err != nil {
			return
		}
	}

	return s.RequestRaw(method, urlStr, "application/json", body, bucketID, 0, options...)
}

// RequestRaw makes a (GET/POST/...) Requests to Discord REST API.
// Preferably use the other Request* methods but this lets you send JSON directly if that's what you have.
// Sequence is the sequence number, if it fails with a 502 it will
// retry with sequence+1 until it either succeeds or sequence >= session.MaxRestRetries
func (s *Session) RequestRaw(method, urlStr, contentType string, b []byte, bucketID string, sequence int, options ...RequestOption) (response []byte, err error) {
	if bucketID == "" {
		bucketID = strings.SplitN(urlStr, "?", 2)[0]
	}
	return s.RequestWithLockedBucket(method, urlStr, contentType, b, s.Ratelimiter.LockBucket(bucketID), sequence, options...)
}

// RequestWithLockedBucket makes a request using a bucket that's already been locked
func (s *Session) RequestWithLockedBucket(method, urlStr, contentType string, b []byte, bucket *Bucket, sequence int, options ...RequestOption) (response []byte, err error) {
	if s.Debug {
		log.Printf("API REQUEST %8s :: %s\n", method, urlStr)
		log.Printf("API REQUEST  PAYLOAD :: [%s]\n", string(b))
	}

	req, err := http.NewRequest(method, urlStr, bytes.NewBuffer(b))
	if err != nil {
		bucket.Release(nil)
		return
	}

	// Not used on initial login..
	// TODO: Verify if a login, otherwise complain about no-token
	if s.Token != "" {
		req.Header.Set("authorization", s.Token)
	}

	// Discord's API returns a 400 Bad Request is Content-Type is set, but the
	// request body is empty.
	if b != nil {
		req.Header.Set("Content-Type", contentType)
	}

	// TODO: Make a configurable static variable.
	req.Header.Set("User-Agent", s.UserAgent)

	if s.IsUser {
		headers := s.fetchHeaders
		if strings.HasPrefix(urlStr, "https://cdn.discordapp.com") {
			headers = s.downloadHeaders
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	cfg := newRequestConfig(s, req)
	for _, opt := range options {
		opt(cfg)
	}
	req = cfg.Request

	if s.Debug {
		for k, v := range req.Header {
			log.Printf("API REQUEST   HEADER :: [%s] = %+v\n", k, v)
		}
	}

	logBody := b
	if contentType != "application/json" {
		logBody = fmt.Appendf(nil, "%d bytes of %s", len(logBody), contentType)
	}
	s.log(LogDebug, "Requesting %s %s with %s", req.Method, req.URL.String(), logBody)
	resp, err := cfg.Client.Do(req)
	if err != nil {
		bucket.Release(nil)
		return
	}
	defer func() {
		err2 := resp.Body.Close()
		if s.Debug && err2 != nil {
			log.Println("error closing resp body")
		}
	}()

	err = bucket.Release(resp.Header)
	if err != nil {
		return
	}

	response, err = io.ReadAll(resp.Body)
	if s.RESTResponseHook != nil {
		s.RESTResponseHook(req, resp, response)
	}
	if err != nil {
		return
	}
	s.log(LogDebug, "Response to %s %s: %d / %s", req.Method, req.URL.String(), resp.StatusCode, response)

	if s.Debug {

		log.Printf("API RESPONSE  STATUS :: %s\n", resp.Status)
		for k, v := range resp.Header {
			log.Printf("API RESPONSE  HEADER :: [%s] = %+v\n", k, v)
		}
		log.Printf("API RESPONSE    BODY :: [%s]\n\n\n", response)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusCreated:
	case http.StatusNoContent:
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// Retry sending request if possible
		if sequence < cfg.MaxRestRetries {

			s.log(LogInformational, "%s Failed (%s), Retrying...", urlStr, resp.Status)
			response, err = s.RequestWithLockedBucket(method, urlStr, contentType, b, s.Ratelimiter.LockBucketObject(bucket), sequence+1, options...)
		} else {
			err = newRestError(req, resp, response)
		}
	case http.StatusTooManyRequests:
		rl := TooManyRequests{}
		err = Unmarshal(response, &rl)
		if err != nil {
			s.log(LogError, "rate limit unmarshal error, %s", err)
			return
		}

		if cfg.ShouldRetryOnRateLimit {
			s.log(LogInformational, "Rate Limiting %s, retry in %v", urlStr, rl.RetryAfter)
			s.handleEvent(rateLimitEventType, &RateLimit{TooManyRequests: &rl, URL: urlStr})

			time.Sleep(rl.RetryAfter)
			// we can make the above smarter
			// this method can cause longer delays than required

			response, err = s.RequestWithLockedBucket(method, urlStr, contentType, b, s.Ratelimiter.LockBucketObject(bucket), sequence, options...)
		} else {
			err = &RateLimitError{&RateLimit{TooManyRequests: &rl, URL: urlStr}}
		}
	case http.StatusUnauthorized:
		if strings.Index(s.Token, "Bot ") != 0 {
			s.log(LogInformational, "%s", ErrUnauthorized.Error())
			err = ErrUnauthorized
		}
		fallthrough
	default: // Error condition
		err = newRestError(req, resp, response)
	}

	return
}

func unmarshal(data []byte, v interface{}) error {
	err := Unmarshal(data, v)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrJSONUnmarshal, err)
	}

	return nil
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Sessions
// ------------------------------------------------------------------------------------------------

// Login asks the Discord server for an authentication token.
//
// NOTE: While email/pass authentication is supported by DiscordGo it is
// HIGHLY DISCOURAGED by Discord. Please only use email/pass to obtain a token
// and then use that authentication token for all future connections.
// Also, doing any form of automation with a user (non Bot) account may result
// in that account being permanently banned from Discord.
func (s *Session) Login(email, password string) (err error) {

	data := struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}{email, password}

	response, err := s.RequestWithBucketID("POST", EndpointLogin, data, EndpointLogin)
	if err != nil {
		return
	}

	temp := struct {
		Token string `json:"token"`
		MFA   bool   `json:"mfa"`
	}{}

	err = unmarshal(response, &temp)
	if err != nil {
		return
	}

	s.Token = temp.Token
	s.MFA = temp.MFA
	return
}

func (s *Session) RemoteAuthLogin(ticket string) (encryptedToken string, err error) {

	data := struct {
		Ticket string `json:"ticket"`
	}{ticket}

	response, err := s.RequestWithBucketID("POST", EndpointRemoteAuthLogin, data, EndpointRemoteAuthLogin)
	if err != nil {
		return
	}

	temp := struct {
		EncryptedToken string `json:"encrypted_token"`
	}{}

	err = unmarshal(response, &temp)
	if err != nil {
		return
	}

	encryptedToken = temp.EncryptedToken
	return
}

// Register sends a Register request to Discord, and returns the authentication token
// Note that this account is temporary and should be verified for future use.
// Another option is to save the authentication token external, but this isn't recommended.
func (s *Session) Register(username string) (token string, err error) {

	data := struct {
		Username string `json:"username"`
	}{username}

	response, err := s.RequestWithBucketID("POST", EndpointRegister, data, EndpointRegister)
	if err != nil {
		return
	}

	temp := struct {
		Token string `json:"token"`
	}{}

	err = unmarshal(response, &temp)
	if err != nil {
		return
	}

	token = temp.Token
	return
}

// Logout sends a logout request to Discord.
// This does not seem to actually invalidate the token.  So you can still
// make API calls even after a Logout.  So, it seems almost pointless to
// even use.
func (s *Session) Logout() (err error) {

	//  _, err = s.Request("POST", LOGOUT, `{"token": "` + s.Token + `"}`)

	if s.Token == "" {
		return
	}

	data := struct {
		Token string `json:"token"`
	}{s.Token}

	_, err = s.RequestWithBucketID("POST", EndpointLogout, data, EndpointLogout)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Users
// ------------------------------------------------------------------------------------------------

// User returns the user details of the given userID
// userID    : A user ID or "@me" which is a shortcut of current user ID
func (s *Session) User(userID string, options ...RequestOption) (st *User, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointUser(userID), nil, EndpointUsers, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserAvatar is deprecated. Please use UserAvatarDecode
// userID    : A user ID or "@me" which is a shortcut of current user ID
func (s *Session) UserAvatar(userID string, options ...RequestOption) (img image.Image, err error) {
	u, err := s.User(userID, options...)
	if err != nil {
		return
	}
	img, err = s.UserAvatarDecode(u, options...)
	return
}

// UserAvatarDecode returns an image.Image of a user's Avatar
// user : The user which avatar should be retrieved
func (s *Session) UserAvatarDecode(u *User, options ...RequestOption) (img image.Image, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointUserAvatar(u.ID, u.Avatar), nil, EndpointUserAvatar("", ""), options...)
	if err != nil {
		return
	}

	img, _, err = image.Decode(bytes.NewReader(body))
	return
}

// UserUpdate updates current user settings.
func (s *Session) UserUpdate(email, password, username, avatar, banner, newPassword string, options ...RequestOption) (st *User, err error) {

	// NOTE: Avatar must be either the hash/id of existing Avatar or
	// data:image/png;base64,BASE64_STRING_OF_NEW_AVATAR_PNG
	// to set a new avatar.
	// If left blank, avatar will be set to null/blank

	data := struct {
		Email       string `json:"email,omitempty"`
		Password    string `json:"password,omitempty"`
		Username    string `json:"username,omitempty"`
		Avatar      string `json:"avatar,omitempty"`
		Banner      string `json:"banner,omitempty"`
		NewPassword string `json:"new_password,omitempty"`
	}{email, password, username, avatar, banner, newPassword}

	body, err := s.RequestWithBucketID("PATCH", EndpointUser("@me"), data, EndpointUsers, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserSettings returns the settings for a given user
func (s *Session) UserSettings() (st *Settings, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointUserSettings("@me"), nil, EndpointUserSettings(""))
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserUpdateStatus update the user status
// status   : The new status (Actual valid status are 'online','idle','dnd','invisible')
func (s *Session) UserUpdateStatus(status Status) (st *Settings, err error) {
	if status == StatusOffline {
		err = ErrStatusOffline
		return
	}

	data := struct {
		Status Status `json:"status"`
	}{status}

	body, err := s.RequestWithBucketID("PATCH", EndpointUserSettings("@me"), data, EndpointUserSettings(""))
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

func (s *Session) SafetyHub(options ...RequestOption) (sh *SafetyHub, err error) {
	response, err := s.Request("GET", EndpointSafetyHub(), nil, options...)
	if err != nil {
		return nil, err
	}
	err = unmarshal(response, &sh)
	return
}

// UserConnections returns the user's connections
func (s *Session) UserConnections(options ...RequestOption) (conn []*UserConnection, err error) {
	response, err := s.RequestWithBucketID("GET", EndpointUserConnections("@me"), nil, EndpointUserConnections("@me"), options...)
	if err != nil {
		return nil, err
	}

	err = unmarshal(response, &conn)
	if err != nil {
		return
	}

	return
}

// UserChannels returns an array of Channel structures for all private
// channels.
func (s *Session) UserChannels() (st []*Channel, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointUserChannels("@me"), nil, EndpointUserChannels(""))
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserChannelCreate creates a new User (Private) Channel with another User
// recipientID : A user ID for the user to which this channel is opened with.
func (s *Session) UserChannelCreate(recipientID string, options ...RequestOption) (st *Channel, err error) {

	data := struct {
		RecipientID string `json:"recipient_id"`
	}{recipientID}

	body, err := s.RequestWithBucketID("POST", EndpointUserChannels("@me"), data, EndpointUserChannels(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserGuildMember returns a guild member object for the current user in the given Guild.
// guildID : ID of the guild
func (s *Session) UserGuildMember(guildID string, options ...RequestOption) (st *Member, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointUserGuildMember("@me", guildID), nil, EndpointUserGuildMember("@me", guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserGuilds returns an array of UserGuild structures for all guilds.
// limit       : The number guilds that can be returned. (max 200)
// beforeID    : If provided all guilds returned will be before given ID.
// afterID     : If provided all guilds returned will be after given ID.
// withCounts  : Whether to include approximate member and presence counts or not.
func (s *Session) UserGuilds(limit int, beforeID, afterID string, withCounts bool, options ...RequestOption) (st []*UserGuild, err error) {

	v := url.Values{}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if afterID != "" {
		v.Set("after", afterID)
	}
	if beforeID != "" {
		v.Set("before", beforeID)
	}
	if withCounts {
		v.Set("with_counts", "true")
	}

	uri := EndpointUserGuilds("@me")

	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointUserGuilds(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserGuildSettingsEdit Edits the users notification settings for a guild
// guildID   : The ID of the guild to edit the settings on
// settings  : The settings to update
func (s *Session) UserGuildSettingsEdit(guildID string, settings *UserGuildSettingsEdit) (st *UserGuildSettings, err error) {

	body, err := s.RequestWithBucketID("PATCH", EndpointUserGuildSettings("@me", guildID), settings, EndpointUserGuildSettings("", guildID))
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserChannelPermissions returns the permission of a user in a channel.
// userID        : The ID of the user to calculate permissions for.
// channelID     : The ID of the channel to calculate permission for.
// fetchOptions  : Options used to fetch guild, member or channel if they are not present in state.
//
// NOTE: This function is now deprecated and will be removed in the future.
// Please see the same function inside state.go
func (s *Session) UserChannelPermissions(userID, channelID string, fetchOptions ...RequestOption) (apermissions int64, err error) {
	// Try to just get permissions from state.
	apermissions, err = s.State.UserChannelPermissions(userID, channelID)
	if err == nil {
		return
	}

	// Otherwise try get as much data from state as possible, falling back to the network.
	channel, err := s.State.Channel(channelID)
	if err != nil || channel == nil {
		channel, err = s.Channel(channelID, fetchOptions...)
		if err != nil {
			return
		}
	}

	guild, err := s.State.Guild(channel.GuildID)
	if err != nil || guild == nil {
		guild, err = s.Guild(channel.GuildID, fetchOptions...)
		if err != nil {
			return
		}
	}

	if userID == guild.OwnerID {
		apermissions = PermissionAll
		return
	}

	member, err := s.State.Member(guild.ID, userID)
	if err != nil || member == nil {
		member, err = s.GuildMember(guild.ID, userID, fetchOptions...)
		if err != nil {
			return
		}
	}

	return memberPermissions(guild, channel, userID, member.Roles), nil
}

// Calculates the permissions for a member.
// https://support.discord.com/hc/en-us/articles/206141927-How-is-the-permission-hierarchy-structured-
func memberPermissions(guild *Guild, channel *Channel, userID string, roles []string) (apermissions int64) {
	if userID == guild.OwnerID {
		apermissions = PermissionAll
		return
	}

	for _, role := range guild.Roles {
		if role.ID == guild.ID {
			apermissions |= role.Permissions
			break
		}
	}

	for _, role := range guild.Roles {
		for _, roleID := range roles {
			if role.ID == roleID {
				apermissions |= role.Permissions
				break
			}
		}
	}

	if apermissions&PermissionAdministrator == PermissionAdministrator {
		apermissions |= PermissionAll
	}

	// Apply @everyone overrides from the channel.
	for _, overwrite := range channel.PermissionOverwrites {
		if guild.ID == overwrite.ID {
			apermissions &= ^overwrite.Deny
			apermissions |= overwrite.Allow
			break
		}
	}

	var denies, allows int64
	// Member overwrites can override role overrides, so do two passes
	for _, overwrite := range channel.PermissionOverwrites {
		for _, roleID := range roles {
			if overwrite.Type == PermissionOverwriteTypeRole && roleID == overwrite.ID {
				denies |= overwrite.Deny
				allows |= overwrite.Allow
				break
			}
		}
	}

	apermissions &= ^denies
	apermissions |= allows

	for _, overwrite := range channel.PermissionOverwrites {
		if overwrite.Type == PermissionOverwriteTypeMember && overwrite.ID == userID {
			apermissions &= ^overwrite.Deny
			apermissions |= overwrite.Allow
			break
		}
	}

	if apermissions&PermissionAdministrator == PermissionAdministrator {
		apermissions |= PermissionAllChannel
	}

	return apermissions
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Guilds
// ------------------------------------------------------------------------------------------------

// Guild returns a Guild structure of a specific Guild.
// guildID   : The ID of a Guild
func (s *Session) Guild(guildID string, options ...RequestOption) (st *Guild, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointGuild(guildID), nil, EndpointGuild(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildWithCounts returns a Guild structure of a specific Guild with approximate member and presence counts.
// guildID    : The ID of a Guild
func (s *Session) GuildWithCounts(guildID string, options ...RequestOption) (st *Guild, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuild(guildID)+"?with_counts=true", nil, EndpointGuild(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildPreview returns a GuildPreview structure of a specific public Guild.
// guildID   : The ID of a Guild
func (s *Session) GuildPreview(guildID string, options ...RequestOption) (st *GuildPreview, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointGuildPreview(guildID), nil, EndpointGuildPreview(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildCreate creates a new Guild
// name      : A name for the Guild (2-100 characters)
func (s *Session) GuildCreate(name string, options ...RequestOption) (st *Guild, err error) {

	data := struct {
		Name string `json:"name"`
	}{name}

	body, err := s.RequestWithBucketID("POST", EndpointGuildCreate, data, EndpointGuildCreate, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildEdit edits a new Guild
// guildID   : The ID of a Guild
// g 		 : A GuildParams struct with the values Name, Region and VerificationLevel defined.
func (s *Session) GuildEdit(guildID string, g *GuildParams, options ...RequestOption) (st *Guild, err error) {

	// Bounds checking for VerificationLevel, interval: [0, 4]
	if g.VerificationLevel != nil {
		val := *g.VerificationLevel
		if val < 0 || val > 4 {
			err = ErrVerificationLevelBounds
			return
		}
	}

	// Bounds checking for regions
	if g.Region != "" {
		isValid := false
		regions, _ := s.VoiceRegions(options...)
		for _, r := range regions {
			if g.Region == r.ID {
				isValid = true
			}
		}
		if !isValid {
			var valid []string
			for _, r := range regions {
				valid = append(valid, r.ID)
			}
			err = fmt.Errorf("region not a valid region (%q)", valid)
			return
		}
	}

	body, err := s.RequestWithBucketID("PATCH", EndpointGuild(guildID), g, EndpointGuild(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildDelete deletes a Guild.
// guildID   : The ID of a Guild
func (s *Session) GuildDelete(guildID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuild(guildID), nil, EndpointGuild(guildID), options...)
	return
}

// GuildLeave leaves a Guild.
// guildID   : The ID of a Guild
func (s *Session) GuildLeave(guildID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointUserGuild("@me", guildID), nil, EndpointUserGuild("", guildID), options...)
	return
}

// GuildBans returns an array of GuildBan structures for bans in the given guild.
// guildID   : The ID of a Guild
// limit     : Max number of bans to return (max 1000)
// beforeID  : If not empty all returned users will be after the given id
// afterID   : If not empty all returned users will be before the given id
func (s *Session) GuildBans(guildID string, limit int, beforeID, afterID string, options ...RequestOption) (st []*GuildBan, err error) {
	uri := EndpointGuildBans(guildID)

	v := url.Values{}
	if limit != 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if beforeID != "" {
		v.Set("before", beforeID)
	}
	if afterID != "" {
		v.Set("after", afterID)
	}

	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildBans(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildBanCreate bans the given user from the given guild.
// guildID   : The ID of a Guild.
// userID    : The ID of a User
// days      : The number of days of previous comments to delete.
func (s *Session) GuildBanCreate(guildID, userID string, days int, options ...RequestOption) (err error) {
	return s.GuildBanCreateWithReason(guildID, userID, "", days, options...)
}

// GuildBan finds ban by given guild and user id and returns GuildBan structure
func (s *Session) GuildBan(guildID, userID string, options ...RequestOption) (st *GuildBan, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildBan(guildID, userID), nil, EndpointGuildBan(guildID, userID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildBanCreateWithReason bans the given user from the given guild also providing a reaso.
// guildID   : The ID of a Guild.
// userID    : The ID of a User
// reason    : The reason for this ban
// days      : The number of days of previous comments to delete.
func (s *Session) GuildBanCreateWithReason(guildID, userID, reason string, days int, options ...RequestOption) (err error) {

	uri := EndpointGuildBan(guildID, userID)

	queryParams := url.Values{}
	if days > 0 {
		queryParams.Set("delete_message_days", strconv.Itoa(days))
	}
	if reason != "" {
		queryParams.Set("reason", reason)
	}

	if len(queryParams) > 0 {
		uri += "?" + queryParams.Encode()
	}

	_, err = s.RequestWithBucketID("PUT", uri, nil, EndpointGuildBan(guildID, ""), options...)
	return
}

// GuildBanDelete removes the given user from the guild bans
// guildID   : The ID of a Guild.
// userID    : The ID of a User
func (s *Session) GuildBanDelete(guildID, userID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildBan(guildID, userID), nil, EndpointGuildBan(guildID, ""), options...)
	return
}

// GuildMembers returns a list of members for a guild.
// guildID  : The ID of a Guild.
// after    : The id of the member to return members after
// limit    : max number of members to return (max 1000)
func (s *Session) GuildMembers(guildID string, after string, limit int, options ...RequestOption) (st []*Member, err error) {

	uri := EndpointGuildMembers(guildID)

	v := url.Values{}

	if after != "" {
		v.Set("after", after)
	}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildMembers(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	// The returned objects don't have the GuildID attribute so we will set it here.
	for _, member := range st {
		member.GuildID = guildID
	}
	return
}

// GuildMembersSearch returns a list of guild member objects whose username or nickname starts with a provided string
// guildID  : The ID of a Guild
// query    : Query string to match username(s) and nickname(s) against
// limit    : Max number of members to return (default 1, min 1, max 1000)
func (s *Session) GuildMembersSearch(guildID, query string, limit int, options ...RequestOption) (st []*Member, err error) {

	uri := EndpointGuildMembersSearch(guildID)

	queryParams := url.Values{}
	queryParams.Set("query", query)
	if limit > 1 {
		queryParams.Set("limit", strconv.Itoa(limit))
	}

	body, err := s.RequestWithBucketID("GET", uri+"?"+queryParams.Encode(), nil, uri, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildMember returns a member of a guild.
// guildID   : The ID of a Guild.
// userID    : The ID of a User
func (s *Session) GuildMember(guildID, userID string, options ...RequestOption) (st *Member, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildMember(guildID, userID), nil, EndpointGuildMember(guildID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	// The returned object doesn't have the GuildID attribute so we will set it here.
	st.GuildID = guildID
	return
}

// GuildMemberAdd force joins a user to the guild.
// guildID       : The ID of a Guild.
// userID        : The ID of a User.
// data          : Parameters of the user to add.
func (s *Session) GuildMemberAdd(guildID, userID string, data *GuildMemberAddParams, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("PUT", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	if err != nil {
		return err
	}

	return err
}

// GuildMemberDelete removes the given user from the given guild.
// guildID   : The ID of a Guild.
// userID    : The ID of a User
func (s *Session) GuildMemberDelete(guildID, userID string, options ...RequestOption) (err error) {

	return s.GuildMemberDeleteWithReason(guildID, userID, "", options...)
}

// GuildMemberDeleteWithReason removes the given user from the given guild.
// guildID   : The ID of a Guild.
// userID    : The ID of a User
// reason    : The reason for the kick
func (s *Session) GuildMemberDeleteWithReason(guildID, userID, reason string, options ...RequestOption) (err error) {

	uri := EndpointGuildMember(guildID, userID)
	if reason != "" {
		uri += "?reason=" + url.QueryEscape(reason)
	}

	_, err = s.RequestWithBucketID("DELETE", uri, nil, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberEdit edits and returns updated member.
// guildID  : The ID of a Guild.
// userID   : The ID of a User.
// data     : Updated GuildMember data.
func (s *Session) GuildMemberEdit(guildID, userID string, data *GuildMemberParams, options ...RequestOption) (st *Member, err error) {
	var body []byte
	body, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	if err != nil {
		return nil, err
	}

	err = unmarshal(body, &st)
	return
}

// GuildMemberEditComplex edits the nickname and roles of a member.
// NOTE: deprecated, use GuildMemberEdit instead.
//
// guildID  : The ID of a Guild.
// userID   : The ID of a User.
// data     : A GuildMemberEditData struct with the new nickname and roles
func (s *Session) GuildMemberEditComplex(guildID, userID string, data *GuildMemberParams, options ...RequestOption) (st *Member, err error) {
	return s.GuildMemberEdit(guildID, userID, data, options...)
}

// GuildMemberMove moves a guild member from one voice channel to another/none
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// channelID : The ID of a channel to move user to or nil to remove from voice channel
//
// NOTE : I am not entirely set on the name of this function and it may change
// prior to the final 1.0.0 release of Discordgo
func (s *Session) GuildMemberMove(guildID string, userID string, channelID *string, options ...RequestOption) (err error) {
	data := struct {
		ChannelID *string `json:"channel_id"`
	}{channelID}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberNickname updates the nickname of a guild member
// guildID   : The ID of a guild
// userID    : The ID of a user
// userID    : The ID of a user or "@me" which is a shortcut of the current user ID
// nickname  : The nickname of the member, "" will reset their nickname
func (s *Session) GuildMemberNickname(guildID, userID, nickname string, options ...RequestOption) (err error) {

	data := struct {
		Nick string `json:"nick"`
	}{nickname}

	if userID == "@me" {
		userID += "/nick"
	}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberMute server mutes a guild member
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// mute      : boolean value for if the user should be muted
func (s *Session) GuildMemberMute(guildID string, userID string, mute bool, options ...RequestOption) (err error) {
	data := struct {
		Mute bool `json:"mute"`
	}{mute}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberTimeout times out a guild member
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// until     : The timestamp for how long a member should be timed out. Set to nil to remove timeout.
func (s *Session) GuildMemberTimeout(guildID string, userID string, until *time.Time, options ...RequestOption) (err error) {
	data := struct {
		CommunicationDisabledUntil *time.Time `json:"communication_disabled_until"`
	}{until}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberDeafen server deafens a guild member
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// deaf      : boolean value for if the user should be deafened
func (s *Session) GuildMemberDeafen(guildID string, userID string, deaf bool, options ...RequestOption) (err error) {
	data := struct {
		Deaf bool `json:"deaf"`
	}{deaf}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildMember(guildID, userID), data, EndpointGuildMember(guildID, ""), options...)
	return
}

// GuildMemberRoleAdd adds the specified role to a given member
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// roleID    : The ID of a Role to be assigned to the user.
func (s *Session) GuildMemberRoleAdd(guildID, userID, roleID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("PUT", EndpointGuildMemberRole(guildID, userID, roleID), nil, EndpointGuildMemberRole(guildID, "", ""), options...)

	return
}

// GuildMemberRoleRemove removes the specified role to a given member
// guildID   : The ID of a Guild.
// userID    : The ID of a User.
// roleID    : The ID of a Role to be removed from the user.
func (s *Session) GuildMemberRoleRemove(guildID, userID, roleID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildMemberRole(guildID, userID, roleID), nil, EndpointGuildMemberRole(guildID, "", ""), options...)

	return
}

// GuildChannels returns an array of Channel structures for all channels of a
// given guild.
// guildID   : The ID of a Guild.
func (s *Session) GuildChannels(guildID string, options ...RequestOption) (st []*Channel, err error) {

	body, err := s.RequestRaw("GET", EndpointGuildChannels(guildID), "", nil, EndpointGuildChannels(guildID), 0, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildChannelCreateData is provided to GuildChannelCreateComplex
type GuildChannelCreateData struct {
	Name                 string                 `json:"name"`
	Type                 ChannelType            `json:"type"`
	Topic                string                 `json:"topic,omitempty"`
	Bitrate              int                    `json:"bitrate,omitempty"`
	UserLimit            int                    `json:"user_limit,omitempty"`
	RateLimitPerUser     int                    `json:"rate_limit_per_user,omitempty"`
	Position             int                    `json:"position,omitempty"`
	PermissionOverwrites []*PermissionOverwrite `json:"permission_overwrites,omitempty"`
	ParentID             string                 `json:"parent_id,omitempty"`
	NSFW                 bool                   `json:"nsfw,omitempty"`
}

// GuildChannelCreateComplex creates a new channel in the given guild
// guildID      : The ID of a Guild
// data         : A data struct describing the new Channel, Name and Type are mandatory, other fields depending on the type
func (s *Session) GuildChannelCreateComplex(guildID string, data GuildChannelCreateData, options ...RequestOption) (st *Channel, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointGuildChannels(guildID), data, EndpointGuildChannels(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildChannelCreate creates a new channel in the given guild
// guildID   : The ID of a Guild.
// name      : Name of the channel (2-100 chars length)
// ctype     : Type of the channel
func (s *Session) GuildChannelCreate(guildID, name string, ctype ChannelType, options ...RequestOption) (st *Channel, err error) {
	return s.GuildChannelCreateComplex(guildID, GuildChannelCreateData{
		Name: name,
		Type: ctype,
	}, options...)
}

// GuildChannelsReorder updates the order of channels in a guild
// guildID   : The ID of a Guild.
// channels  : Updated channels.
func (s *Session) GuildChannelsReorder(guildID string, channels []*Channel, options ...RequestOption) (err error) {

	data := make([]struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
	}, len(channels))

	for i, c := range channels {
		data[i].ID = c.ID
		data[i].Position = c.Position
	}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildChannels(guildID), data, EndpointGuildChannels(guildID), options...)
	return
}

// GuildInvites returns an array of Invite structures for the given guild
// guildID   : The ID of a Guild.
func (s *Session) GuildInvites(guildID string, options ...RequestOption) (st []*Invite, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointGuildInvites(guildID), nil, EndpointGuildInvites(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildRoles returns all roles for a given guild.
// guildID   : The ID of a Guild.
func (s *Session) GuildRoles(guildID string, options ...RequestOption) (st []*Role, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildRoles(guildID), nil, EndpointGuildRoles(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return // TODO return pointer
}

// GuildRole returns a specific role for a given guild.
// guildID   : The ID of a Guild.
// roleID    : The ID of a Role.
func (s *Session) GuildRole(guildID, roleID string, options ...RequestOption) (st *Role, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointGuildRole(guildID, roleID), nil, EndpointGuildRole(guildID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildRoleCreate creates a new Guild Role and returns it.
// guildID : The ID of a Guild.
// data    : New Role parameters.
func (s *Session) GuildRoleCreate(guildID string, data *RoleParams, options ...RequestOption) (st *Role, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointGuildRoles(guildID), data, EndpointGuildRoles(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildRoleEdit updates an existing Guild Role and returns updated Role data.
// guildID   : The ID of a Guild.
// roleID    : The ID of a Role.
// data 		 : Updated Role data.
func (s *Session) GuildRoleEdit(guildID, roleID string, data *RoleParams, options ...RequestOption) (st *Role, err error) {

	// Prevent sending a color int that is too big.
	if data.Color != nil && *data.Color > 0xFFFFFF {
		return nil, fmt.Errorf("color value cannot be larger than 0xFFFFFF")
	}

	body, err := s.RequestWithBucketID("PATCH", EndpointGuildRole(guildID, roleID), data, EndpointGuildRole(guildID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildRoleReorder reoders guild roles
// guildID   : The ID of a Guild.
// roles     : A list of ordered roles.
func (s *Session) GuildRoleReorder(guildID string, roles []*Role, options ...RequestOption) (st []*Role, err error) {

	body, err := s.RequestWithBucketID("PATCH", EndpointGuildRoles(guildID), roles, EndpointGuildRoles(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildRoleDelete deletes an existing role.
// guildID   : The ID of a Guild.
// roleID    : The ID of a Role.
func (s *Session) GuildRoleDelete(guildID, roleID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildRole(guildID, roleID), nil, EndpointGuildRole(guildID, ""), options...)

	return
}

// GuildRoleMemberCounts returns a map of role ID to the number of members that have that role.
//
// guildID	: The ID of a Guild.
//
// Does not include the @everyone role.
func (s *Session) GuildRoleMemberCounts(guildID string, options ...RequestOption) (memberCounts map[string]uint64, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointGuildRoleMemberCounts(guildID), nil, EndpointGuildRoleMemberCounts(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &memberCounts)
	return
}

// GuildPruneCount Returns the number of members that would be removed in a prune operation.
// Requires 'KICK_MEMBER' permission.
// guildID	: The ID of a Guild.
// days		: The number of days to count prune for (1 or more).
func (s *Session) GuildPruneCount(guildID string, days uint32, options ...RequestOption) (count uint32, err error) {
	count = 0

	if days <= 0 {
		err = ErrPruneDaysBounds
		return
	}

	p := struct {
		Pruned uint32 `json:"pruned"`
	}{}

	uri := EndpointGuildPrune(guildID) + "?days=" + strconv.FormatUint(uint64(days), 10)
	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildPrune(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &p)
	if err != nil {
		return
	}

	count = p.Pruned

	return
}

// GuildPrune Begin as prune operation. Requires the 'KICK_MEMBERS' permission.
// Returns an object with one 'pruned' key indicating the number of members that were removed in the prune operation.
// guildID	: The ID of a Guild.
// days		: The number of days to count prune for (1 or more).
func (s *Session) GuildPrune(guildID string, days uint32, options ...RequestOption) (count uint32, err error) {

	count = 0

	if days <= 0 {
		err = ErrPruneDaysBounds
		return
	}

	data := struct {
		days uint32
	}{days}

	p := struct {
		Pruned uint32 `json:"pruned"`
	}{}

	body, err := s.RequestWithBucketID("POST", EndpointGuildPrune(guildID), data, EndpointGuildPrune(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &p)
	if err != nil {
		return
	}

	count = p.Pruned

	return
}

// GuildIntegrations returns an array of Integrations for a guild.
// guildID   : The ID of a Guild.
func (s *Session) GuildIntegrations(guildID string, options ...RequestOption) (st []*Integration, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildIntegrations(guildID), nil, EndpointGuildIntegrations(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildIntegrationCreate creates a Guild Integration.
// guildID          : The ID of a Guild.
// integrationType  : The Integration type.
// integrationID    : The ID of an integration.
func (s *Session) GuildIntegrationCreate(guildID, integrationType, integrationID string, options ...RequestOption) (err error) {

	data := struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{integrationType, integrationID}

	_, err = s.RequestWithBucketID("POST", EndpointGuildIntegrations(guildID), data, EndpointGuildIntegrations(guildID), options...)
	return
}

// GuildIntegrationEdit edits a Guild Integration.
// guildID              : The ID of a Guild.
// integrationType      : The Integration type.
// integrationID        : The ID of an integration.
// expireBehavior	      : The behavior when an integration subscription lapses (see the integration object documentation).
// expireGracePeriod    : Period (in seconds) where the integration will ignore lapsed subscriptions.
// enableEmoticons	    : Whether emoticons should be synced for this integration (twitch only currently).
func (s *Session) GuildIntegrationEdit(guildID, integrationID string, expireBehavior, expireGracePeriod int, enableEmoticons bool, options ...RequestOption) (err error) {

	data := struct {
		ExpireBehavior    int  `json:"expire_behavior"`
		ExpireGracePeriod int  `json:"expire_grace_period"`
		EnableEmoticons   bool `json:"enable_emoticons"`
	}{expireBehavior, expireGracePeriod, enableEmoticons}

	_, err = s.RequestWithBucketID("PATCH", EndpointGuildIntegration(guildID, integrationID), data, EndpointGuildIntegration(guildID, ""), options...)
	return
}

// GuildIntegrationDelete removes the given integration from the Guild.
// guildID          : The ID of a Guild.
// integrationID    : The ID of an integration.
func (s *Session) GuildIntegrationDelete(guildID, integrationID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildIntegration(guildID, integrationID), nil, EndpointGuildIntegration(guildID, ""), options...)
	return
}

// GuildIntegrationSync syncs an integration.
// guildID          : The ID of a Guild.
// integrationID    : The ID of an integration.
func (s *Session) GuildIntegrationSync(guildID, integrationID string) (err error) {

	_, err = s.RequestWithBucketID("POST", EndpointGuildIntegrationSync(guildID, integrationID), nil, EndpointGuildIntegration(guildID, ""))
	return
}

// GuildIcon returns an image.Image of a guild icon.
// guildID   : The ID of a Guild.
func (s *Session) GuildIcon(guildID string, options ...RequestOption) (img image.Image, err error) {
	g, err := s.Guild(guildID, options...)
	if err != nil {
		return
	}

	if g.Icon == "" {
		err = ErrGuildNoIcon
		return
	}

	body, err := s.RequestWithBucketID("GET", EndpointGuildIcon(guildID, g.Icon), nil, EndpointGuildIcon(guildID, ""), options...)
	if err != nil {
		return
	}

	img, _, err = image.Decode(bytes.NewReader(body))
	return
}

// GuildSplash returns an image.Image of a guild splash image.
// guildID   : The ID of a Guild.
func (s *Session) GuildSplash(guildID string, options ...RequestOption) (img image.Image, err error) {
	g, err := s.Guild(guildID, options...)
	if err != nil {
		return
	}

	if g.Splash == "" {
		err = ErrGuildNoSplash
		return
	}

	body, err := s.RequestWithBucketID("GET", EndpointGuildSplash(guildID, g.Splash), nil, EndpointGuildSplash(guildID, ""), options...)
	if err != nil {
		return
	}

	img, _, err = image.Decode(bytes.NewReader(body))
	return
}

// GuildEmbed returns the embed for a Guild.
// guildID   : The ID of a Guild.
func (s *Session) GuildEmbed(guildID string, options ...RequestOption) (st *GuildEmbed, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildEmbed(guildID), nil, EndpointGuildEmbed(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildEmbedEdit edits the embed of a Guild.
// guildID   : The ID of a Guild.
// data      : New GuildEmbed data.
func (s *Session) GuildEmbedEdit(guildID string, data *GuildEmbed, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("PATCH", EndpointGuildEmbed(guildID), data, EndpointGuildEmbed(guildID), options...)
	return
}

// GuildAuditLog returns the audit log for a Guild.
// guildID     : The ID of a Guild.
// userID      : If provided the log will be filtered for the given ID.
// beforeID    : If provided all log entries returned will be before the given ID.
// actionType  : If provided the log will be filtered for the given Action Type.
// limit       : The number messages that can be returned. (default 50, min 1, max 100)
func (s *Session) GuildAuditLog(guildID, userID, beforeID string, actionType, limit int, options ...RequestOption) (st *GuildAuditLog, err error) {

	uri := EndpointGuildAuditLogs(guildID)

	v := url.Values{}
	if userID != "" {
		v.Set("user_id", userID)
	}
	if beforeID != "" {
		v.Set("before", beforeID)
	}
	if actionType > 0 {
		v.Set("action_type", strconv.Itoa(actionType))
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if len(v) > 0 {
		uri = fmt.Sprintf("%s?%s", uri, v.Encode())
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildAuditLogs(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildEmojis returns all emoji
// guildID : The ID of a Guild.
func (s *Session) GuildEmojis(guildID string, options ...RequestOption) (emoji []*Emoji, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildEmojis(guildID), nil, EndpointGuildEmojis(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// GuildEmoji returns specified emoji.
// guildID : The ID of a Guild
// emojiID : The ID of an Emoji to retrieve
func (s *Session) GuildEmoji(guildID, emojiID string, options ...RequestOption) (emoji *Emoji, err error) {
	var body []byte
	body, err = s.RequestWithBucketID("GET", EndpointGuildEmoji(guildID, emojiID), nil, EndpointGuildEmoji(guildID, emojiID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// GuildEmojiCreate creates a new Emoji.
// guildID : The ID of a Guild.
// data    : New Emoji data.
func (s *Session) GuildEmojiCreate(guildID string, data *EmojiParams, options ...RequestOption) (emoji *Emoji, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointGuildEmojis(guildID), data, EndpointGuildEmojis(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// GuildEmojiEdit modifies and returns updated Emoji.
// guildID : The ID of a Guild.
// emojiID : The ID of an Emoji.
// data    : Updated Emoji data.
func (s *Session) GuildEmojiEdit(guildID, emojiID string, data *EmojiParams, options ...RequestOption) (emoji *Emoji, err error) {
	body, err := s.RequestWithBucketID("PATCH", EndpointGuildEmoji(guildID, emojiID), data, EndpointGuildEmojis(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// GuildEmojiDelete deletes an Emoji.
// guildID : The ID of a Guild.
// emojiID : The ID of an Emoji.
func (s *Session) GuildEmojiDelete(guildID, emojiID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildEmoji(guildID, emojiID), nil, EndpointGuildEmojis(guildID), options...)
	return
}

// ApplicationEmojis returns all emojis for the given application
// appID : ID of the application
func (s *Session) ApplicationEmojis(appID string, options ...RequestOption) (emojis []*Emoji, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointApplicationEmojis(appID), nil, EndpointApplicationEmojis(appID), options...)
	if err != nil {
		return
	}

	var temp struct {
		Items []*Emoji `json:"items"`
	}

	err = unmarshal(body, &temp)
	if err != nil {
		return
	}

	emojis = temp.Items
	return
}

// ApplicationEmoji returns the emoji for the given application.
// appID   : ID of the application
// emojiID : ID of an Emoji to retrieve
func (s *Session) ApplicationEmoji(appID, emojiID string, options ...RequestOption) (emoji *Emoji, err error) {
	var body []byte
	body, err = s.RequestWithBucketID("GET", EndpointApplicationEmoji(appID, emojiID), nil, EndpointApplicationEmoji(appID, emojiID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// ApplicationEmojiCreate creates a new Emoji for the given application.
// appID : ID of the application
// data  : New Emoji data
func (s *Session) ApplicationEmojiCreate(appID string, data *EmojiParams, options ...RequestOption) (emoji *Emoji, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointApplicationEmojis(appID), data, EndpointApplicationEmojis(appID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// ApplicationEmojiEdit modifies and returns updated Emoji for the given application.
// appID   : ID of the application
// emojiID : ID of an Emoji
// data    : Updated Emoji data
func (s *Session) ApplicationEmojiEdit(appID string, emojiID string, data *EmojiParams, options ...RequestOption) (emoji *Emoji, err error) {
	body, err := s.RequestWithBucketID("PATCH", EndpointApplicationEmoji(appID, emojiID), data, EndpointApplicationEmojis(appID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &emoji)
	return
}

// ApplicationEmojiDelete deletes an Emoji for the given application.
// appID   : ID of the application
// emojiID : ID of an Emoji
func (s *Session) ApplicationEmojiDelete(appID, emojiID string, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("DELETE", EndpointApplicationEmoji(appID, emojiID), nil, EndpointApplicationEmojis(appID), options...)
	return
}

// GuildTemplate returns a GuildTemplate for the given code
// templateCode: The Code of a GuildTemplate
func (s *Session) GuildTemplate(templateCode string, options ...RequestOption) (st *GuildTemplate, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildTemplate(templateCode), nil, EndpointGuildTemplate(templateCode), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildCreateWithTemplate creates a guild based on a GuildTemplate
// templateCode: The Code of a GuildTemplate
// name: The name of the guild (2-100) characters
// icon: base64 encoded 128x128 image for the guild icon
func (s *Session) GuildCreateWithTemplate(templateCode, name, icon string, options ...RequestOption) (st *Guild, err error) {

	data := struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}{name, icon}

	body, err := s.RequestWithBucketID("POST", EndpointGuildTemplate(templateCode), data, EndpointGuildTemplate(templateCode), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildTemplates returns all of GuildTemplates
// guildID: The ID of the guild
func (s *Session) GuildTemplates(guildID string, options ...RequestOption) (st []*GuildTemplate, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildTemplates(guildID), nil, EndpointGuildTemplates(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildTemplateCreate creates a template for the guild
// guildID : The ID of the guild
// data    : Template metadata
func (s *Session) GuildTemplateCreate(guildID string, data *GuildTemplateParams, options ...RequestOption) (st *GuildTemplate, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointGuildTemplates(guildID), data, EndpointGuildTemplates(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildTemplateSync syncs the template to the guild's current state
// guildID: The ID of the guild
// templateCode: The code of the template
func (s *Session) GuildTemplateSync(guildID, templateCode string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("PUT", EndpointGuildTemplateSync(guildID, templateCode), nil, EndpointGuildTemplateSync(guildID, ""), options...)
	return
}

// GuildTemplateEdit modifies the template's metadata
// guildID      : The ID of the guild
// templateCode : The code of the template
// data         : New template metadata
func (s *Session) GuildTemplateEdit(guildID, templateCode string, data *GuildTemplateParams, options ...RequestOption) (st *GuildTemplate, err error) {

	body, err := s.RequestWithBucketID("PATCH", EndpointGuildTemplateSync(guildID, templateCode), data, EndpointGuildTemplateSync(guildID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildTemplateDelete deletes the template
// guildID: The ID of the guild
// templateCode: The code of the template
func (s *Session) GuildTemplateDelete(guildID, templateCode string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointGuildTemplateSync(guildID, templateCode), nil, EndpointGuildTemplateSync(guildID, ""), options...)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Channels
// ------------------------------------------------------------------------------------------------

// Channel returns a Channel structure of a specific Channel.
// channelID  : The ID of the Channel you want returned.
func (s *Session) Channel(channelID string, options ...RequestOption) (st *Channel, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointChannel(channelID), nil, EndpointChannel(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelEdit edits the given channel and returns the updated Channel data.
// channelID  : The ID of a Channel.
// data       : New Channel data.
func (s *Session) ChannelEdit(channelID string, data *ChannelEdit, options ...RequestOption) (st *Channel, err error) {
	body, err := s.RequestWithBucketID("PATCH", EndpointChannel(channelID), data, EndpointChannel(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return

}

// ChannelEditComplex edits an existing channel, replacing the parameters entirely with ChannelEdit struct
// NOTE: deprecated, use ChannelEdit instead
// channelID     : The ID of a Channel
// data          : The channel struct to send
func (s *Session) ChannelEditComplex(channelID string, data *ChannelEdit, options ...RequestOption) (st *Channel, err error) {
	return s.ChannelEdit(channelID, data, options...)
}

// ChannelDelete deletes the given channel
// channelID  : The ID of a Channel
func (s *Session) ChannelDelete(channelID string, options ...RequestOption) (st *Channel, err error) {

	body, err := s.RequestWithBucketID("DELETE", EndpointChannel(channelID), nil, EndpointChannel(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelTyping broadcasts to all members that authenticated user is typing in
// the given channel.
// channelID  : The ID of a Channel
func (s *Session) ChannelTyping(channelID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("POST", EndpointChannelTyping(channelID), nil, EndpointChannelTyping(channelID), options...)
	return
}

// ChannelMessages returns an array of Message structures for messages within
// a given channel.
// channelID : The ID of a Channel.
// limit     : The number messages that can be returned. (max 100)
// beforeID  : If provided all messages returned will be before given ID.
// afterID   : If provided all messages returned will be after given ID.
// aroundID  : If provided all messages returned will be around given ID.
func (s *Session) ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...RequestOption) (st []*Message, err error) {

	uri := EndpointChannelMessages(channelID)

	v := url.Values{}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if afterID != "" {
		v.Set("after", afterID)
	}
	if beforeID != "" {
		v.Set("before", beforeID)
	}
	if aroundID != "" {
		v.Set("around", aroundID)
	}
	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointChannelMessages(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelMessage gets a single message by ID from a given channel.
// channeld  : The ID of a Channel
// messageID : the ID of a Message
func (s *Session) ChannelMessage(channelID, messageID string, options ...RequestOption) (st *Message, err error) {

	response, err := s.RequestWithBucketID("GET", EndpointChannelMessage(channelID, messageID), nil, EndpointChannelMessage(channelID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(response, &st)
	return
}

// ChannelMessageAck acknowledges and marks the given message as read
// channeld  : The ID of a Channel
// messageID : the ID of a Message
// lastToken : token returned by last ack
func (s *Session) ChannelMessageAck(channelID, messageID, lastToken string) (st *Ack, err error) {

	body, err := s.RequestWithBucketID("POST", EndpointChannelMessageAck(channelID, messageID), &Ack{Token: lastToken}, EndpointChannelMessageAck(channelID, ""))
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelMessageAckNoToken acknowledges and marks the given message as read without a token
// channeld  : The ID of a Channel
// messageID : the ID of a Message
func (s *Session) ChannelMessageAckNoToken(channelID, messageID string, options ...RequestOption) (st *PtrAck, err error) {

	body, err := s.RequestWithBucketID("POST", EndpointChannelMessageAck(channelID, messageID), &PtrAck{}, EndpointChannelMessageAck(channelID, ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelMessageSend sends a message to the given channel.
// channelID : The ID of a Channel.
// content   : The message to send.
func (s *Session) ChannelMessageSend(channelID string, content string, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendComplex(channelID, &MessageSend{
		Content: content,
	}, options...)
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// ChannelMessageSendComplex sends a message to the given channel.
// channelID : The ID of a Channel.
// data      : The message struct to send.
func (s *Session) ChannelMessageSendComplex(channelID string, data *MessageSend, options ...RequestOption) (st *Message, err error) {
	// TODO: Remove this when compatibility is not required.
	if data.Embed != nil {
		if data.Embeds == nil {
			data.Embeds = []*MessageEmbed{data.Embed}
		} else {
			err = fmt.Errorf("cannot specify both Embed and Embeds")
			return
		}
	}

	for _, embed := range data.Embeds {
		if embed.Type == "" {
			embed.Type = "rich"
		}
	}
	endpoint := EndpointChannelMessages(channelID)

	// TODO: Remove this when compatibility is not required.
	files := data.Files
	if data.File != nil {
		if files == nil {
			files = []*File{data.File}
		} else {
			err = fmt.Errorf("cannot specify both File and Files")
			return
		}
	}

	if data.StickerIDs != nil {
		if len(*data.StickerIDs) > 3 {
			err = fmt.Errorf("cannot send more than 3 stickers")
			return
		}
	}

	var response []byte
	if len(files) > 0 {
		contentType, body, encodeErr := MultipartBodyWithJSON(data, files)
		if encodeErr != nil {
			return st, encodeErr
		}
		response, err = s.RequestRaw("POST", endpoint, contentType, body, endpoint, 0, options...)
	} else {
		if s.IsUser {
			if data.Attachments != nil {
				zero := 0
				data.Type = &zero
				if data.StickerIDs == nil {
					emptyArr := make([]string, 0)
					data.StickerIDs = &emptyArr
				}
			} else {
				var zero MessageFlags
				data.Flags = &zero
				data.MobileNetworkType = "unknown"
				if data.TTS == nil {
					var falseVal bool
					data.TTS = &falseVal
				}
			}
			options = append(options, WithContextProperties("chat_input"))
		}
		response, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	}
	if err != nil {
		return
	}

	err = unmarshal(response, &st)
	return
}

func (s *Session) ChannelAttachmentCreate(channelID string, data *ReqPrepareAttachments, options ...RequestOption) (st *RespPrepareAttachments, err error) {
	endpoint := EndpointChannelAttachments(channelID)

	var response []byte
	response, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(response, &st)
	return
}

// ChannelMessageSendTTS sends a message to the given channel with Text to Speech.
// channelID : The ID of a Channel.
// content   : The message to send.
func (s *Session) ChannelMessageSendTTS(channelID string, content string, options ...RequestOption) (*Message, error) {
	trueVal := true
	return s.ChannelMessageSendComplex(channelID, &MessageSend{
		Content: content,
		TTS:     &trueVal,
	}, options...)
}

// ChannelMessageSendEmbed sends a message to the given channel with embedded data.
// channelID : The ID of a Channel.
// embed     : The embed data to send.
func (s *Session) ChannelMessageSendEmbed(channelID string, embed *MessageEmbed, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendEmbeds(channelID, []*MessageEmbed{embed}, options...)
}

// ChannelMessageSendEmbeds sends a message to the given channel with multiple embedded data.
// channelID : The ID of a Channel.
// embeds    : The embeds data to send.
func (s *Session) ChannelMessageSendEmbeds(channelID string, embeds []*MessageEmbed, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendComplex(channelID, &MessageSend{
		Embeds: embeds,
	}, options...)
}

// ChannelMessageSendReply sends a message to the given channel with reference data.
// channelID : The ID of a Channel.
// content   : The message to send.
// reference : The message reference to send.
func (s *Session) ChannelMessageSendReply(channelID string, content string, reference *MessageReference, options ...RequestOption) (*Message, error) {
	if reference == nil {
		return nil, fmt.Errorf("reply attempted with nil message reference")
	}
	return s.ChannelMessageSendComplex(channelID, &MessageSend{
		Content:   content,
		Reference: reference,
	}, options...)
}

// ChannelMessageSendEmbedReply sends a message to the given channel with reference data and embedded data.
// channelID : The ID of a Channel.
// embed   : The embed data to send.
// reference : The message reference to send.
func (s *Session) ChannelMessageSendEmbedReply(channelID string, embed *MessageEmbed, reference *MessageReference, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendEmbedsReply(channelID, []*MessageEmbed{embed}, reference, options...)
}

// ChannelMessageSendEmbedsReply sends a message to the given channel with reference data and multiple embedded data.
// channelID : The ID of a Channel.
// embeds    : The embeds data to send.
// reference : The message reference to send.
func (s *Session) ChannelMessageSendEmbedsReply(channelID string, embeds []*MessageEmbed, reference *MessageReference, options ...RequestOption) (*Message, error) {
	if reference == nil {
		return nil, fmt.Errorf("reply attempted with nil message reference")
	}
	return s.ChannelMessageSendComplex(channelID, &MessageSend{
		Embeds:    embeds,
		Reference: reference,
	}, options...)
}

// ChannelMessageEdit edits an existing message, replacing it entirely with
// the given content.
// channelID  : The ID of a Channel
// messageID  : The ID of a Message
// content    : The contents of the message
func (s *Session) ChannelMessageEdit(channelID, messageID, content string, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageEditComplex(NewMessageEdit(channelID, messageID).SetContent(content), options...)
}

// ChannelMessageEditComplex edits an existing message, replacing it entirely with
// the given MessageEdit struct
func (s *Session) ChannelMessageEditComplex(m *MessageEdit, options ...RequestOption) (st *Message, err error) {
	// TODO: Remove this when compatibility is not required.
	if m.Embed != nil {
		if m.Embeds == nil {
			m.Embeds = &[]*MessageEmbed{m.Embed}
		} else {
			err = fmt.Errorf("cannot specify both Embed and Embeds")
			return
		}
	}

	if m.Embeds != nil {
		for _, embed := range *m.Embeds {
			if embed.Type == "" {
				embed.Type = "rich"
			}
		}
	}

	endpoint := EndpointChannelMessage(m.Channel, m.ID)

	var response []byte
	if len(m.Files) > 0 {
		contentType, body, encodeErr := MultipartBodyWithJSON(m, m.Files)
		if encodeErr != nil {
			return st, encodeErr
		}
		response, err = s.RequestRaw("PATCH", endpoint, contentType, body, EndpointChannelMessage(m.Channel, ""), 0, options...)
	} else {
		response, err = s.RequestWithBucketID("PATCH", endpoint, m, EndpointChannelMessage(m.Channel, ""), options...)
	}
	if err != nil {
		return
	}

	err = unmarshal(response, &st)
	return
}

// ChannelMessageEditEmbed edits an existing message with embedded data.
// channelID : The ID of a Channel
// messageID : The ID of a Message
// embed     : The embed data to send
func (s *Session) ChannelMessageEditEmbed(channelID, messageID string, embed *MessageEmbed, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageEditEmbeds(channelID, messageID, []*MessageEmbed{embed}, options...)
}

// ChannelMessageEditEmbeds edits an existing message with multiple embedded data.
// channelID : The ID of a Channel
// messageID : The ID of a Message
// embeds    : The embeds data to send
func (s *Session) ChannelMessageEditEmbeds(channelID, messageID string, embeds []*MessageEmbed, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageEditComplex(NewMessageEdit(channelID, messageID).SetEmbeds(embeds), options...)
}

// ChannelMessageDelete deletes a message from the Channel.
func (s *Session) ChannelMessageDelete(channelID, messageID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointChannelMessage(channelID, messageID), nil, EndpointChannelMessage(channelID, ""), options...)
	return
}

// ChannelMessagesBulkDelete bulk deletes the messages from the channel for the provided messageIDs.
// If only one messageID is in the slice call channelMessageDelete function.
// If the slice is empty do nothing.
// channelID : The ID of the channel for the messages to delete.
// messages  : The IDs of the messages to be deleted. A slice of string IDs. A maximum of 100 messages.
func (s *Session) ChannelMessagesBulkDelete(channelID string, messages []string, options ...RequestOption) (err error) {

	if len(messages) == 0 {
		return
	}

	if len(messages) == 1 {
		err = s.ChannelMessageDelete(channelID, messages[0], options...)
		return
	}

	if len(messages) > 100 {
		messages = messages[:100]
	}

	data := struct {
		Messages []string `json:"messages"`
	}{messages}

	_, err = s.RequestWithBucketID("POST", EndpointChannelMessagesBulkDelete(channelID), data, EndpointChannelMessagesBulkDelete(channelID), options...)
	return
}

// ChannelMessagePin pins a message within a given channel.
// channelID: The ID of a channel.
// messageID: The ID of a message.
func (s *Session) ChannelMessagePin(channelID, messageID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("PUT", EndpointChannelMessagePin(channelID, messageID), nil, EndpointChannelMessagePin(channelID, ""), options...)
	return
}

// ChannelMessageUnpin unpins a message within a given channel.
// channelID: The ID of a channel.
// messageID: The ID of a message.
func (s *Session) ChannelMessageUnpin(channelID, messageID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointChannelMessagePin(channelID, messageID), nil, EndpointChannelMessagePin(channelID, ""), options...)
	return
}

// ChannelMessagesPinned returns an array of Message structures for pinned messages
// within a given channel
// channelID : The ID of a Channel.
// before : If specified returns only pinned messages before the timestamp
// limit  : Optional maximum amount of pinned messages to return.
func (s *Session) ChannelMessagesPinned(channelID string, before *time.Time, limit int, options ...RequestOption) (pinnedMessages *ChannelMessagePinsList, err error) {
	uri := EndpointChannelMessagesPins(channelID)

	v := url.Values{}

	if before != nil {
		v.Set("before", before.Format(time.RFC3339))
	}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, uri, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &pinnedMessages)
	return
}

// ChannelFileSend sends a file to the given channel.
// channelID : The ID of a Channel.
// name: The name of the file.
// io.Reader : A reader for the file contents.
func (s *Session) ChannelFileSend(channelID, name string, r io.Reader, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendComplex(channelID, &MessageSend{File: &File{Name: name, Reader: r}}, options...)
}

// ChannelFileSendWithMessage sends a file to the given channel with an message.
// DEPRECATED. Use ChannelMessageSendComplex instead.
// channelID : The ID of a Channel.
// content: Optional Message content.
// name: The name of the file.
// io.Reader : A reader for the file contents.
func (s *Session) ChannelFileSendWithMessage(channelID, content string, name string, r io.Reader, options ...RequestOption) (*Message, error) {
	return s.ChannelMessageSendComplex(channelID, &MessageSend{File: &File{Name: name, Reader: r}, Content: content}, options...)
}

// ChannelInvites returns an array of Invite structures for the given channel
// channelID   : The ID of a Channel
func (s *Session) ChannelInvites(channelID string, options ...RequestOption) (st []*Invite, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointChannelInvites(channelID), nil, EndpointChannelInvites(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelInviteCreate creates a new invite for the given channel.
// channelID   : The ID of a Channel
// i           : An Invite struct with the values MaxAge, MaxUses and Temporary defined.
func (s *Session) ChannelInviteCreate(channelID string, i Invite, options ...RequestOption) (st *Invite, err error) {

	data := struct {
		MaxAge    int  `json:"max_age"`
		MaxUses   int  `json:"max_uses"`
		Temporary bool `json:"temporary"`
		Unique    bool `json:"unique"`
	}{i.MaxAge, i.MaxUses, i.Temporary, i.Unique}

	body, err := s.RequestWithBucketID("POST", EndpointChannelInvites(channelID), data, EndpointChannelInvites(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelPermissionSet creates a Permission Override for the given channel.
// NOTE: This func name may changed.  Using Set instead of Create because
// you can both create a new override or update an override with this function.
func (s *Session) ChannelPermissionSet(channelID, targetID string, targetType PermissionOverwriteType, allow, deny int64, options ...RequestOption) (err error) {

	data := struct {
		ID    string                  `json:"id"`
		Type  PermissionOverwriteType `json:"type"`
		Allow int64                   `json:"allow,string"`
		Deny  int64                   `json:"deny,string"`
	}{targetID, targetType, allow, deny}

	_, err = s.RequestWithBucketID("PUT", EndpointChannelPermission(channelID, targetID), data, EndpointChannelPermission(channelID, ""), options...)
	return
}

// ChannelPermissionDelete deletes a specific permission override for the given channel.
// NOTE: Name of this func may change.
func (s *Session) ChannelPermissionDelete(channelID, targetID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointChannelPermission(channelID, targetID), nil, EndpointChannelPermission(channelID, ""), options...)
	return
}

// ChannelMessageCrosspost cross posts a message in a news channel to followers
// of the channel
// channelID   : The ID of a Channel
// messageID   : The ID of a Message
func (s *Session) ChannelMessageCrosspost(channelID, messageID string, options ...RequestOption) (st *Message, err error) {

	endpoint := EndpointChannelMessageCrosspost(channelID, messageID)

	body, err := s.RequestWithBucketID("POST", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ChannelNewsFollow follows a news channel in the targetID
// channelID   : The ID of a News Channel
// targetID    : The ID of a Channel where the News Channel should post to
func (s *Session) ChannelNewsFollow(channelID, targetID string, options ...RequestOption) (st *ChannelFollow, err error) {

	endpoint := EndpointChannelFollow(channelID)

	data := struct {
		WebhookChannelID string `json:"webhook_channel_id"`
	}{targetID}

	body, err := s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Invites
// ------------------------------------------------------------------------------------------------

// Invite returns an Invite structure of the given invite
// inviteID : The invite code
func (s *Session) Invite(inviteID string, options ...RequestOption) (st *Invite, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointInvite(inviteID), nil, EndpointInvite(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// InviteWithCounts returns an Invite structure of the given invite including approximate member counts
// inviteID : The invite code
func (s *Session) InviteWithCounts(inviteID string, options ...RequestOption) (st *Invite, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointInvite(inviteID)+"?with_counts=true", nil, EndpointInvite(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// InviteComplex returns an Invite structure of the given invite including specified fields.
// inviteID                  : The invite code
// guildScheduledEventID     : If specified, includes specified guild scheduled event.
// withCounts                : Whether to include approximate member counts or not
// withExpiration            : Whether to include expiration time or not
func (s *Session) InviteComplex(inviteID, guildScheduledEventID string, withCounts, withExpiration bool, options ...RequestOption) (st *Invite, err error) {
	endpoint := EndpointInvite(inviteID)
	v := url.Values{}
	if guildScheduledEventID != "" {
		v.Set("guild_scheduled_event_id", guildScheduledEventID)
	}
	if withCounts {
		v.Set("with_counts", "true")
	}
	if withExpiration {
		v.Set("with_expiration", "true")
	}

	if len(v) != 0 {
		endpoint += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", endpoint, nil, EndpointInvite(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// InviteDelete deletes an existing invite
// inviteID   : the code of an invite
func (s *Session) InviteDelete(inviteID string, options ...RequestOption) (st *Invite, err error) {

	body, err := s.RequestWithBucketID("DELETE", EndpointInvite(inviteID), nil, EndpointInvite(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// InviteAccept accepts an Invite to a Guild or Channel
// inviteID : The invite code
func (s *Session) InviteAccept(inviteID string, options ...RequestOption) (st *Invite, err error) {

	body, err := s.RequestWithBucketID("POST", EndpointInvite(inviteID), nil, EndpointInvite(""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Voice
// ------------------------------------------------------------------------------------------------

// VoiceRegions returns the voice server regions
func (s *Session) VoiceRegions(options ...RequestOption) (st []*VoiceRegion, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointVoiceRegions, nil, EndpointVoiceRegions, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// VoiceICE returns the voice server ICE information
func (s *Session) VoiceICE() (st *VoiceICE, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointVoiceIce, nil, EndpointVoiceIce)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Websockets
// ------------------------------------------------------------------------------------------------

// Gateway returns the websocket Gateway address
func (s *Session) Gateway(options ...RequestOption) (gateway string, err error) {

	response, err := s.RequestWithBucketID("GET", EndpointGateway, nil, EndpointGateway, options...)
	if err != nil {
		return
	}

	temp := struct {
		URL string `json:"url"`
	}{}

	err = unmarshal(response, &temp)
	if err != nil {
		return
	}

	gateway = temp.URL

	// Ensure the gateway always has a trailing slash.
	// MacOS will fail to connect if we add query params without a trailing slash on the base domain.
	if !strings.HasSuffix(gateway, "/") {
		gateway += "/"
	}

	return
}

// GatewayBot returns the websocket Gateway address and the recommended number of shards
func (s *Session) GatewayBot(options ...RequestOption) (st *GatewayBotResponse, err error) {

	response, err := s.RequestWithBucketID("GET", EndpointGatewayBot, nil, EndpointGatewayBot, options...)
	if err != nil {
		return
	}

	err = unmarshal(response, &st)
	if err != nil {
		return
	}

	// Ensure the gateway always has a trailing slash.
	// MacOS will fail to connect if we add query params without a trailing slash on the base domain.
	if !strings.HasSuffix(st.URL, "/") {
		st.URL += "/"
	}

	return
}

// Functions specific to Webhooks

// WebhookCreate returns a new Webhook.
// channelID: The ID of a Channel.
// name     : The name of the webhook.
// avatar   : The avatar of the webhook.
func (s *Session) WebhookCreate(channelID, name, avatar string, options ...RequestOption) (st *Webhook, err error) {

	data := struct {
		Name   string `json:"name"`
		Avatar string `json:"avatar,omitempty"`
	}{name, avatar}

	body, err := s.RequestWithBucketID("POST", EndpointChannelWebhooks(channelID), data, EndpointChannelWebhooks(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// ChannelWebhooks returns all webhooks for a given channel.
// channelID: The ID of a channel.
func (s *Session) ChannelWebhooks(channelID string, options ...RequestOption) (st []*Webhook, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointChannelWebhooks(channelID), nil, EndpointChannelWebhooks(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// GuildWebhooks returns all webhooks for a given guild.
// guildID: The ID of a Guild.
func (s *Session) GuildWebhooks(guildID string, options ...RequestOption) (st []*Webhook, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointGuildWebhooks(guildID), nil, EndpointGuildWebhooks(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// Webhook returns a webhook for a given ID
// webhookID: The ID of a webhook.
func (s *Session) Webhook(webhookID string, options ...RequestOption) (st *Webhook, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointWebhook(webhookID), nil, EndpointWebhooks, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// WebhookWithToken returns a webhook for a given ID
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook.
func (s *Session) WebhookWithToken(webhookID, token string, options ...RequestOption) (st *Webhook, err error) {

	body, err := s.RequestWithBucketID("GET", EndpointWebhookToken(webhookID, token), nil, EndpointWebhookToken("", ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// WebhookEdit updates an existing Webhook.
// webhookID: The ID of a webhook.
// name     : The name of the webhook.
// avatar   : The avatar of the webhook.
func (s *Session) WebhookEdit(webhookID, name, avatar, channelID string, options ...RequestOption) (st *Webhook, err error) {

	data := struct {
		Name      string `json:"name,omitempty"`
		Avatar    string `json:"avatar,omitempty"`
		ChannelID string `json:"channel_id,omitempty"`
	}{name, avatar, channelID}

	body, err := s.RequestWithBucketID("PATCH", EndpointWebhook(webhookID), data, EndpointWebhooks, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// WebhookEditWithToken updates an existing Webhook with an auth token.
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook.
// name     : The name of the webhook.
// avatar   : The avatar of the webhook.
func (s *Session) WebhookEditWithToken(webhookID, token, name, avatar string, options ...RequestOption) (st *Webhook, err error) {

	data := struct {
		Name   string `json:"name,omitempty"`
		Avatar string `json:"avatar,omitempty"`
	}{name, avatar}

	var body []byte
	body, err = s.RequestWithBucketID("PATCH", EndpointWebhookToken(webhookID, token), data, EndpointWebhookToken("", ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)

	return
}

// WebhookDelete deletes a webhook for a given ID
// webhookID: The ID of a webhook.
func (s *Session) WebhookDelete(webhookID string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointWebhook(webhookID), nil, EndpointWebhooks, options...)

	return
}

// WebhookDeleteWithToken deletes a webhook for a given ID with an auth token.
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook.
func (s *Session) WebhookDeleteWithToken(webhookID, token string, options ...RequestOption) (err error) {

	_, err = s.RequestWithBucketID("DELETE", EndpointWebhookToken(webhookID, token), nil, EndpointWebhookToken("", ""), options...)

	return
}

func (s *Session) webhookExecute(webhookID, token string, wait bool, threadID string, data *WebhookParams, options ...RequestOption) (st *Message, err error) {
	uri := EndpointWebhookToken(webhookID, token)

	v := url.Values{}
	if wait {
		v.Set("wait", "true")
	}

	if threadID != "" {
		v.Set("thread_id", threadID)
	}
	if len(v) != 0 {
		uri += "?" + v.Encode()
	}

	var response []byte
	if len(data.Files) > 0 {
		contentType, body, encodeErr := MultipartBodyWithJSON(data, data.Files)
		if encodeErr != nil {
			return st, encodeErr
		}

		response, err = s.RequestRaw("POST", uri, contentType, body, uri, 0, options...)
	} else {
		response, err = s.RequestWithBucketID("POST", uri, data, uri, options...)
	}
	if !wait || err != nil {
		return
	}

	err = unmarshal(response, &st)
	return
}

// WebhookExecute executes a webhook.
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook
// wait     : Waits for server confirmation of message send and ensures that the return struct is populated (it is nil otherwise)
func (s *Session) WebhookExecute(webhookID, token string, wait bool, data *WebhookParams, options ...RequestOption) (st *Message, err error) {
	return s.webhookExecute(webhookID, token, wait, "", data, options...)
}

// WebhookThreadExecute executes a webhook in a thread.
// webhookID: The ID of a webhook.
// token    : The auth token for the webhook
// wait     : Waits for server confirmation of message send and ensures that the return struct is populated (it is nil otherwise)
// threadID :	Sends a message to the specified thread within a webhook's channel. The thread will automatically be unarchived.
func (s *Session) WebhookThreadExecute(webhookID, token string, wait bool, threadID string, data *WebhookParams, options ...RequestOption) (st *Message, err error) {
	return s.webhookExecute(webhookID, token, wait, threadID, data, options...)
}

// WebhookMessage gets a webhook message.
// webhookID : The ID of a webhook
// token     : The auth token for the webhook
// messageID : The ID of message to get
func (s *Session) WebhookMessage(webhookID, token, messageID string, options ...RequestOption) (message *Message, err error) {
	uri := EndpointWebhookMessage(webhookID, token, messageID)

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointWebhookToken("", ""), options...)
	if err != nil {
		return
	}

	err = Unmarshal(body, &message)

	return
}

// WebhookMessageEdit edits a webhook message and returns a new one.
// webhookID : The ID of a webhook
// token     : The auth token for the webhook
// messageID : The ID of message to edit
func (s *Session) WebhookMessageEdit(webhookID, token, messageID string, data *WebhookEdit, options ...RequestOption) (st *Message, err error) {
	uri := EndpointWebhookMessage(webhookID, token, messageID)

	var response []byte
	if len(data.Files) > 0 {
		contentType, body, err := MultipartBodyWithJSON(data, data.Files)
		if err != nil {
			return nil, err
		}

		response, err = s.RequestRaw("PATCH", uri, contentType, body, uri, 0, options...)
		if err != nil {
			return nil, err
		}
	} else {
		response, err = s.RequestWithBucketID("PATCH", uri, data, EndpointWebhookToken("", ""), options...)

		if err != nil {
			return nil, err
		}
	}

	err = unmarshal(response, &st)
	return
}

// WebhookMessageDelete deletes a webhook message.
// webhookID : The ID of a webhook
// token     : The auth token for the webhook
// messageID : The ID of a message to edit
func (s *Session) WebhookMessageDelete(webhookID, token, messageID string, options ...RequestOption) (err error) {
	uri := EndpointWebhookMessage(webhookID, token, messageID)

	_, err = s.RequestWithBucketID("DELETE", uri, nil, EndpointWebhookToken("", ""), options...)
	return
}

// MessageReactionAdd creates an emoji reaction to a message.
// channelID : The channel ID.
// messageID : The message ID.
// emojiID   : Either the unicode emoji for the reaction, or a guild emoji identifier in name:id format (e.g. "hello:1234567654321")
func (s *Session) MessageReactionAdd(channelID, messageID, emojiID string, options ...RequestOption) error {

	// emoji such as  #⃣ need to have # escaped
	emojiID = strings.Replace(emojiID, "#", "%23", -1)
	_, err := s.RequestWithBucketID("PUT", EndpointMessageReaction(channelID, messageID, emojiID, "@me"), nil, EndpointMessageReaction(channelID, "", "", ""), options...)

	return err
}

// MessageReactionRemove deletes an emoji reaction to a message.
// channelID : The channel ID.
// messageID : The message ID.
// emojiID   : Either the unicode emoji for the reaction, or a guild emoji identifier.
// userID	 : @me or ID of the user to delete the reaction for.
func (s *Session) MessageReactionRemove(channelID, messageID, emojiID, userID string, options ...RequestOption) error {

	// emoji such as  #⃣ need to have # escaped
	emojiID = strings.Replace(emojiID, "#", "%23", -1)
	_, err := s.RequestWithBucketID("DELETE", EndpointMessageReaction(channelID, messageID, emojiID, userID), nil, EndpointMessageReaction(channelID, "", "", ""), options...)

	return err
}

// MessageReactionsRemoveAll deletes all reactions from a message
// channelID : The channel ID
// messageID : The message ID.
func (s *Session) MessageReactionsRemoveAll(channelID, messageID string, options ...RequestOption) error {

	_, err := s.RequestWithBucketID("DELETE", EndpointMessageReactionsAll(channelID, messageID), nil, EndpointMessageReactionsAll(channelID, messageID), options...)

	return err
}

// MessageReactionsRemoveEmoji deletes all reactions of a certain emoji from a message
// channelID : The channel ID
// messageID : The message ID
// emojiID   : The emoji ID
func (s *Session) MessageReactionsRemoveEmoji(channelID, messageID, emojiID string, options ...RequestOption) error {

	// emoji such as  #⃣ need to have # escaped
	emojiID = strings.Replace(emojiID, "#", "%23", -1)
	_, err := s.RequestWithBucketID("DELETE", EndpointMessageReactions(channelID, messageID, emojiID), nil, EndpointMessageReactions(channelID, messageID, emojiID), options...)

	return err
}

// MessageReactions gets all the users reactions for a specific emoji.
// channelID : The channel ID.
// messageID : The message ID.
// emojiID   : Either the unicode emoji for the reaction, or a guild emoji identifier.
// limit    : max number of users to return (max 100)
// beforeID  : If provided all reactions returned will be before given ID.
// afterID   : If provided all reactions returned will be after given ID.
func (s *Session) MessageReactions(channelID, messageID, emojiID string, limit int, beforeID, afterID string, options ...RequestOption) (st []*User, err error) {
	// emoji such as  #⃣ need to have # escaped
	emojiID = strings.Replace(emojiID, "#", "%23", -1)
	uri := EndpointMessageReactions(channelID, messageID, emojiID)

	v := url.Values{}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if afterID != "" {
		v.Set("after", afterID)
	}
	if beforeID != "" {
		v.Set("before", beforeID)
	}

	if len(v) > 0 {
		uri += "?" + v.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointMessageReaction(channelID, "", "", ""), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to threads
// ------------------------------------------------------------------------------------------------

// MessageThreadStartComplex creates a new thread from an existing message.
// channelID : Channel to create thread in
// messageID : Message to start thread from
// data : Parameters of the thread
func (s *Session) MessageThreadStartComplex(channelID, messageID string, data *ThreadStart, options ...RequestOption) (ch *Channel, err error) {
	endpoint := EndpointChannelMessageThread(channelID, messageID)
	var body []byte
	body, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &ch)
	return
}

// MessageThreadStart creates a new thread from an existing message.
// channelID       : Channel to create thread in
// messageID       : Message to start thread from
// name            : Name of the thread
// archiveDuration : Auto archive duration (in minutes)
func (s *Session) MessageThreadStart(channelID, messageID string, name string, archiveDuration int, options ...RequestOption) (ch *Channel, err error) {
	return s.MessageThreadStartComplex(channelID, messageID, &ThreadStart{
		Name:                name,
		AutoArchiveDuration: archiveDuration,
	}, options...)
}

// ThreadStartComplex creates a new thread.
// channelID : Channel to create thread in
// data : Parameters of the thread
func (s *Session) ThreadStartComplex(channelID string, data *ThreadStart, options ...RequestOption) (ch *Channel, err error) {
	endpoint := EndpointChannelThreads(channelID)
	var body []byte
	body, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &ch)
	return
}

// ThreadStart creates a new thread.
// channelID       : Channel to create thread in
// name            : Name of the thread
// archiveDuration : Auto archive duration (in minutes)
func (s *Session) ThreadStart(channelID, name string, typ ChannelType, archiveDuration int, options ...RequestOption) (ch *Channel, err error) {
	return s.ThreadStartComplex(channelID, &ThreadStart{
		Name:                name,
		Type:                typ,
		AutoArchiveDuration: archiveDuration,
	}, options...)
}

// ForumThreadStartComplex starts a new thread (creates a post) in a forum channel.
// channelID   : Channel to create thread in.
// threadData  : Parameters of the thread.
// messageData : Parameters of the starting message.
func (s *Session) ForumThreadStartComplex(channelID string, threadData *ThreadStart, messageData *MessageSend, options ...RequestOption) (th *Channel, err error) {
	endpoint := EndpointChannelThreads(channelID)

	// TODO: Remove this when compatibility is not required.
	if messageData.Embed != nil {
		if messageData.Embeds == nil {
			messageData.Embeds = []*MessageEmbed{messageData.Embed}
		} else {
			err = fmt.Errorf("cannot specify both Embed and Embeds")
			return
		}
	}

	for _, embed := range messageData.Embeds {
		if embed.Type == "" {
			embed.Type = "rich"
		}
	}

	// TODO: Remove this when compatibility is not required.
	files := messageData.Files
	if messageData.File != nil {
		if files == nil {
			files = []*File{messageData.File}
		} else {
			err = fmt.Errorf("cannot specify both File and Files")
			return
		}
	}

	data := struct {
		*ThreadStart
		Message *MessageSend `json:"message"`
	}{ThreadStart: threadData, Message: messageData}

	var response []byte
	if len(files) > 0 {
		contentType, body, encodeErr := MultipartBodyWithJSON(data, files)
		if encodeErr != nil {
			return th, encodeErr
		}

		response, err = s.RequestRaw("POST", endpoint, contentType, body, endpoint, 0, options...)
	} else {
		response, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	}
	if err != nil {
		return
	}

	err = unmarshal(response, &th)
	return
}

// ForumThreadStart starts a new thread (post) in a forum channel.
// channelID       : Channel to create thread in.
// name            : Name of the thread.
// archiveDuration : Auto archive duration.
// content         : Content of the starting message.
func (s *Session) ForumThreadStart(channelID, name string, archiveDuration int, content string, options ...RequestOption) (th *Channel, err error) {
	return s.ForumThreadStartComplex(channelID, &ThreadStart{
		Name:                name,
		AutoArchiveDuration: archiveDuration,
	}, &MessageSend{Content: content}, options...)
}

// ForumThreadStartEmbed starts a new thread (post) in a forum channel.
// channelID       : Channel to create thread in.
// name            : Name of the thread.
// archiveDuration : Auto archive duration.
// embed           : Embed data of the starting message.
func (s *Session) ForumThreadStartEmbed(channelID, name string, archiveDuration int, embed *MessageEmbed, options ...RequestOption) (th *Channel, err error) {
	return s.ForumThreadStartComplex(channelID, &ThreadStart{
		Name:                name,
		AutoArchiveDuration: archiveDuration,
	}, &MessageSend{Embeds: []*MessageEmbed{embed}}, options...)
}

// ForumThreadStartEmbeds starts a new thread (post) in a forum channel.
// channelID       : Channel to create thread in.
// name            : Name of the thread.
// archiveDuration : Auto archive duration.
// embeds          : Embeds data of the starting message.
func (s *Session) ForumThreadStartEmbeds(channelID, name string, archiveDuration int, embeds []*MessageEmbed, options ...RequestOption) (th *Channel, err error) {
	return s.ForumThreadStartComplex(channelID, &ThreadStart{
		Name:                name,
		AutoArchiveDuration: archiveDuration,
	}, &MessageSend{Embeds: embeds}, options...)
}

// ThreadJoin adds current user to a thread
func (s *Session) ThreadJoin(id string, options ...RequestOption) error {
	endpoint := EndpointThreadMember(id, "@me")
	_, err := s.RequestWithBucketID("PUT", endpoint, nil, endpoint, options...)
	return err
}

// ThreadLeave removes current user to a thread
func (s *Session) ThreadLeave(id string, options ...RequestOption) error {
	endpoint := EndpointThreadMember(id, "@me")
	_, err := s.RequestWithBucketID("DELETE", endpoint, nil, endpoint, options...)
	return err
}

// ThreadMemberAdd adds another member to a thread
func (s *Session) ThreadMemberAdd(threadID, memberID string, options ...RequestOption) error {
	endpoint := EndpointThreadMember(threadID, memberID)
	_, err := s.RequestWithBucketID("PUT", endpoint, nil, endpoint, options...)
	return err
}

// ThreadMemberRemove removes another member from a thread
func (s *Session) ThreadMemberRemove(threadID, memberID string, options ...RequestOption) error {
	endpoint := EndpointThreadMember(threadID, memberID)
	_, err := s.RequestWithBucketID("DELETE", endpoint, nil, endpoint, options...)
	return err
}

// ThreadMember returns thread member object for the specified member of a thread.
// withMember : Whether to include a guild member object.
func (s *Session) ThreadMember(threadID, memberID string, withMember bool, options ...RequestOption) (member *ThreadMember, err error) {
	uri := EndpointThreadMember(threadID, memberID)

	queryParams := url.Values{}
	if withMember {
		queryParams.Set("with_member", "true")
	}

	if len(queryParams) > 0 {
		uri += "?" + queryParams.Encode()
	}

	var body []byte
	body, err = s.RequestWithBucketID("GET", uri, nil, uri, options...)

	if err != nil {
		return
	}

	err = unmarshal(body, &member)
	return
}

// ThreadMembers returns all members of specified thread.
// limit      : Max number of thread members to return (1-100). Defaults to 100.
// afterID    : Get thread members after this user ID.
// withMember : Whether to include a guild member object for each thread member.
func (s *Session) ThreadMembers(threadID string, limit int, withMember bool, afterID string, options ...RequestOption) (members []*ThreadMember, err error) {
	uri := EndpointThreadMembers(threadID)

	queryParams := url.Values{}
	if withMember {
		queryParams.Set("with_member", "true")
	}
	if limit > 0 {
		queryParams.Set("limit", strconv.Itoa(limit))
	}
	if afterID != "" {
		queryParams.Set("after", afterID)
	}

	if len(queryParams) > 0 {
		uri += "?" + queryParams.Encode()
	}

	var body []byte
	body, err = s.RequestWithBucketID("GET", uri, nil, uri, options...)

	if err != nil {
		return
	}

	err = unmarshal(body, &members)
	return
}

// ThreadsActive returns all active threads for specified channel.
func (s *Session) ThreadsActive(channelID string, options ...RequestOption) (threads *ThreadsList, err error) {
	var body []byte
	body, err = s.RequestWithBucketID("GET", EndpointChannelActiveThreads(channelID), nil, EndpointChannelActiveThreads(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &threads)
	return
}

// GuildThreadsActive returns all active threads for specified guild.
func (s *Session) GuildThreadsActive(guildID string, options ...RequestOption) (threads *ThreadsList, err error) {
	var body []byte
	body, err = s.RequestWithBucketID("GET", EndpointGuildActiveThreads(guildID), nil, EndpointGuildActiveThreads(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &threads)
	return
}

// ThreadsArchived returns archived threads for specified channel.
// before : If specified returns only threads before the timestamp
// limit  : Optional maximum amount of threads to return.
func (s *Session) ThreadsArchived(channelID string, before *time.Time, limit int, options ...RequestOption) (threads *ThreadsList, err error) {
	endpoint := EndpointChannelPublicArchivedThreads(channelID)
	v := url.Values{}
	if before != nil {
		v.Set("before", before.Format(time.RFC3339))
	}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if len(v) > 0 {
		endpoint += "?" + v.Encode()
	}

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &threads)
	return
}

// ThreadsPrivateArchived returns archived private threads for specified channel.
// before : If specified returns only threads before the timestamp
// limit  : Optional maximum amount of threads to return.
func (s *Session) ThreadsPrivateArchived(channelID string, before *time.Time, limit int, options ...RequestOption) (threads *ThreadsList, err error) {
	endpoint := EndpointChannelPrivateArchivedThreads(channelID)
	v := url.Values{}
	if before != nil {
		v.Set("before", before.Format(time.RFC3339))
	}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if len(v) > 0 {
		endpoint += "?" + v.Encode()
	}
	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &threads)
	return
}

// ThreadsPrivateJoinedArchived returns archived joined private threads for specified channel.
// before : If specified returns only threads before the timestamp
// limit  : Optional maximum amount of threads to return.
func (s *Session) ThreadsPrivateJoinedArchived(channelID string, before *time.Time, limit int, options ...RequestOption) (threads *ThreadsList, err error) {
	endpoint := EndpointChannelJoinedPrivateArchivedThreads(channelID)
	v := url.Values{}
	if before != nil {
		v.Set("before", before.Format(time.RFC3339))
	}

	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}

	if len(v) > 0 {
		endpoint += "?" + v.Encode()
	}
	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &threads)
	return
}

// Functions specific to user notes
// ------------------------------------------------------------------------------------------------

// UserNoteSet sets the note for a specific user.
func (s *Session) UserNoteSet(userID string, message string) (err error) {
	data := struct {
		Note string `json:"note"`
	}{message}

	_, err = s.RequestWithBucketID("PUT", EndpointUserNotes(userID), data, EndpointUserNotes(""))
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to Discord Relationships (Friends list)
// ------------------------------------------------------------------------------------------------

// RelationshipsGet returns an array of all the relationships of the user.
func (s *Session) RelationshipsGet() (r []*Relationship, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointRelationships(), nil, EndpointRelationships())
	if err != nil {
		return
	}

	err = unmarshal(body, &r)
	return
}

// relationshipCreate creates a new relationship. (I.e. send or accept a friend request, block a user.)
// relationshipType : 1 = friend, 2 = blocked, 3 = incoming friend req, 4 = sent friend req
func (s *Session) relationshipCreate(userID string, relationshipType int) (err error) {
	data := struct {
		Type int `json:"type"`
	}{relationshipType}

	_, err = s.RequestWithBucketID("PUT", EndpointRelationship(userID), data, EndpointRelationships())
	return
}

// RelationshipFriendRequestSend sends a friend request to a user.
// userID: ID of the user.
func (s *Session) RelationshipFriendRequestSend(userID string) (err error) {
	err = s.relationshipCreate(userID, 4)
	return
}

// RelationshipFriendRequestAccept accepts a friend request from a user.
// userID: ID of the user.
func (s *Session) RelationshipFriendRequestAccept(userID string) (err error) {
	err = s.relationshipCreate(userID, 1)
	return
}

// RelationshipUserBlock blocks a user.
// userID: ID of the user.
func (s *Session) RelationshipUserBlock(userID string) (err error) {
	err = s.relationshipCreate(userID, 2)
	return
}

// RelationshipDelete removes the relationship with a user.
// userID: ID of the user.
func (s *Session) RelationshipDelete(userID string) (err error) {
	_, err = s.RequestWithBucketID("DELETE", EndpointRelationship(userID), nil, EndpointRelationships())
	return
}

// RelationshipsMutualGet returns an array of all the users both @me and the given user is friends with.
// userID: ID of the user.
func (s *Session) RelationshipsMutualGet(userID string) (mf []*User, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointRelationshipsMutual(userID), nil, EndpointRelationshipsMutual(userID))
	if err != nil {
		return
	}

	err = unmarshal(body, &mf)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to application (slash) commands
// ------------------------------------------------------------------------------------------------

// ApplicationCommandCreate creates a global application command and returns it.
// appID       : The application ID.
// guildID     : Guild ID to create guild-specific application command. If empty - creates global application command.
// cmd         : New application command data.
func (s *Session) ApplicationCommandCreate(appID string, guildID string, cmd *ApplicationCommand, options ...RequestOption) (ccmd *ApplicationCommand, err error) {
	endpoint := EndpointApplicationGlobalCommands(appID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommands(appID, guildID)
	}

	body, err := s.RequestWithBucketID("POST", endpoint, *cmd, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &ccmd)

	return
}

// ApplicationCommandEdit edits application command and returns new command data.
// appID       : The application ID.
// cmdID       : Application command ID to edit.
// guildID     : Guild ID to edit guild-specific application command. If empty - edits global application command.
// cmd         : Updated application command data.
func (s *Session) ApplicationCommandEdit(appID, guildID, cmdID string, cmd *ApplicationCommand, options ...RequestOption) (updated *ApplicationCommand, err error) {
	endpoint := EndpointApplicationGlobalCommand(appID, cmdID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommand(appID, guildID, cmdID)
	}

	body, err := s.RequestWithBucketID("PATCH", endpoint, *cmd, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &updated)

	return
}

// ApplicationCommandBulkOverwrite Creates commands overwriting existing commands. Returns a list of commands.
// appID    : The application ID.
// commands : The commands to create.
func (s *Session) ApplicationCommandBulkOverwrite(appID string, guildID string, commands []*ApplicationCommand, options ...RequestOption) (createdCommands []*ApplicationCommand, err error) {
	endpoint := EndpointApplicationGlobalCommands(appID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommands(appID, guildID)
	}

	body, err := s.RequestWithBucketID("PUT", endpoint, commands, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &createdCommands)

	return
}

// ApplicationCommandDelete deletes application command by ID.
// appID       : The application ID.
// cmdID       : Application command ID to delete.
// guildID     : Guild ID to delete guild-specific application command. If empty - deletes global application command.
func (s *Session) ApplicationCommandDelete(appID, guildID, cmdID string, options ...RequestOption) error {
	endpoint := EndpointApplicationGlobalCommand(appID, cmdID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommand(appID, guildID, cmdID)
	}

	_, err := s.RequestWithBucketID("DELETE", endpoint, nil, endpoint, options...)

	return err
}

// ApplicationCommand retrieves an application command by given ID.
// appID       : The application ID.
// cmdID       : Application command ID.
// guildID     : Guild ID to retrieve guild-specific application command. If empty - retrieves global application command.
func (s *Session) ApplicationCommand(appID, guildID, cmdID string, options ...RequestOption) (cmd *ApplicationCommand, err error) {
	endpoint := EndpointApplicationGlobalCommand(appID, cmdID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommand(appID, guildID, cmdID)
	}

	body, err := s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &cmd)

	return
}

// ApplicationCommands retrieves all commands in application.
// appID       : The application ID.
// guildID     : Guild ID to retrieve all guild-specific application commands. If empty - retrieves global application commands.
func (s *Session) ApplicationCommands(appID, guildID string, options ...RequestOption) (cmd []*ApplicationCommand, err error) {
	endpoint := EndpointApplicationGlobalCommands(appID)
	if guildID != "" {
		endpoint = EndpointApplicationGuildCommands(appID, guildID)
	}

	body, err := s.RequestWithBucketID("GET", endpoint+"?with_localizations=true", nil, "GET "+endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &cmd)

	return
}

// GuildApplicationCommandsPermissions returns permissions for application commands in a guild.
// appID       : The application ID
// guildID     : Guild ID to retrieve application commands permissions for.
func (s *Session) GuildApplicationCommandsPermissions(appID, guildID string, options ...RequestOption) (permissions []*GuildApplicationCommandPermissions, err error) {
	endpoint := EndpointApplicationCommandsGuildPermissions(appID, guildID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &permissions)
	return
}

// ApplicationCommandPermissions returns all permissions of an application command
// appID       : The Application ID
// guildID     : The guild ID containing the application command
// cmdID       : The command ID to retrieve the permissions of
func (s *Session) ApplicationCommandPermissions(appID, guildID, cmdID string, options ...RequestOption) (permissions *GuildApplicationCommandPermissions, err error) {
	endpoint := EndpointApplicationCommandPermissions(appID, guildID, cmdID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &permissions)
	return
}

// ApplicationCommandPermissionsEdit edits the permissions of an application command
// appID       : The Application ID
// guildID     : The guild ID containing the application command
// cmdID       : The command ID to edit the permissions of
// permissions : An object containing a list of permissions for the application command
//
// NOTE: Requires OAuth2 token with applications.commands.permissions.update scope
func (s *Session) ApplicationCommandPermissionsEdit(appID, guildID, cmdID string, permissions *ApplicationCommandPermissionsList, options ...RequestOption) (err error) {
	endpoint := EndpointApplicationCommandPermissions(appID, guildID, cmdID)

	_, err = s.RequestWithBucketID("PUT", endpoint, permissions, endpoint, options...)
	return
}

// ApplicationCommandPermissionsBatchEdit edits the permissions of a batch of commands
// appID       : The Application ID
// guildID     : The guild ID to batch edit commands of
// permissions : A list of permissions paired with a command ID, guild ID, and application ID per application command
//
// NOTE: This endpoint has been disabled with updates to command permissions (Permissions v2). Please use ApplicationCommandPermissionsEdit instead.
func (s *Session) ApplicationCommandPermissionsBatchEdit(appID, guildID string, permissions []*GuildApplicationCommandPermissions, options ...RequestOption) (err error) {
	endpoint := EndpointApplicationCommandsGuildPermissions(appID, guildID)

	_, err = s.RequestWithBucketID("PUT", endpoint, permissions, endpoint, options...)
	return
}

// InteractionRespond creates the response to an interaction.
// interaction : Interaction instance.
// resp        : Response message data.
func (s *Session) InteractionRespond(interaction *Interaction, resp *InteractionResponse, options ...RequestOption) error {
	endpoint := EndpointInteractionResponse(interaction.ID, interaction.Token)

	if resp.Data != nil && len(resp.Data.Files) > 0 {
		contentType, body, err := MultipartBodyWithJSON(resp, resp.Data.Files)
		if err != nil {
			return err
		}

		_, err = s.RequestRaw("POST", endpoint, contentType, body, endpoint, 0, options...)
		return err
	}

	_, err := s.RequestWithBucketID("POST", endpoint, *resp, endpoint, options...)
	return err
}

// InteractionResponse gets the response to an interaction.
// interaction : Interaction instance.
func (s *Session) InteractionResponse(interaction *Interaction, options ...RequestOption) (*Message, error) {
	return s.WebhookMessage(interaction.AppID, interaction.Token, "@original", options...)
}

// InteractionResponseEdit edits the response to an interaction.
// interaction : Interaction instance.
// newresp     : Updated response message data.
func (s *Session) InteractionResponseEdit(interaction *Interaction, newresp *WebhookEdit, options ...RequestOption) (*Message, error) {
	return s.WebhookMessageEdit(interaction.AppID, interaction.Token, "@original", newresp, options...)
}

// InteractionResponseDelete deletes the response to an interaction.
// interaction : Interaction instance.
func (s *Session) InteractionResponseDelete(interaction *Interaction, options ...RequestOption) error {
	endpoint := EndpointInteractionResponseActions(interaction.AppID, interaction.Token)

	_, err := s.RequestWithBucketID("DELETE", endpoint, nil, endpoint, options...)

	return err
}

// FollowupMessageCreate creates the followup message for an interaction.
// interaction : Interaction instance.
// wait        : Waits for server confirmation of message send and ensures that the return struct is populated (it is nil otherwise)
// data        : Data of the message to send.
func (s *Session) FollowupMessageCreate(interaction *Interaction, wait bool, data *WebhookParams, options ...RequestOption) (*Message, error) {
	return s.WebhookExecute(interaction.AppID, interaction.Token, wait, data, options...)
}

// FollowupMessageEdit edits a followup message of an interaction.
// interaction : Interaction instance.
// messageID   : The followup message ID.
// data        : Data to update the message
func (s *Session) FollowupMessageEdit(interaction *Interaction, messageID string, data *WebhookEdit, options ...RequestOption) (*Message, error) {
	return s.WebhookMessageEdit(interaction.AppID, interaction.Token, messageID, data, options...)
}

// FollowupMessageDelete deletes a followup message of an interaction.
// interaction : Interaction instance.
// messageID   : The followup message ID.
func (s *Session) FollowupMessageDelete(interaction *Interaction, messageID string, options ...RequestOption) error {
	return s.WebhookMessageDelete(interaction.AppID, interaction.Token, messageID, options...)
}

// ------------------------------------------------------------------------------------------------
// Functions specific to stage instances
// ------------------------------------------------------------------------------------------------

// StageInstanceCreate creates and returns a new Stage instance associated to a Stage channel.
// data : Parameters needed to create a stage instance.
// data : The data of the Stage instance to create
func (s *Session) StageInstanceCreate(data *StageInstanceParams, options ...RequestOption) (si *StageInstance, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointStageInstances, data, EndpointStageInstances, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &si)
	return
}

// StageInstance will retrieve a Stage instance by ID of the Stage channel.
// channelID : The ID of the Stage channel
func (s *Session) StageInstance(channelID string, options ...RequestOption) (si *StageInstance, err error) {
	body, err := s.RequestWithBucketID("GET", EndpointStageInstance(channelID), nil, EndpointStageInstance(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &si)
	return
}

// StageInstanceEdit will edit a Stage instance by ID of the Stage channel.
// channelID : The ID of the Stage channel
// data : The data to edit the Stage instance
func (s *Session) StageInstanceEdit(channelID string, data *StageInstanceParams, options ...RequestOption) (si *StageInstance, err error) {

	body, err := s.RequestWithBucketID("PATCH", EndpointStageInstance(channelID), data, EndpointStageInstance(channelID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &si)
	return
}

// StageInstanceDelete will delete a Stage instance by ID of the Stage channel.
// channelID : The ID of the Stage channel
func (s *Session) StageInstanceDelete(channelID string, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("DELETE", EndpointStageInstance(channelID), nil, EndpointStageInstance(channelID), options...)
	return
}

// ------------------------------------------------------------------------------------------------
// Functions specific to guilds scheduled events
// ------------------------------------------------------------------------------------------------

// GuildScheduledEvents returns an array of GuildScheduledEvent for a guild
// guildID        : The ID of a Guild
// userCount      : Whether to include the user count in the response
func (s *Session) GuildScheduledEvents(guildID string, userCount bool, options ...RequestOption) (st []*GuildScheduledEvent, err error) {
	uri := EndpointGuildScheduledEvents(guildID)
	if userCount {
		uri += "?with_user_count=true"
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildScheduledEvents(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildScheduledEvent returns a specific GuildScheduledEvent in a guild
// guildID        : The ID of a Guild
// eventID        : The ID of the event
// userCount      : Whether to include the user count in the response
func (s *Session) GuildScheduledEvent(guildID, eventID string, userCount bool, options ...RequestOption) (st *GuildScheduledEvent, err error) {
	uri := EndpointGuildScheduledEvent(guildID, eventID)
	if userCount {
		uri += "?with_user_count=true"
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildScheduledEvent(guildID, eventID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildScheduledEventCreate creates a GuildScheduledEvent for a guild and returns it
// guildID   : The ID of a Guild
// eventID   : The ID of the event
func (s *Session) GuildScheduledEventCreate(guildID string, event *GuildScheduledEventParams, options ...RequestOption) (st *GuildScheduledEvent, err error) {
	body, err := s.RequestWithBucketID("POST", EndpointGuildScheduledEvents(guildID), event, EndpointGuildScheduledEvents(guildID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildScheduledEventEdit updates a specific event for a guild and returns it.
// guildID   : The ID of a Guild
// eventID   : The ID of the event
func (s *Session) GuildScheduledEventEdit(guildID, eventID string, event *GuildScheduledEventParams, options ...RequestOption) (st *GuildScheduledEvent, err error) {
	body, err := s.RequestWithBucketID("PATCH", EndpointGuildScheduledEvent(guildID, eventID), event, EndpointGuildScheduledEvent(guildID, eventID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildScheduledEventDelete deletes a specific GuildScheduledEvent in a guild
// guildID   : The ID of a Guild
// eventID   : The ID of the event
func (s *Session) GuildScheduledEventDelete(guildID, eventID string, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("DELETE", EndpointGuildScheduledEvent(guildID, eventID), nil, EndpointGuildScheduledEvent(guildID, eventID), options...)
	return
}

// GuildScheduledEventUsers returns an array of GuildScheduledEventUser for a particular event in a guild
// guildID    : The ID of a Guild
// eventID    : The ID of the event
// limit      : The maximum number of users to return (Max 100)
// withMember : Whether to include the member object in the response
// beforeID   : If is not empty all returned users entries will be before the given ID
// afterID    : If is not empty all returned users entries will be after the given ID
func (s *Session) GuildScheduledEventUsers(guildID, eventID string, limit int, withMember bool, beforeID, afterID string, options ...RequestOption) (st []*GuildScheduledEventUser, err error) {
	uri := EndpointGuildScheduledEventUsers(guildID, eventID)

	queryParams := url.Values{}
	if withMember {
		queryParams.Set("with_member", "true")
	}
	if limit > 0 {
		queryParams.Set("limit", strconv.Itoa(limit))
	}
	if beforeID != "" {
		queryParams.Set("before", beforeID)
	}
	if afterID != "" {
		queryParams.Set("after", afterID)
	}

	if len(queryParams) > 0 {
		uri += "?" + queryParams.Encode()
	}

	body, err := s.RequestWithBucketID("GET", uri, nil, EndpointGuildScheduledEventUsers(guildID, eventID), options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// GuildOnboarding returns onboarding configuration of a guild.
// guildID   : The ID of the guild
func (s *Session) GuildOnboarding(guildID string, options ...RequestOption) (onboarding *GuildOnboarding, err error) {
	endpoint := EndpointGuildOnboarding(guildID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &onboarding)
	return
}

// GuildOnboardingEdit edits onboarding configuration of a guild.
// guildID   : The ID of the guild
// o         : New GuildOnboarding data
func (s *Session) GuildOnboardingEdit(guildID string, o *GuildOnboarding, options ...RequestOption) (onboarding *GuildOnboarding, err error) {
	endpoint := EndpointGuildOnboarding(guildID)

	var body []byte
	body, err = s.RequestWithBucketID("PUT", endpoint, o, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &onboarding)
	return
}

// ----------------------------------------------------------------------
// Functions specific to auto moderation
// ----------------------------------------------------------------------

// AutoModerationRules returns a list of auto moderation rules.
// guildID : ID of the guild
func (s *Session) AutoModerationRules(guildID string, options ...RequestOption) (st []*AutoModerationRule, err error) {
	endpoint := EndpointGuildAutoModerationRules(guildID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// AutoModerationRule returns an auto moderation rule.
// guildID : ID of the guild
// ruleID  : ID of the auto moderation rule
func (s *Session) AutoModerationRule(guildID, ruleID string, options ...RequestOption) (st *AutoModerationRule, err error) {
	endpoint := EndpointGuildAutoModerationRule(guildID, ruleID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// AutoModerationRuleCreate creates an auto moderation rule with the given data and returns it.
// guildID : ID of the guild
// rule    : Rule data
func (s *Session) AutoModerationRuleCreate(guildID string, rule *AutoModerationRule, options ...RequestOption) (st *AutoModerationRule, err error) {
	endpoint := EndpointGuildAutoModerationRules(guildID)

	var body []byte
	body, err = s.RequestWithBucketID("POST", endpoint, rule, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// AutoModerationRuleEdit edits and returns the updated auto moderation rule.
// guildID : ID of the guild
// ruleID  : ID of the auto moderation rule
// rule    : New rule data
func (s *Session) AutoModerationRuleEdit(guildID, ruleID string, rule *AutoModerationRule, options ...RequestOption) (st *AutoModerationRule, err error) {
	endpoint := EndpointGuildAutoModerationRule(guildID, ruleID)

	var body []byte
	body, err = s.RequestWithBucketID("PATCH", endpoint, rule, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// AutoModerationRuleDelete deletes an auto moderation rule.
// guildID : ID of the guild
// ruleID  : ID of the auto moderation rule
func (s *Session) AutoModerationRuleDelete(guildID, ruleID string, options ...RequestOption) (err error) {
	endpoint := EndpointGuildAutoModerationRule(guildID, ruleID)
	_, err = s.RequestWithBucketID("DELETE", endpoint, nil, endpoint, options...)
	return
}

// ApplicationRoleConnectionMetadata returns application role connection metadata.
// appID : ID of the application
func (s *Session) ApplicationRoleConnectionMetadata(appID string) (st []*ApplicationRoleConnectionMetadata, err error) {
	endpoint := EndpointApplicationRoleConnectionMetadata(appID)
	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// ApplicationRoleConnectionMetadataUpdate updates and returns application role connection metadata.
// appID    : ID of the application
// metadata : New metadata
func (s *Session) ApplicationRoleConnectionMetadataUpdate(appID string, metadata []*ApplicationRoleConnectionMetadata) (st []*ApplicationRoleConnectionMetadata, err error) {
	endpoint := EndpointApplicationRoleConnectionMetadata(appID)
	var body []byte
	body, err = s.RequestWithBucketID("PUT", endpoint, metadata, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

// UserApplicationRoleConnection returns user role connection to the specified application.
// appID : ID of the application
func (s *Session) UserApplicationRoleConnection(appID string) (st *ApplicationRoleConnection, err error) {
	endpoint := EndpointUserApplicationRoleConnection(appID)
	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return

}

// UserApplicationRoleConnectionUpdate updates and returns user role connection to the specified application.
// appID      : ID of the application
// connection : New ApplicationRoleConnection data
func (s *Session) UserApplicationRoleConnectionUpdate(appID string, rconn *ApplicationRoleConnection) (st *ApplicationRoleConnection, err error) {
	endpoint := EndpointUserApplicationRoleConnection(appID)
	var body []byte
	body, err = s.RequestWithBucketID("PUT", endpoint, rconn, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &st)
	return
}

type cursor struct {
	Repaired bool   `json:"repaired"`
	Previous string `json:"previous"`
	Next     string `json:"next"`
}

type respSearchApplicationCommands struct {
	Applications        []*Application        `json:"applications"`
	ApplicationCommands []*ApplicationCommand `json:"application_commands"`
	Cursor              cursor                `json:"cursor"`
}

func (s *Session) ApplicationCommandsSearch(channelID, query string, options ...RequestOption) (st []*ApplicationCommand, err error) {
	queryParams := url.Values{
		"type":                 {"1"},
		"query":                {query},
		"limit":                {"7"},
		"include_applications": {"false"},
	}
	if query == "" {
		queryParams.Set("limit", "10")
		queryParams.Set("include_applications", "true")
		queryParams.Del("query")
	}
	endpoint := EndpointApplicationCommandsSearch(channelID) + "?" + queryParams.Encode()

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	var resp respSearchApplicationCommands
	err = unmarshal(body, &resp)
	st = resp.ApplicationCommands
	return
}

type ApplicationCommandOptionInput struct {
	Type    ApplicationCommandOptionType     `json:"type"`
	Name    string                           `json:"name"`
	Value   interface{}                      `json:"value,omitempty"`
	Options []*ApplicationCommandOptionInput `json:"options,omitempty"`
}

type interactionData struct {
	Version       string                 `json:"version"`
	ID            string                 `json:"id"`
	ApplicationID string                 `json:"application_id"`
	Name          string                 `json:"name"`
	Type          ApplicationCommandType `json:"type"`

	Options     []*ApplicationCommandOptionInput `json:"options"`
	Attachments []interface{}                    `json:"attachments"`

	ApplicationCommand *ApplicationCommand `json:"application_command"`
}

type reqSendInteraction struct {
	Type          int             `json:"type"`
	ApplicationID string          `json:"application_id"`
	GuildID       string          `json:"guild_id,omitempty"`
	ChannelID     string          `json:"channel_id"`
	SessionID     string          `json:"session_id"`
	Data          interactionData `json:"data"`
	Nonce         string          `json:"nonce"`
}

func (s *Session) SendInteractions(guildID, channelID string, cmd *ApplicationCommand, options []*ApplicationCommandOptionInput, nonce string, reqOptions ...RequestOption) error {
	if options == nil {
		options = make([]*ApplicationCommandOptionInput, 0)
	}
	req := &reqSendInteraction{
		Type:          2,
		ApplicationID: cmd.ApplicationID,
		GuildID:       guildID,
		ChannelID:     channelID,
		SessionID:     s.sessionID,
		Data: interactionData{
			Version:            cmd.Version,
			ApplicationID:      cmd.ApplicationID,
			ID:                 cmd.ID,
			Name:               cmd.Name,
			Type:               cmd.Type,
			Options:            options,
			Attachments:        []interface{}{},
			ApplicationCommand: cmd,
		},
		Nonce: nonce,
	}
	contentType, body, encodeErr := MultipartBodyWithJSON(req, nil)
	if encodeErr != nil {
		return encodeErr
	}

	endpoint := EndpointInteractions
	_, err := s.RequestRaw("POST", endpoint, contentType, body, endpoint, 0, reqOptions...)
	return err
}

// ----------------------------------------------------------------------
// Functions specific to polls
// ----------------------------------------------------------------------

// PollAnswerVoters returns users who voted for a particular answer in a poll on the specified message.
// channelID : ID of the channel.
// messageID : ID of the message.
// answerID  : ID of the answer.
func (s *Session) PollAnswerVoters(channelID, messageID string, answerID int) (voters []*User, err error) {
	endpoint := EndpointPollAnswerVoters(channelID, messageID, answerID)

	var body []byte
	body, err = s.RequestWithBucketID("GET", endpoint, nil, endpoint)
	if err != nil {
		return
	}

	var r struct {
		Users []*User `json:"users"`
	}

	err = unmarshal(body, &r)
	if err != nil {
		return
	}

	voters = r.Users
	return
}

// PollExpire expires poll on the specified message.
// channelID : ID of the channel.
// messageID : ID of the message.
func (s *Session) PollExpire(channelID, messageID string) (msg *Message, err error) {
	endpoint := EndpointPollExpire(channelID, messageID)

	var body []byte
	body, err = s.RequestWithBucketID("POST", endpoint, nil, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &msg)
	return
}

// ----------------------------------------------------------------------
// Functions specific to monetization
// ----------------------------------------------------------------------

// SKUs returns all SKUs for a given application.
// appID : The ID of the application.
func (s *Session) SKUs(appID string) (skus []*SKU, err error) {
	endpoint := EndpointApplicationSKUs(appID)

	body, err := s.RequestWithBucketID("GET", endpoint, nil, endpoint)
	if err != nil {
		return
	}

	err = unmarshal(body, &skus)
	return
}

// Entitlements returns all Entitlements for a given app, active and expired.
// appID			: The ID of the application.
// filterOptions	: Optional filter options; otherwise set it to nil.
func (s *Session) Entitlements(appID string, filterOptions *EntitlementFilterOptions, options ...RequestOption) (entitlements []*Entitlement, err error) {
	endpoint := EndpointEntitlements(appID)

	queryParams := url.Values{}
	if filterOptions != nil {
		if filterOptions.UserID != "" {
			queryParams.Set("user_id", filterOptions.UserID)
		}
		if len(filterOptions.SkuIDs) > 0 {
			queryParams.Set("sku_ids", strings.Join(filterOptions.SkuIDs, ","))
		}
		if filterOptions.Before != nil {
			queryParams.Set("before", filterOptions.Before.Format(time.RFC3339))
		}
		if filterOptions.After != nil {
			queryParams.Set("after", filterOptions.After.Format(time.RFC3339))
		}
		if filterOptions.Limit > 0 {
			queryParams.Set("limit", strconv.Itoa(filterOptions.Limit))
		}
		if filterOptions.GuildID != "" {
			queryParams.Set("guild_id", filterOptions.GuildID)
		}
		if filterOptions.ExcludeEnded {
			queryParams.Set("exclude_ended", "true")
		}
	}

	body, err := s.RequestWithBucketID("GET", endpoint+"?"+queryParams.Encode(), nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &entitlements)
	return
}

// EntitlementConsume marks a given One-Time Purchase for the user as consumed.
func (s *Session) EntitlementConsume(appID, entitlementID string, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("POST", EndpointEntitlementConsume(appID, entitlementID), nil, EndpointEntitlementConsume(appID, ""), options...)
	return
}

// EntitlementTestCreate creates a test entitlement to a given SKU for a given guild or user.
// Discord will act as though that user or guild has entitlement to your premium offering.
func (s *Session) EntitlementTestCreate(appID string, data *EntitlementTest, options ...RequestOption) (err error) {
	endpoint := EndpointEntitlements(appID)

	_, err = s.RequestWithBucketID("POST", endpoint, data, endpoint, options...)
	return
}

// EntitlementTestDelete deletes a currently-active test entitlement. Discord will act as though
// that user or guild no longer has entitlement to your premium offering.
func (s *Session) EntitlementTestDelete(appID, entitlementID string, options ...RequestOption) (err error) {
	_, err = s.RequestWithBucketID("DELETE", EndpointEntitlement(appID, entitlementID), nil, EndpointEntitlement(appID, ""), options...)
	return
}

// Subscriptions returns all subscriptions containing the SKU.
// skuID : The ID of the SKU.
// userID : User ID for which to return subscriptions. Required except for OAuth queries.
// before : Optional timestamp to retrieve subscriptions before this time.
// after : Optional timestamp to retrieve subscriptions after this time.
// limit : Optional maximum number of subscriptions to return (1-100, default 50).
func (s *Session) Subscriptions(skuID string, userID string, before, after *time.Time, limit int, options ...RequestOption) (subscriptions []*Subscription, err error) {
	endpoint := EndpointSubscriptions(skuID)

	queryParams := url.Values{}
	if before != nil {
		queryParams.Set("before", before.Format(time.RFC3339))
	}
	if after != nil {
		queryParams.Set("after", after.Format(time.RFC3339))
	}
	if userID != "" {
		queryParams.Set("user_id", userID)
	}
	if limit > 0 {
		queryParams.Set("limit", strconv.Itoa(limit))
	}

	body, err := s.RequestWithBucketID("GET", endpoint+"?"+queryParams.Encode(), nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &subscriptions)
	return
}

// Subscription returns a subscription by its SKU and subscription ID.
// skuID : The ID of the SKU.
// subscriptionID : The ID of the subscription.
// userID : User ID for which to return the subscription. Required except for OAuth queries.
func (s *Session) Subscription(skuID, subscriptionID, userID string, options ...RequestOption) (subscription *Subscription, err error) {
	endpoint := EndpointSubscription(skuID, subscriptionID)

	queryParams := url.Values{}
	if userID != "" {
		// Unlike stated in the documentation, the user_id parameter is required here.
		queryParams.Set("user_id", userID)
	}

	body, err := s.RequestWithBucketID("GET", endpoint+"?"+queryParams.Encode(), nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &subscription)
	return
}

// UserVoiceState returns the voice state of the current user (the bot) in a guild.
// guildID : The ID of the guild.
// userID  : The ID of the user.
// Note: Using @me will return the bot's voice state for the given guild.
func (s *Session) UserVoiceState(guildID string, userID string, options ...RequestOption) (state *VoiceState, err error) {
	endpoint := EndpointGuildMemberVoiceState(guildID, userID)

	body, err := s.RequestWithBucketID("GET", endpoint, nil, endpoint, options...)
	if err != nil {
		return
	}

	err = unmarshal(body, &state)
	return
}

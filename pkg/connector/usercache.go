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
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// NOTE: Not simply using `exsync.Map` because we want the lock to be held
// during HTTP requests.

type UserCache struct {
	session *discordgo.Session
	cache   map[string]*discordgo.User
	lock    sync.Mutex
}

func NewUserCache(session *discordgo.Session) *UserCache {
	return &UserCache{
		session: session,
		cache:   make(map[string]*discordgo.User),
	}
}

func (uc *UserCache) HandleReady(ready *discordgo.Ready) {
	if ready == nil {
		return
	}

	uc.lock.Lock()
	defer uc.lock.Unlock()

	for _, user := range ready.Users {
		uc.cache[user.ID] = user
	}
}

// HandleMessage updates the user cache with the users involved in a single
// message (author, mentioned, mentioned author, etc.)
//
// The updated user IDs are returned.
func (uc *UserCache) HandleMessage(msg *discordgo.Message) []string {
	if msg == nil {
		return []string{}
	}

	// For now just forward to HandleMessages until a need for a specialized
	// path makes itself known.
	return uc.HandleMessages([]*discordgo.Message{msg})
}

// HandleMessages updates the user cache with the total set of users involved
// with multiple messages (authors, mentioned users, mentioned authors, etc.)
//
// The updated user IDs are returned.
func (uc *UserCache) HandleMessages(msgs []*discordgo.Message) []string {
	if len(msgs) == 0 {
		return []string{}
	}

	collectedUsers := map[string]*discordgo.User{}
	for _, msg := range msgs {
		collectedUsers[msg.Author.ID] = msg.Author

		referenced := msg.ReferencedMessage
		if referenced != nil && referenced.Author != nil {
			collectedUsers[referenced.Author.ID] = referenced.Author
		}

		for _, mentioned := range msg.Mentions {
			collectedUsers[mentioned.ID] = mentioned
		}

		// Message snapshots lack `author` entirely and seemingly have an empty
		// `mentions` array, even when the original message actually mentions
		// someone.
	}

	uc.lock.Lock()
	defer uc.lock.Unlock()

	for _, user := range collectedUsers {
		uc.cache[user.ID] = user
	}

	return slices.Collect(maps.Keys(collectedUsers))
}

func (uc *UserCache) HandleUserUpdate(update *discordgo.UserUpdate) {
	if update == nil || update.User == nil {
		return
	}

	uc.lock.Lock()
	defer uc.lock.Unlock()

	uc.cache[update.ID] = update.User
}

// Resolve looks up a user in the cache, requesting the user from the Discord
// HTTP API if not present.
//
// If the user cannot be found, then its nonexistence is cached. This is to
// avoid excessive requests when e.g. backfilling messages from a user that has
// since been deleted since connecting. If some other error occurs, the cache
// isn't touched and nil is returned.
//
// Otherwise, the cache is updated as you'd expect.
func (uc *UserCache) Resolve(ctx context.Context, userID string) *discordgo.User {
	if userID == discordid.DeletedGuildUserID {
		return &discordid.DeletedGuildUser
	}

	// Hopefully this isn't too contentious?
	uc.lock.Lock()
	defer uc.lock.Unlock()

	cachedUser, present := uc.cache[userID]
	if cachedUser != nil {
		return cachedUser
	} else if present {
		// If a `nil` is present in the map, then we already know that the user
		// doesn't exist.
		return nil
	}

	log := zerolog.Ctx(ctx).With().
		Str("action", "resolve user").
		Str("user_id", userID).Logger()

	log.Trace().Msg("Fetching user")
	user, err := uc.session.User(userID)

	var restError *discordgo.RESTError
	if errors.As(err, &restError) && restError.Response.StatusCode == http.StatusNotFound {
		log.Info().Msg("Tried to resolve a user that doesn't exist, caching nonexistence")
		uc.cache[userID] = nil

		return nil
	} else if err != nil {
		log.Err(err).Msg("Failed to resolve user")
		return nil
	}

	uc.cache[userID] = user

	return user
}

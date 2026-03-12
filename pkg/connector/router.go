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

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/router"
)

var _ router.Router = (*DiscordClient)(nil)

func (d *DiscordClient) uncertainRoute(ctx context.Context, channelID string) *router.Route {
	log := zerolog.Ctx(ctx)
	log.Warn().Str("channel_id", channelID).Msg("Creating an uncertain route")

	return &router.Route{
		// It's generally bad to call into discordid for PortalKey
		// construction since the helpers on DiscordClient ensure receiver
		// correctness, but this is a bit of a special case as we're
		// uncertain who the receiver should even be.
		PortalKey:       discordid.MakeChannelPortalKey(channelID, d.UserLogin.ID, true),
		PortalChannelID: channelID,
		Uncertain:       true,
	}
}

// FIXME(skip): This method is infallible now, remove the error from the
// signature in the interface and refactor.
func (d *DiscordClient) Route(ctx context.Context, channelID string) (*router.Route, error) {
	ch := d.channelWithID(ctx, channelID)
	dbThread, err := d.connector.DB.Thread.GetByThreadChannelID(
		ctx,
		discordid.ParseUserLoginID(d.UserLogin.ID),
		channelID,
	)
	if err != nil {
		// Even if we can't touch the database right now, we can try examining
		// the channel from State to make a routing decision.
		zerolog.Ctx(ctx).Warn().
			Err(err).
			Str("channel_id", channelID).
			Msg("Failed to look up potential thread channel ID, proceeding with route")
		dbThread = nil
	}

	// Most routes will just go to the channel the event originated from. (Not
	// true for threads right now.)
	r := router.Route{
		PortalChannelID: channelID,
		FromChannel:     ch,
		FromThread:      dbThread,
	}

	if dbThread != nil {
		// If the channel exists in the database as a thread, we immediately
		// know how to be receiver-correct (i.e. we can set a correct PortalKey),
		// even if the channel doesn't exist in State.

		// Threaded Discord messages need to be bridged to the Matrix room
		// that portals to the _parent_ Discord channel, since we always bridge
		// threads via m.thread right now.
		r.PortalChannelID = dbThread.ParentChannelID
		r.PortalKey = d.guildChannelPortalKey(dbThread.ParentChannelID)

		if ch == nil {
			return &r, nil
		}
	}

	if ch == nil {
		// We can't know the proper PortalKey for this channel. Return an
		// uncertain route instead.
		//
		// TODO: Maybe we can just ask the REST API for the channel?
		return d.uncertainRoute(ctx, channelID), nil
	}

	if isThread(ch) {
		if dbThread == nil {
			// This is a thread we haven't seen before, so insert it into the database.
			rootMsgID := defaultThreadRootMessageID(ch)
			if upsertErr := d.upsertThreadInfo(ctx, channelID, rootMsgID, ch.ParentID); upsertErr != nil {
				// Even if we can't save the thread to the database, we can still
				// use the routing decision.
				zerolog.Ctx(ctx).Warn().
					Err(upsertErr).
					Str("thread_channel_id", channelID).
					Str("parent_channel_id", ch.ParentID).
					Msg("Failed to upsert newly discovered thread, proceeding with route")
			}
			thread := discorddb.Thread{
				UserLoginID:     discordid.ParseUserLoginID(d.UserLogin.ID),
				ThreadChannelID: channelID,
				RootMessageID:   rootMsgID,
				ParentChannelID: ch.ParentID,
			}

			// Duplicated from above.
			r.PortalChannelID = thread.ParentChannelID
			r.PortalKey = d.guildChannelPortalKey(thread.ParentChannelID)
			r.FromThread = &thread
		}
	} else {
		r.PortalKey = d.portalKeyForChannel(ch)
	}

	return &r, nil
}

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

package router

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type Router interface {
	// Route embodies the core logic that determines where a Discord event from a
	// certain channel is ultimately bridged to on Matrix.
	//
	// This is significant for threads; routing a thread channel ID currently
	// redirects to the portal corresponding to the parent channel on Discord,
	// because we bridge threads via m.thread. That means the Matrix messages need
	// to go in the parent channel. The routing logic performs this resolution.
	//
	// Another way to think about this mechanism is that it hides the concern of
	// constructing the correct PortalKey in response to something that happened
	// in a Discord channel. It handles details like threading for you.
	Route(ctx context.Context, channelID string) (*Route, error)
}

// How and where a Discord event should be bridged to Matrix.
type Route struct {
	// The key of the portal that the event should be "routed" to. This is
	// almost like a destination.
	PortalKey networkid.PortalKey

	// The corresponding Discord channel ID of the portal that PortalKey points
	// to.
	PortalChannelID string

	// Whether or not we're certain about the receiver of the PortalKey. In
	// practice, this will only be true if we can't find the channel in state.
	// If this is true, then FromChannel and FromThread will always be nil.
	Uncertain bool

	// The Discord channel that the event originated from. This can be nil
	// despite the channel actually existing if it wasn't found in state.
	FromChannel *discordgo.Channel

	// Non-nil if Channel is a thread and the thread was found in state.
	FromThread *discorddb.Thread
}

func (r *Route) FromThreadRootMessageID() *networkid.MessageID {
	if r.FromThread == nil {
		return nil
	}

	rootMsgID := r.FromThread.RootMessageID
	if rootMsgID == "" {
		return nil
	}

	return ptr.Ptr(discordid.MakeMessageID(rootMsgID))
}

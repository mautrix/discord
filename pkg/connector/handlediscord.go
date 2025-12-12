// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"runtime/debug"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type DiscordEventMeta struct {
	Type       bridgev2.RemoteEventType
	PortalKey  networkid.PortalKey
	LogContext func(c zerolog.Context) zerolog.Context
}

func (em *DiscordEventMeta) AddLogContext(c zerolog.Context) zerolog.Context {
	if em.LogContext == nil {
		return c
	}
	c = em.LogContext(c)
	return c
}

func (em *DiscordEventMeta) GetType() bridgev2.RemoteEventType {
	return em.Type
}

func (em *DiscordEventMeta) GetPortalKey() networkid.PortalKey {
	return em.PortalKey
}

type DiscordMessage struct {
	*DiscordEventMeta
	Data   *discordgo.Message
	Client *DiscordClient
}

var (
	_ bridgev2.RemoteMessage = (*DiscordMessage)(nil)
	// _ bridgev2.RemoteEdit    = (*DiscordMessage)(nil)
	// _ bridgev2.RemoteMessageRemove    = (*DiscordMessage)(nil)
)

func (m *DiscordMessage) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return m.Client.connector.MsgConv.ToMatrix(ctx, portal, intent, m.Client.UserLogin, m.Data), nil
}

func (m *DiscordMessage) GetID() networkid.MessageID {
	return networkid.MessageID(m.Data.ID)
}

func (m *DiscordMessage) GetSender() bridgev2.EventSender {
	return m.Client.makeEventSender(m.Data.Author)
}

func (d *DiscordClient) wrapDiscordMessage(evt *discordgo.MessageCreate) DiscordMessage {
	return DiscordMessage{
		DiscordEventMeta: &DiscordEventMeta{
			Type: bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{
				ID:       networkid.PortalID(evt.ChannelID),
				Receiver: d.UserLogin.ID,
			},
		},
		Data:   evt.Message,
		Client: d,
	}
}

func (d *DiscordClient) handleDiscordEvent(rawEvt any) {
	if d.UserLogin == nil {
		// Our event handlers are able to assume that a UserLogin is available.
		// We respond to special events like READY outside of this function,
		// by virtue of methods like Session.Open only returning control flow
		// after RESUME or READY.
		log := zerolog.Ctx(context.TODO())
		log.Trace().Msg("Dropping Discord event received before UserLogin creation")
		return
	}

	defer func() {
		err := recover()
		if err != nil {
			d.UserLogin.Log.Error().
				Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
				Any(zerolog.ErrorFieldName, err).
				Msg("Panic in Discord event handler")
		}
	}()

	log := d.UserLogin.Log.With().Str("action", "handle discord event").
		Type("event_type", rawEvt).
		Logger()

	switch evt := rawEvt.(type) {
	case *discordgo.MessageCreate:
		wrappedEvt := d.wrapDiscordMessage(evt)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.PresenceUpdate:
		return
	case *discordgo.Event:
		// For presently unknown reasons sometimes discordgo won't unmarshal
		// events into their proper corresponding structs.
		if evt.Type == "PRESENCE_UPDATE" || evt.Type == "PASSIVE_UPDATE_V2" || evt.Type == "CONVERSATION_SUMMARY_UPDATE" {
			return
		}
		log.Debug().Str("event_type", evt.Type).Msg("Ignoring unknown Discord event")
	}
}

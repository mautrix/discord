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
	"fmt"
	"runtime/debug"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
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
	return m.Client.connector.MsgConv.ToMatrix(ctx, portal, intent, m.Client.UserLogin, m.Client.Session, m.Data), nil
}

func (m *DiscordMessage) GetID() networkid.MessageID {
	return discordid.MakeMessageID(m.Data.ID)
}

func (m *DiscordMessage) GetSender() bridgev2.EventSender {
	return m.Client.makeEventSender(m.Data.Author)
}

func (d *DiscordClient) wrapDiscordMessage(evt *discordgo.MessageCreate) DiscordMessage {
	return DiscordMessage{
		DiscordEventMeta: &DiscordEventMeta{
			Type: bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{
				ID:       discordid.MakePortalID(evt.ChannelID),
				Receiver: d.UserLogin.ID,
			},
		},
		Data:   evt.Message,
		Client: d,
	}
}

type DiscordReaction struct {
	*DiscordEventMeta
	Reaction *discordgo.MessageReaction
	Client   *DiscordClient
}

func (r *DiscordReaction) GetSender() bridgev2.EventSender {
	return r.Client.makeEventSenderWithID(r.Reaction.UserID)
}

func (r *DiscordReaction) GetTargetMessage() networkid.MessageID {
	return discordid.MakeMessageID(r.Reaction.MessageID)
}

func (r *DiscordReaction) GetRemovedEmojiID() networkid.EmojiID {
	return discordid.MakeEmojiID(r.Reaction.Emoji.Name)
}

var (
	_ bridgev2.RemoteReaction                 = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionRemove           = (*DiscordReaction)(nil)
	_ bridgev2.RemoteReactionWithExtraContent = (*DiscordReaction)(nil)
)

func (r *DiscordReaction) GetReactionEmoji() (string, networkid.EmojiID) {
	// name is either a grapheme cluster consisting of a Unicode emoji, or the
	// name of a custom emoji.
	name := r.Reaction.Emoji.Name
	return name, discordid.MakeEmojiID(name)
}

func (r *DiscordReaction) GetReactionExtraContent() map[string]any {
	extra := make(map[string]any)

	reaction := r.Reaction
	emoji := reaction.Emoji

	if emoji.ID != "" {
		// The emoji is a custom emoji.

		extra["fi.mau.discord.reaction"] = map[string]any{
			"id":   emoji.ID,
			"name": emoji.Name,
			// FIXME Handle custom emoji.
			// "mxc":  reaction,
		}

		wrappedShortcode := fmt.Sprintf(":%s:", reaction.Emoji.Name)
		extra["com.beeper.reaction.shortcode"] = wrappedShortcode
	}

	return extra
}

func (d *DiscordClient) wrapDiscordReaction(reaction *discordgo.MessageReaction, beingAdded bool) DiscordReaction {
	evtType := bridgev2.RemoteEventReaction
	if !beingAdded {
		evtType = bridgev2.RemoteEventReactionRemove
	}

	return DiscordReaction{
		DiscordEventMeta: &DiscordEventMeta{
			Type: evtType,
			PortalKey: networkid.PortalKey{
				ID:       discordid.MakePortalID(reaction.ChannelID),
				Receiver: d.UserLogin.ID,
			},
		},
		Reaction: reaction,
		Client:   d,
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

	if d.Session == nil || d.Session.State == nil || d.Session.State.User == nil {
		// Our event handlers are able to assume that we've fully connected to the
		// gateway.
		d.UserLogin.Log.Debug().Msg("Dropping Discord event received before READY or RESUMED")
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
	case *discordgo.Connect:
		log.Info().Msg("Discord gateway connected")
		d.markConnected()
	case *discordgo.Disconnect:
		log.Info().Msg("Discord gateway disconnected")
		d.scheduleTransientDisconnect("")
	case *discordgo.InvalidAuth:
		log.Warn().Msg("Discord gateway reported invalid auth")
		d.markInvalidAuth("You have been logged out of Discord, please reconnect")
	case *discordgo.Ready:
		log.Info().Msg("Received READY dispatch from discordgo")
		d.markConnected()
	case *discordgo.Resumed:
		log.Info().Msg("Received RESUMED dispatch from discordgo")
		d.markConnected()
	case *discordgo.MessageCreate:
		if evt.Author == nil {
			log.Trace().Int("message_type", int(evt.Message.Type)).
				Str("guild_id", evt.GuildID).
				Str("message_id", evt.ID).
				Str("channel_id", evt.ChannelID).
				Msg("Dropping message that lacks an author")
			return
		}
		wrappedEvt := d.wrapDiscordMessage(evt)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.MessageReactionAdd:
		wrappedEvt := d.wrapDiscordReaction(evt.MessageReaction, true)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	case *discordgo.MessageReactionRemove:
		wrappedEvt := d.wrapDiscordReaction(evt.MessageReaction, false)
		d.UserLogin.Bridge.QueueRemoteEvent(d.UserLogin, &wrappedEvt)
	// TODO case *discordgo.MessageReactionRemoveAll:
	// TODO case *discordgo.MessageReactionRemoveEmoji: (needs impl. in discordgo)
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

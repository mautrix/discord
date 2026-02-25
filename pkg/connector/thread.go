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
	"strings"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
)

func isThread(ch *discordgo.Channel) bool {
	return ch.Type == discordgo.ChannelTypeGuildPublicThread ||
		ch.Type == discordgo.ChannelTypeGuildPrivateThread ||
		ch.Type == discordgo.ChannelTypeGuildNewsThread
}

func defaultThreadRootMessageID(ch *discordgo.Channel) string {
	if ch == nil || !isThread(ch) {
		return ""
	}
	if ch.Type == discordgo.ChannelTypeGuildPrivateThread {
		return ""
	}
	return ch.ID
}

func (d *DiscordClient) upsertThreadInfo(ctx context.Context, threadChannelID, rootMessageID, parentChannelID string) error {
	if threadChannelID == "" || parentChannelID == "" {
		return nil
	}
	return d.connector.DB.Thread.Put(ctx, &discorddb.Thread{
		UserLoginID:     string(d.UserLogin.ID),
		ThreadChannelID: threadChannelID,
		RootMessageID:   rootMessageID,
		ParentChannelID: parentChannelID,
	})
}

func (d *DiscordClient) upsertThreadInfoFromChannel(ctx context.Context, ch *discordgo.Channel) error {
	if ch == nil || !isThread(ch) {
		return nil
	}
	return d.upsertThreadInfo(ctx, ch.ID, defaultThreadRootMessageID(ch), ch.ParentID)
}

func (d *DiscordClient) upsertThreadInfoFromMessage(ctx context.Context, msg *discordgo.Message) error {
	if msg == nil || msg.Flags&discordgo.MessageFlagsHasThread == 0 || msg.Thread == nil {
		return nil
	}
	threadChannelID := msg.Thread.ID
	if threadChannelID == "" {
		threadChannelID = msg.ID
	}
	parentChannelID := msg.Thread.ParentID
	if parentChannelID == "" {
		parentChannelID = msg.ChannelID
	}
	return d.upsertThreadInfo(ctx, threadChannelID, msg.ID, parentChannelID)
}

func (d *DiscordClient) getThreadByChannelID(ctx context.Context, threadChannelID string) (*discorddb.Thread, error) {
	if threadChannelID == "" {
		return nil, nil
	}
	thread, err := d.connector.DB.Thread.GetByThreadChannelID(ctx, string(d.UserLogin.ID), threadChannelID)
	if err != nil || thread != nil {
		return thread, err
	}

	ch, err := d.Session.State.Channel(threadChannelID)
	if err == nil && ch != nil && isThread(ch) {
		rootMsgID := defaultThreadRootMessageID(ch)
		if upsertErr := d.upsertThreadInfo(ctx, threadChannelID, rootMsgID, ch.ParentID); upsertErr != nil {
			return nil, upsertErr
		}
		return &discorddb.Thread{
			UserLoginID:     string(d.UserLogin.ID),
			ThreadChannelID: threadChannelID,
			RootMessageID:   rootMsgID,
			ParentChannelID: ch.ParentID,
		}, nil
	}

	return nil, nil
}

func (d *DiscordClient) getThreadByRootMessageID(ctx context.Context, rootMessageID string) (*discorddb.Thread, error) {
	if rootMessageID == "" {
		return nil, nil
	}
	thread, err := d.connector.DB.Thread.GetByRootMessageID(ctx, string(d.UserLogin.ID), rootMessageID)
	if err != nil || thread != nil {
		return thread, err
	}

	ch, err := d.Session.State.Channel(rootMessageID)
	if err == nil && ch != nil && isThread(ch) && defaultThreadRootMessageID(ch) == rootMessageID {
		if upsertErr := d.upsertThreadInfo(ctx, ch.ID, rootMessageID, ch.ParentID); upsertErr != nil {
			return nil, upsertErr
		}
		return &discorddb.Thread{
			UserLoginID:     string(d.UserLogin.ID),
			ThreadChannelID: ch.ID,
			RootMessageID:   rootMessageID,
			ParentChannelID: ch.ParentID,
		}, nil
	}

	return nil, nil
}

func (d *DiscordClient) getThreadPortalInfo(ctx context.Context, channelID string) (portalChannelID string, threadRootID *networkid.MessageID, err error) {
	portalChannelID = channelID
	thread, err := d.getThreadByChannelID(ctx, channelID)
	if err != nil || thread == nil {
		return
	}

	portalChannelID = thread.ParentChannelID
	if thread.RootMessageID != "" {
		rootID := discordid.MakeMessageID(thread.RootMessageID)
		threadRootID = &rootID
	}
	return
}

func getMatrixThreadRootRemoteMessageID(threadRoot *database.Message) string {
	if threadRoot == nil {
		return ""
	}
	remoteID := discordid.ParseMessageID(threadRoot.ID)
	if threadRoot.ThreadRoot != "" {
		remoteID = discordid.ParseMessageID(threadRoot.ThreadRoot)
	}
	return remoteID
}

func makeDiscordReferer(guildID, parentChannelID, threadChannelID string) discordgo.RequestOption {
	if threadChannelID != "" && threadChannelID != parentChannelID {
		return discordgo.WithThreadReferer(guildID, parentChannelID, threadChannelID)
	}
	return discordgo.WithChannelReferer(guildID, parentChannelID)
}

func getThreadName(content *event.MessageEventContent) string {
	body := ""
	if content != nil {
		body = content.Body
	}
	if len(body) == 0 {
		return "thread"
	}

	fields := strings.Fields(body)
	var title string
	for _, field := range fields {
		if len(title)+len(field) < 40 {
			title += field + " "
		} else if len(title) == 0 {
			title = field[:40]
			break
		} else {
			break
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "thread"
	}
	return title
}

func (d *DiscordClient) startThreadFromMatrix(
	ctx context.Context,
	guildID string,
	parentChannelID string,
	rootMessageID string,
	threadName string,
) (string, error) {
	if !d.Session.IsUser {
		return "", fmt.Errorf("can't create thread without being logged into Discord")
	}

	threadType := discordgo.ChannelTypeGuildPublicThread
	parentCh, err := d.Session.State.Channel(parentChannelID)
	if err == nil && parentCh != nil && parentCh.Type == discordgo.ChannelTypeGuildNews {
		threadType = discordgo.ChannelTypeGuildNewsThread
	}

	ch, err := d.Session.MessageThreadStartComplex(
		parentChannelID,
		rootMessageID,
		&discordgo.ThreadStart{
			Name:                threadName,
			AutoArchiveDuration: 24 * 60,
			Type:                threadType,
			Location:            "Message",
		},
		makeDiscordReferer(guildID, parentChannelID, ""),
	)
	if err != nil {
		return "", err
	}

	if upsertErr := d.upsertThreadInfo(ctx, ch.ID, rootMessageID, parentChannelID); upsertErr != nil {
		return "", upsertErr
	}
	return ch.ID, nil
}

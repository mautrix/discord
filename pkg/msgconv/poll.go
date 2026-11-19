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

package msgconv

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

const MessageTypePollResult discordgo.MessageType = 46

const pollPartID networkid.PartID = "poll"

func PollAnswerID(answerID int) string {
	return strconv.Itoa(answerID)
}

func pollMediaText(media *discordgo.PollMedia) string {
	if media == nil {
		return ""
	}
	if media.Emoji != nil && media.Emoji.ID == "" && media.Emoji.Name != "" {
		return strings.TrimSpace(media.Emoji.Name + " " + media.Text)
	}
	return media.Text
}

func (mc *MessageConverter) renderDiscordPoll(msg *discordgo.Message) *bridgev2.ConvertedMessagePart {
	poll := msg.Poll
	question := pollMediaText(&poll.Question)
	textAnswers := make([]string, len(poll.Answers))
	var htmlAnswers strings.Builder
	answers := make([]event.PollOption, len(poll.Answers))
	for i, answer := range poll.Answers {
		text := pollMediaText(answer.Media)
		textAnswers[i] = fmt.Sprintf("%d. %s", i+1, text)
		htmlAnswers.WriteString("<li>")
		htmlAnswers.WriteString(event.TextToHTML(text))
		htmlAnswers.WriteString("</li>")
		answers[i] = event.PollOption{
			ID:             PollAnswerID(answer.AnswerID),
			MSC1767Message: event.MSC1767Message{Text: text},
		}
	}
	part := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          fmt.Sprintf("Poll: %s\n%s\nOpen the Discord app to vote.", question, strings.Join(textAnswers, "\n")),
			Format:        event.FormatHTML,
			FormattedBody: fmt.Sprintf("<strong>Poll</strong>: %s<ol>%s</ol>Open the Discord app to vote.", event.TextToHTML(question), htmlAnswers.String()),
		},
	}
	if !mc.ExtEvPolls {
		return part
	}
	maxSelections := 1
	if poll.AllowMultiselect {
		maxSelections = len(answers)
	}
	part.Type = event.EventUnstablePollStart
	part.Extra = map[string]any{
		"org.matrix.msc3381.poll.start": &event.PollStart{
			Kind:          "org.matrix.msc3381.poll.disclosed",
			MaxSelections: maxSelections,
			Question:      event.MSC1767Message{Text: question},
			Answers:       answers,
		},
	}
	return part
}

func pollResultFields(msg *discordgo.Message) map[string]string {
	fields := make(map[string]string)
	for _, embed := range msg.Embeds {
		if embed.Type != "poll_result" {
			continue
		}
		for _, field := range embed.Fields {
			fields[field.Name] = field.Value
		}
	}
	return fields
}

func (mc *MessageConverter) renderDiscordPollResult(ctx context.Context, portal *bridgev2.Portal, msg *discordgo.Message) *bridgev2.ConvertedMessagePart {
	fields := pollResultFields(msg)
	var text string
	if winner := fields["victor_answer_text"]; winner != "" {
		if emoji := fields["victor_answer_emoji_name"]; emoji != "" && fields["victor_answer_emoji_id"] == "" {
			winner = emoji + " " + winner
		}
		text = fmt.Sprintf("The poll %q has ended. Winning answer: %s (%s of %s votes)",
			fields["poll_question_text"], winner, fields["victor_answer_votes"], fields["total_votes"])
	} else {
		text = fmt.Sprintf("The poll %q has ended with no single winner (%s votes)", fields["poll_question_text"], fields["total_votes"])
	}

	part := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    text,
		},
	}
	ref := msg.MessageReference
	if !mc.ExtEvPolls || ref == nil || ref.MessageID == "" {
		return part
	}
	pollMsg, err := mc.Bridge.DB.Message.GetPartByID(ctx, portal.Receiver, discordid.MakeMessageID(ref.MessageID), pollPartID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get ended poll message")
		return part
	} else if pollMsg == nil {
		return part
	}
	part.Type = event.EventUnstablePollEnd
	part.Content = &event.MessageEventContent{
		Body: text,
		RelatesTo: &event.RelatesTo{
			Type:    event.RelReference,
			EventID: pollMsg.MXID,
		},
	}
	part.Extra = map[string]any{
		"org.matrix.msc3381.poll.end": map[string]any{},
		"org.matrix.msc1767.text":     text,
	}
	return part
}

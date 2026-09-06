// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2023 Tulir Asokan
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

package main

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// The mautrix library version used by this bridge doesn't ship the unstable
// poll (MSC3381) event types and content structs, so they're defined here.
var EventUnstablePollStart = event.Type{Type: "org.matrix.msc3381.poll.start", Class: event.MessageEventType}
var EventUnstablePollResponse = event.Type{Type: "org.matrix.msc3381.poll.response", Class: event.MessageEventType}
var EventUnstablePollEnd = event.Type{Type: "org.matrix.msc3381.poll.end", Class: event.MessageEventType}

type MSC1767Message struct {
	Text    string           `json:"org.matrix.msc1767.text,omitempty"`
	HTML    string           `json:"org.matrix.msc1767.html,omitempty"`
	Message []ExtensibleText `json:"org.matrix.msc1767.message,omitempty"`
}

type ExtensibleText struct {
	MimeType string `json:"mimetype,omitempty"`
	Body     string `json:"body"`
}

func (content *MSC1767Message) getBody() string {
	if content.Text != "" {
		return content.Text
	}
	for _, part := range content.Message {
		if part.Body != "" && (part.MimeType == "" || part.MimeType == "text/plain") {
			return part.Body
		}
	}
	return content.HTML
}

type PollOption struct {
	ID string `json:"id"`
	MSC1767Message
}

type PollStart struct {
	Kind          string         `json:"kind"`
	MaxSelections int            `json:"max_selections"`
	Question      MSC1767Message `json:"question"`
	Answers       []PollOption   `json:"answers"`
}

type PollStartEventContent struct {
	RelatesTo *event.RelatesTo `json:"m.relates_to,omitempty"`
	Mentions  *event.Mentions  `json:"m.mentions,omitempty"`
	PollStart PollStart        `json:"org.matrix.msc3381.poll.start"`
}

func (content *PollStartEventContent) GetRelatesTo() *event.RelatesTo {
	if content.RelatesTo == nil {
		content.RelatesTo = &event.RelatesTo{}
	}
	return content.RelatesTo
}

type PollResponse struct {
	Answers []string `json:"answers"`
}

type PollResponseEventContent struct {
	RelatesTo event.RelatesTo `json:"m.relates_to"`
	Response  PollResponse    `json:"org.matrix.msc3381.poll.response"`
}

func (content *PollResponseEventContent) GetRelatesTo() *event.RelatesTo {
	return &content.RelatesTo
}

// handleMatrixPollEvent is registered as the Matrix event handler for poll events.
// It resolves the user and portal and forwards the event to the portal's message loop.
func (br *DiscordBridge) handleMatrixPollEvent(evt *event.Event) {
	// Ignore events sent by the bridge itself (the bot or puppets). Otherwise, polls that the
	// bridge created on Matrix (e.g. from a Discord poll) would be echoed back and re-sent to
	// Discord, causing an infinite loop.
	if evt.Sender == br.Bot.UserID || br.IsGhost(evt.Sender) {
		return
	}
	brUser := br.GetIUser(evt.Sender, true)
	if brUser == nil || brUser.GetPermissionLevel() < 1 {
		return
	}
	user, ok := brUser.(*User)
	if !ok {
		br.ZLog.Warn().Str("user_id", evt.Sender.String()).Msg("Poll event from unknown user type, dropping")
		return
	}
	portal := br.GetIPortal(evt.RoomID)
	if portal == nil {
		br.ZLog.Warn().Str("room_id", evt.RoomID.String()).Msg("Poll event in unknown room, dropping")
		return
	}
	if _, isPoll := parsePollStartFromEvent(evt); !isPoll && evt.Type == EventUnstablePollStart {
		br.ZLog.Debug().Str("event_type", evt.Type.Type).Msg("Dropping event of poll start type without poll content")
		return
	}
	evt.Content.Parsed = br.parsePollEventContent(evt.Type, evt.Content.VeryRaw)
	portal.ReceiveMatrixEvent(user, evt)
}

func (br *DiscordBridge) parsePollEventContent(evtType event.Type, raw json.RawMessage) any {
	var parsed any
	switch evtType {
	case EventUnstablePollStart:
		parsed = &PollStartEventContent{}
	case EventUnstablePollResponse:
		parsed = &PollResponseEventContent{}
	case event.EventMessage:
		parsed = &event.MessageEventContent{}
	default:
		return nil
	}
	if err := json.Unmarshal(raw, parsed); err != nil {
		br.ZLog.Warn().Err(err).Str("event_type", evtType.Type).Msg("Failed to parse poll event content")
		return nil
	}
	return parsed
}

// brIsPollStartMessage reports whether a Matrix m.room.message event carries the
// MSC3381 poll start extension (clients send polls this way).
func brIsPollStartMessage(evt *event.Event) bool {
	_, ok := parsePollStartFromEvent(evt)
	return ok
}

// parsePollStartFromEvent extracts the MSC3381 poll start content from an event.
// It supports both the standalone org.matrix.msc3381.poll.start event type and a
// regular m.room.message carrying the extension in its content.
func parsePollStartFromEvent(evt *event.Event) (*PollStartEventContent, bool) {
	if parsed, ok := evt.Content.Parsed.(*PollStartEventContent); ok {
		return parsed, ok
	}
	raw := evt.Content.VeryRaw
	if len(raw) == 0 {
		return nil, false
	}
	var wrapper struct {
		PollStart *PollStart `json:"org.matrix.msc3381.poll.start"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || wrapper.PollStart == nil {
		return nil, false
	}
	return &PollStartEventContent{PollStart: *wrapper.PollStart}, true
}

// convertDiscordPoll converts a Discord poll message into a Matrix poll start event.
func (portal *Portal) convertDiscordPoll(msg *discordgo.Message) *ConvertedMessage {
	poll := msg.Poll
	if poll == nil {
		return nil
	}
	questionText := poll.Question.Text

	maxSelections := 1
	if poll.AllowMultiselect {
		maxSelections = len(poll.Answers)
	}

	// Build a fallback message body in case the client doesn't support polls.
	var body string
	if questionText != "" {
		body = questionText + "\n"
	}
	answers := make([]map[string]any, 0, len(poll.Answers))
	optionsText := make([]string, 0, len(poll.Answers))
	answerIndex := 0
	for _, answer := range poll.Answers {
		if answer.Media == nil || answer.Media.Text == "" {
			continue
		}
		if answerIndex > 0 {
			body += "\n"
		}
		body += fmt.Sprintf("%d. %s", answerIndex+1, answer.Media.Text)
		// MSC3381 poll option IDs are opaque strings. The same index-based scheme
		// as the reference bridge (mautrix-signal/whatsapp) is used: the Matrix
		// answer order matches Discord's 1-based answer_id, so we can map votes.
		answers = append(answers, map[string]any{
			"id":                      strconv.Itoa(answerIndex),
			"org.matrix.msc1767.text": answer.Media.Text,
		})
		optionsText = append(optionsText, answer.Media.Text)
		answerIndex++
	}
	body += "\n\n(This message is a poll. Please use a compatible client to vote.)"

	var htmlBody string
	if questionText != "" {
		htmlBody = "<p>" + html.EscapeString(questionText) + "</p><ol>"
	} else {
		htmlBody = "<ol>"
	}
	for _, answer := range poll.Answers {
		if answer.Media == nil || answer.Media.Text == "" {
			continue
		}
		htmlBody += "<li>" + html.EscapeString(answer.Media.Text) + "</li>"
	}
	htmlBody += "</ol><p><em>This message is a poll. Please use a compatible client to vote.</em></p>"

	// Leave MsgType unset so poll-aware clients (e.g. Sable) fall through to
	// their poll extension rendering instead of treating the event as plain
	// m.text/emote/notice, which would otherwise render the fallback body as a
	// normal text message before the poll check is reached.
	content := &event.MessageEventContent{
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: htmlBody,
	}
	// Send the poll as the standalone MSC3381 poll start event type with the
	// poll content in its body. Element detects a poll by the event type
	// (org.matrix.msc3381.poll.start), so this is reliably rendered as a poll
	// rather than plain text.
	extra := map[string]any{
		"org.matrix.msc1767.text": body,
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind":           "org.matrix.msc3381.poll.disclosed",
			"max_selections": maxSelections,
			"question": map[string]any{
				"org.matrix.msc1767.text": questionText,
				"body":                    questionText,
			},
			"answers": answers,
		},
	}
	pollOptionIDs := make([]string, 0, len(answers))
	for _, answer := range answers {
		if idStr, ok := answer["id"].(string); ok {
			pollOptionIDs = append(pollOptionIDs, idStr)
		}
	}
	return &ConvertedMessage{
		Type:          EventUnstablePollStart,
		Content:       content,
		Extra:         extra,
		PollOptionIDs: pollOptionIDs,
	}
}

// getPollOptionIDs fetches the option IDs of a Matrix poll start event in Discord answer order.
func (portal *Portal) getPollOptionIDs(pollMXID id.EventID) ([]string, error) {
	// Prefer the in-memory option ID cache (stored when the poll was bridged).
	portal.pollOptionIDsLock.Lock()
	ids, ok := portal.pollOptionIDs[pollMXID]
	portal.pollOptionIDsLock.Unlock()
	if ok {
		return ids, nil
	}
	// Fall back to reading the Matrix poll start event.
	evt, err := portal.getEvent(pollMXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get poll event: %w", err)
	}
	if evt.Type == event.EventEncrypted {
		return nil, fmt.Errorf("poll event %s is encrypted and can't be read", pollMXID)
	}
	content, ok := parsePollStartFromEvent(evt)
	if !ok {
		return nil, fmt.Errorf("event %s is not a poll start event", pollMXID)
	}
	ids = make([]string, len(content.PollStart.Answers))
	for i, answer := range content.PollStart.Answers {
		ids[i] = answer.ID
	}
	return ids, nil
}

// storePollOptionIDs caches the option IDs of a poll in Discord answer order in memory.
func (portal *Portal) storePollOptionIDs(pollMXID id.EventID, ids []string) {
	if len(ids) == 0 {
		return
	}
	copied := make([]string, len(ids))
	copy(copied, ids)
	portal.pollOptionIDsLock.Lock()
	portal.pollOptionIDs[pollMXID] = copied
	portal.pollOptionIDsLock.Unlock()
}

// handleMatrixPollStart converts and sends a Matrix poll start event to Discord.
func (portal *Portal) handleMatrixPollStart(sender *User, evt *event.Event) {
	// If a DB row already exists for this exact Matrix event ID, it means this
	// event was already bridged from Discord (see handleDiscordMessageCreate /
	// markMessageHandled). The appservice is echoing our own poll event back to
	// us, so ignore it to prevent an infinite Discord<->Matrix loop.
	if existing := portal.bridge.DB.Message.GetByMXID(portal.Key, evt.ID); existing != nil {
		portal.log.Debug().
			Str("event_id", evt.ID.String()).
			Msg("Dropping poll start event that was already bridged from Discord")
		return
	}
	pollContent, ok := parsePollStartFromEvent(evt)
	if !ok {
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed), "Ignoring")
		return
	}
	sess := sender.Session
	if sess == nil {
		go portal.sendMessageMetrics(evt, fmt.Errorf("polls can't be sent without being logged in"), "Ignoring")
		return
	}
	channelID := portal.Key.ChannelID
	if portal.IsPrivateChat() && sender.DiscordID != portal.Key.Receiver {
		go portal.sendMessageMetrics(evt, errUserNotReceiver, "Ignoring")
		return
	}
	var threadID string
	if threadRoot := pollContent.GetRelatesTo().GetThreadParent(); threadRoot != "" {
		existingThread := portal.bridge.GetThreadByRootMXID(threadRoot)
		if existingThread != nil {
			threadID = existingThread.ID
		}
	}
	if threadID != "" {
		channelID = threadID
	}

	question := pollContent.PollStart.Question.getBody()
	if question == "" {
		go portal.sendMessageMetrics(evt, fmt.Errorf("poll message is missing question"), "Ignoring")
		return
	}
	if len(pollContent.PollStart.Answers) == 0 {
		go portal.sendMessageMetrics(evt, fmt.Errorf("poll message has no answers"), "Ignoring")
		return
	}

	discordPoll := &discordgo.Poll{
		Question: discordgo.PollMedia{Text: question},
		Answers:  make([]discordgo.PollAnswer, 0, len(pollContent.PollStart.Answers)),
	}
	if pollContent.PollStart.MaxSelections != 1 {
		discordPoll.AllowMultiselect = true
	}
	optionIDs := make([]string, 0, len(pollContent.PollStart.Answers))
	for _, answer := range pollContent.PollStart.Answers {
		answerText := answer.getBody()
		if answerText == "" {
			continue
		}
		discordPoll.Answers = append(discordPoll.Answers, discordgo.PollAnswer{
			Media: &discordgo.PollMedia{Text: answerText},
		})
		optionIDs = append(optionIDs, answer.ID)
	}
	if len(discordPoll.Answers) == 0 {
		go portal.sendMessageMetrics(evt, fmt.Errorf("poll message has no answers with text"), "Ignoring")
		return
	}

	sendReq := discordgo.MessageSend{Poll: discordPoll, Nonce: generateNonce()}
	msg, err := sess.ChannelMessageSendComplex(channelID, &sendReq, portal.RefererOptIfUser(sess, threadID)...)
	go portal.sendMessageMetrics(evt, err, "Error sending poll")
	if err != nil || msg == nil {
		return
	}
	dbMsg := portal.bridge.DB.Message.New()
	dbMsg.Channel = portal.Key
	dbMsg.DiscordID = msg.ID
	dbMsg.MXID = evt.ID
	dbMsg.SenderID = sender.DiscordID
	dbMsg.SenderMXID = sender.MXID
	dbMsg.Timestamp, _ = discordgo.SnowflakeTimestamp(msg.ID)
	dbMsg.ThreadID = threadID
	dbMsg.Insert()

	// Cache the option IDs (in Matrix answer order, which matches Discord's 1-based answer order)
	// so votes can be mapped back without re-fetching the Matrix event.
	portal.storePollOptionIDs(evt.ID, optionIDs)
}

// handleMatrixPollResponse relays a poll vote from Matrix to Discord.
func (portal *Portal) handleMatrixPollResponse(sender *User, evt *event.Event) {
	pollResponse, ok := evt.Content.Parsed.(*PollResponseEventContent)
	if !ok {
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed), "Ignoring")
		return
	}
	sess := sender.Session
	if sess == nil {
		go portal.sendMessageMetrics(evt, errUserNotLoggedIn, "Ignoring")
		return
	}
	if portal.IsPrivateChat() && sender.DiscordID != portal.Key.Receiver {
		go portal.sendMessageMetrics(evt, errUserNotReceiver, "Ignoring")
		return
	}
	pollMXID := pollResponse.GetRelatesTo().EventID
	if pollMXID == "" {
		go portal.sendMessageMetrics(evt, fmt.Errorf("poll response is missing target event"), "Ignoring")
		return
	}
	pollMsg := portal.bridge.DB.Message.GetByMXID(portal.Key, pollMXID)
	if pollMsg == nil {
		go portal.sendMessageMetrics(evt, fmt.Errorf("%w %s", errTargetNotFound, pollMXID), "Ignoring")
		return
	}
	optionIDs, err := portal.getPollOptionIDs(pollMsg.MXID)
	if err != nil {
		portal.log.Warn().Err(err).Str("poll_mxid", pollMXID.String()).Msg("Failed to get poll option IDs")
		go portal.sendMessageMetrics(evt, err, "Failed to get poll option IDs")
		return
	}
	// Discord answer IDs are 1-based indexes into the answers array,
	// which is the same order as the Matrix poll answers array.
	answerIDByOptionID := make(map[string]int, len(optionIDs))
	for i, optionID := range optionIDs {
		answerIDByOptionID[optionID] = i + 1
	}
	selectedAnswerIDs := make([]int, 0, len(pollResponse.Response.Answers))
	for _, selectedOptionID := range pollResponse.Response.Answers {
		answerID, ok := answerIDByOptionID[selectedOptionID]
		if !ok {
			portal.log.Warn().Str("option_id", selectedOptionID).Str("poll_mxid", pollMXID.String()).Msg("Unknown poll option in vote")
			continue
		}
		selectedAnswerIDs = append(selectedAnswerIDs, answerID)
	}
	if len(selectedAnswerIDs) == 0 {
		go portal.sendMessageMetrics(evt, nil, "")
		return
	}
	// Discord casts poll votes via `PUT /channels/{channel.id}/polls/{message.id}/answers/@me`
	// with a body listing the answer IDs and the channel referer header, exactly like the
	// user-session Discord web/desktop client.
	body, err := json.Marshal(map[string]any{"answer_ids": selectedAnswerIDs})
	if err != nil {
		go portal.sendMessageMetrics(evt, fmt.Errorf("failed to serialize poll vote: %w", err), "Error sending")
		return
	}
	endpoint := discordgo.EndpointPoll(pollMsg.DiscordProtoChannelID(), pollMsg.DiscordID) + "/answers/@me"
	var options []discordgo.RequestOption
	if sess.IsUser {
		options = []discordgo.RequestOption{portal.RefererOpt(pollMsg.ThreadID)}
	}
	_, err = sess.RequestRaw("PUT", endpoint, "application/json", body, endpoint, 0, options...)
	go portal.sendMessageMetrics(evt, err, "Error sending")
}

// handleDiscordPollVote relays a poll vote from Discord to Matrix.
func (portal *Portal) handleDiscordPollVote(userID, messageID string, answerID int, add bool) {
	msg := portal.bridge.DB.Message.GetByDiscordID(portal.Key, messageID)
	if len(msg) == 0 || msg[0].MXID == "" {
		portal.log.Debug().
			Str("message_id", messageID).
			Msg("Failed to relay poll vote: poll message not found")
		return
	}
	pollMXID := msg[0].MXID
	optionIDs, err := portal.getPollOptionIDs(pollMXID)
	if err != nil {
		portal.log.Warn().Err(err).Str("poll_mxid", pollMXID.String()).Msg("Failed to get poll option IDs for vote")
		return
	}
	if answerID < 1 || answerID > len(optionIDs) {
		portal.log.Warn().
			Str("poll_mxid", pollMXID.String()).
			Int("answer_id", answerID).
			Msg("Poll vote answer ID out of range")
		return
	}
	selected := make([]string, 0, 1)
	if add {
		selected = append(selected, optionIDs[answerID-1])
	}
	puppet := portal.bridge.GetPuppetByID(userID)
	intent := puppet.IntentFor(portal)
	responses := &PollResponseEventContent{
		RelatesTo: event.RelatesTo{Type: event.RelReference, EventID: pollMXID},
		Response:  PollResponse{Answers: selected},
	}
	wrappedContent := event.Content{Parsed: responses}
	evtType := EventUnstablePollResponse
	evtType, err = portal.encrypt(intent, &wrappedContent, evtType)
	if err != nil {
		portal.log.Warn().Err(err).
			Str("message_id", messageID).
			Str("user_id", userID).
			Msg("Failed to encrypt poll vote")
		return
	}
	_, err = intent.SendMessageEvent(portal.MXID, evtType, &wrappedContent)
	if err != nil {
		portal.log.Warn().Err(err).
			Str("message_id", messageID).
			Str("user_id", userID).
			Msg("Failed to relay poll vote to Matrix")
	}
}

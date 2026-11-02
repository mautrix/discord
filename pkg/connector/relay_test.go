package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/connector/discorddb"
	"go.mau.fi/mautrix-discord/pkg/discordid"
	"go.mau.fi/mautrix-discord/pkg/msgconv"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type relayTestMatrix struct {
	bridgev2.MatrixConnector
	moderator bool
}

func (m *relayTestMatrix) GetPowerLevels(context.Context, id.RoomID) (*event.PowerLevelsEventContent, error) {
	levels := &event.PowerLevelsEventContent{}
	if m.moderator {
		levels.Users = map[id.UserID]int{"@alice:test": 100}
	}
	return levels, nil
}
func (m *relayTestMatrix) GetMemberInfo(context.Context, id.RoomID, id.UserID) (*event.MemberEventContent, error) {
	return &event.MemberEventContent{Displayname: "Bob"}, nil
}
func (m *relayTestMatrix) ParseGhostMXID(id.UserID) (networkid.UserID, bool) { return "", false }

func (m *relayTestMatrix) GetPublicMediaAddress(id.ContentURIString) string {
	return "https://public.test/avatar"
}
func (m *relayTestMatrix) GetPublicMediaAddressForEvent(context.Context, *event.MessageEventContent) (string, error) {
	return "", nil
}

type relayTestBot struct{ bridgev2.MatrixAPI }

func (b *relayTestBot) DownloadMedia(context.Context, id.ContentURIString, *event.EncryptedFileInfo) ([]byte, error) {
	return []byte("file content"), nil
}
func (b *relayTestBot) GetEvent(context.Context, id.RoomID, id.EventID) (*event.Event, error) {
	return &event.Event{Sender: "@bob:test", Content: event.Content{Parsed: &event.MessageEventContent{Body: "reply target\nsecond line"}}}, nil
}

func newRelayTest(t *testing.T, handle testRoundTripper) (*DiscordClient, *bridgev2.Portal) {
	t.Helper()
	bridge := &bridgev2.Bridge{Matrix: &relayTestMatrix{}, Bot: &relayTestBot{}}
	connector := &DiscordConnector{Bridge: bridge, MsgConv: msgconv.NewMessageConverter(bridge)}
	connector.relayClient, _ = discordgo.New("")
	connector.relayClient.Client = &http.Client{Transport: handle}
	session, _ := discordgo.New("private-user-token")
	session.State.User = &discordgo.User{ID: "100"}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "100", Metadata: &discordid.UserLoginMetadata{Token: "private-user-token"}}}
	client := &DiscordClient{connector: connector, UserLogin: login, Session: session}
	portal := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "200"}, MXID: "!room:test", Metadata: &discordid.PortalMetadata{GuildID: "300", RelayWebhookID: "400", RelayWebhookSecret: "webhook-secret"}}}
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	raw.SetMaxOpenConns(1)
	_, err = raw.Exec(`CREATE TABLE discord_thread (user_login_id TEXT, thread_channel_id TEXT, root_message_id TEXT, parent_channel_id TEXT); INSERT INTO discord_thread VALUES ('100', '500', '600', '200')`)
	if err != nil {
		t.Fatal(err)
	}
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	connector.DB = discorddb.New(db, zerolog.Nop())
	return client, portal
}

func webhookResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestWebhookSendRouting(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(fmt.Sprint(media), func(t *testing.T) {
			calls := 0
			client, portal := newRelayTest(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/api/v9/webhooks/400/webhook-secret" || req.URL.Query().Get("wait") != "true" || req.URL.Query().Get("thread_id") != "500" {
					t.Errorf("unexpected webhook request: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != "" {
					t.Error("user credentials sent to webhook")
				}
				var params discordgo.WebhookParams
				if media {
					if err := req.ParseMultipartForm(1024 * 1024); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(req.FormValue("payload_json")), &params); err != nil {
						t.Fatal(err)
					}
					if len(req.MultipartForm.File) != 1 {
						t.Error("missing file")
					}
				} else if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
					t.Fatal(err)
				}
				if params.Username != "Alice" || params.AvatarURL != "https://public.test/avatar" || params.Content != "hello @everyone" {
					t.Errorf("unexpected profile/content: %+v", params)
				}
				if params.AllowedMentions == nil || len(params.AllowedMentions.Parse) != 0 {
					t.Error("unprivileged mass mention allowed")
				}
				if params.Flags != 0 {
					t.Error("unsupported webhook flags")
				}
				return webhookResponse(200, `{"id":"1234567890123456789","author":{"id":"400"}}`), nil
			})
			content := &event.MessageEventContent{MsgType: event.MsgText, Body: "hello @everyone"}
			if media {
				content.MsgType, content.FileName, content.URL = event.MsgImage, "photo.png", "mxc://test/image"
			}
			msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
				Portal: portal, Content: content, Event: &event.Event{Sender: "@alice:test"},
				OrigSender: &bridgev2.OrigSender{UserID: "@alice:test", MemberEventContent: event.MemberEventContent{Displayname: "Alice", AvatarURL: "mxc://test/avatar"}},
			}, ThreadRoot: &database.Message{ID: "600"}}
			result, err := client.HandleMatrixMessage(context.Background(), msg)
			if err != nil {
				t.Fatal(err)
			}
			if result.DB.SenderID != "400" || calls != 1 {
				t.Fatal("incorrect relay sender or send count")
			}
		})
	}
}

func TestWebhookMutationOwnershipAndThread(t *testing.T) {
	calls := 0
	client, portal := newRelayTest(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("thread_id") != "500" || !strings.HasSuffix(req.URL.Path, "/messages/700") {
			t.Errorf("wrong target: %s", req.URL)
		}
		if req.Method == "PATCH" {
			var body discordgo.WebhookEdit
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.AllowedMentions == nil {
				t.Error("edit lost mention policy")
			}
			return webhookResponse(200, `{"id":"700"}`), nil
		}
		return webhookResponse(404, `{"code":10008,"message":"Unknown Message"}`), nil
	})
	target := &database.Message{ID: "700", SenderID: "400", SenderMXID: "@alice:test", ThreadRoot: "600"}
	edit := &bridgev2.MatrixEdit{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: portal, Event: &event.Event{Sender: "@alice:test"}, Content: &event.MessageEventContent{Body: "edited"}}, EditTarget: target}
	if err := client.HandleMatrixEdit(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	edit.Event.Sender = "@other:test"
	if err := client.HandleMatrixEdit(context.Background(), edit); err == nil {
		t.Fatal("another sender edited relay message")
	}
	if calls != 1 {
		t.Fatal("unauthorized edit reached Discord")
	}
	removal := &bridgev2.MatrixMessageRemove{MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: portal, Event: &event.Event{Sender: "@alice:test"}}, TargetMessage: target}
	if err := client.HandleMatrixMessageRemove(context.Background(), removal); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("delete was not sent")
	}
}

func TestWebhookURLAndSecretErrors(t *testing.T) {
	for _, raw := range []string{"http://discord.com/api/webhooks/1/secret", "https://evil.test/api/webhooks/1/secret", "https://discord.com/api/webhooks/1/secret?x=y", "https://discord.com/api/webhooks/1/", "https://discord.com@evil.test/api/webhooks/1/secret"} {
		if _, _, err := parseWebhookURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	hook, token, err := parseWebhookURL("https://discord.com/api/webhooks/123/secret")
	if err != nil || hook != "123" || token != "secret" {
		t.Fatal("valid webhook rejected")
	}
	err = webhookError(fmt.Errorf("POST https://discord.com/api/webhooks/123/secret: failure"), "secret")
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("secret leaked")
	}
}

func TestWebhookReplyAndNoAccountFallback(t *testing.T) {
	client, portal := newRelayTest(t, func(req *http.Request) (*http.Response, error) {
		var params discordgo.WebhookParams
		if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
			t.Fatal(err)
		}
		if len(params.Embeds) != 1 || !strings.Contains(params.Embeds[0].Description, "https://discord.com/channels/300/200/700") || !strings.Contains(params.Embeds[0].Description, "reply target") {
			t.Errorf("reply fallback missing: %+v", params.Embeds)
		}
		return webhookResponse(200, `{"id":"1234567890123456789"}`), nil
	})
	msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
		Portal: portal, Event: &event.Event{Sender: "@alice:test"}, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "reply"}, OrigSender: &bridgev2.OrigSender{UserID: "@alice:test"},
	}, ReplyTo: &database.Message{ID: "700", MXID: "$target"}}
	if _, err := client.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	portal.Metadata.(*discordid.PortalMetadata).RelayWebhookID = ""
	client.Session.Client = &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("relay fell back to user account")
		return nil, nil
	})}
	if _, err := client.HandleMatrixMessage(context.Background(), msg); err == nil {
		t.Fatal("missing webhook accepted")
	}
}

func TestWebhookEditEcho(t *testing.T) {
	client, portal := newRelayTest(t, nil)
	incoming := &DiscordMessage{Client: client, Data: &discordgo.Message{WebhookID: "400"}}
	edit, err := incoming.ConvertEdit(context.Background(), portal, nil, nil)
	if err != nil || len(edit.ModifiedParts) != 0 || len(edit.DeletedParts) != 0 || edit.AddedParts != nil {
		t.Fatalf("echo was converted: %+v, %v", edit, err)
	}
}

func TestWebhookMutationOutsideThread(t *testing.T) {
	client, portal := newRelayTest(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Has("thread_id") {
			t.Error("sent empty thread ID")
		}
		return webhookResponse(200, `{"id":"700"}`), nil
	})
	msg := &bridgev2.MatrixEdit{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: portal, Event: &event.Event{Sender: "@alice:test"}, Content: &event.MessageEventContent{Body: "edit"}}, EditTarget: &database.Message{ID: "700", SenderID: "400", SenderMXID: "@alice:test"}}
	if err := client.HandleMatrixEdit(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
}

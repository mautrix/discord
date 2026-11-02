package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/shlex"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestInteractionArgumentParity(t *testing.T) {
	cmd := &discordgo.ApplicationCommand{Options: []*discordgo.ApplicationCommandOption{{
		Name: "sub", Type: discordgo.ApplicationCommandOptionSubCommand,
		Options: []*discordgo.ApplicationCommandOption{
			{Name: "text", Type: discordgo.ApplicationCommandOptionString, Required: true},
			{Name: "count", Type: discordgo.ApplicationCommandOptionInteger},
			{Name: "yes", Type: discordgo.ApplicationCommandOptionBoolean},
			{Name: "number", Type: discordgo.ApplicationCommandOptionNumber},
		},
	}}}
	args, err := shlex.Split(`sub text="hello world" count=42 yes=true number=1.5`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeCommand(cmd, args)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	expected := `[{"type":1,"name":"sub","options":[{"type":3,"name":"text","value":"hello world"},{"type":4,"name":"count","value":42},{"type":5,"name":"yes","value":true},{"type":10,"name":"number","value":1.5}]}]`
	if string(raw) != expected {
		t.Fatalf("wrong interaction options: %s", raw)
	}
	for _, input := range [][]string{{"sub"}, {"sub", "text=x", "count=no"}, {"unknown"}, {"sub", "text=x", "extra"}} {
		if _, err := executeCommand(cmd, input); err == nil {
			t.Errorf("invalid arguments accepted: %v", input)
		}
	}
	for _, typ := range []discordgo.ApplicationCommandOptionType{
		discordgo.ApplicationCommandOptionSubCommandGroup, discordgo.ApplicationCommandOptionUser,
		discordgo.ApplicationCommandOptionChannel, discordgo.ApplicationCommandOptionRole,
		discordgo.ApplicationCommandOptionMentionable, discordgo.ApplicationCommandOptionAttachment,
	} {
		if _, err := parseCommandOptionValue(typ, "123"); err == nil {
			t.Errorf("legacy-unsupported option %d accepted", typ)
		}
	}
}

func TestInteractionCommandCacheScope(t *testing.T) {
	count := 0
	session, _ := discordgo.New("token")
	session.Client = &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		count++
		if req.URL.Query().Get("query") != "ping" {
			t.Error("wrong search query")
		}
		return webhookResponse(200, `{"application_commands":[{"id":"1","name":"ping","application_id":"2"}]}`), nil
	})}
	client := &DiscordClient{Session: session}
	for _, channel := range []string{"10", "10", "20"} {
		cmd, err := client.getCommand(context.Background(), "30", channel, "ping")
		if err != nil || cmd == nil || cmd.ID != "1" {
			t.Fatalf("command lookup failed: %+v, %v", cmd, err)
		}
	}
	if count != 2 {
		t.Fatalf("cache crossed channels or missed: %d requests", count)
	}
}

type interactionTestBot struct {
	bridgev2.MatrixAPI
	reactions atomic.Int32
}

func (b *interactionTestBot) SendMessage(_ context.Context, _ id.RoomID, typ event.Type, content *event.Content, _ *bridgev2.MatrixSendExtra) (*mautrix.RespSendEvent, error) {
	if typ == event.EventReaction && content.AsReaction().RelatesTo.Key == "✅" {
		b.reactions.Add(1)
	}
	return &mautrix.RespSendEvent{EventID: "$ack"}, nil
}

func TestInteractionAcknowledgementIsConsumedOnce(t *testing.T) {
	bot := &interactionTestBot{}
	ce := &commands.Event{Bot: bot, Ctx: context.Background()}
	client := &DiscordClient{pendingInteractions: map[string]*pendingInteraction{"nonce": {event: ce, timer: time.NewTimer(time.Hour)}}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { client.handleInteractionSuccess(&discordgo.InteractionSuccess{Nonce: "nonce"}) })
	}
	wg.Wait()
	if bot.reactions.Load() != 1 || len(client.pendingInteractions) != 0 {
		t.Fatal("duplicate acknowledgement or pending interaction leak")
	}
}

func TestInteractionTimeoutAndSuccessRace(t *testing.T) {
	ce := &commands.Event{}
	client := &DiscordClient{pendingInteractions: map[string]*pendingInteraction{"nonce": {event: ce}}}
	var consumed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if client.takeInteraction("nonce") != nil {
				consumed.Add(1)
			}
		})
	}
	wg.Wait()
	if consumed.Load() != 1 {
		t.Fatal("interaction completed more than once")
	}
}

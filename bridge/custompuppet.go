package bridge

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	ErrNoCustomMXID    = errors.New("no custom mxid set")
	ErrMismatchingMXID = errors.New("whoami result does not match custom mxid")
)

///////////////////////////////////////////////////////////////////////////////
// additional bridge api
///////////////////////////////////////////////////////////////////////////////
func (b *Bridge) newDoublePuppetClient(mxid id.UserID, accessToken string) (*mautrix.Client, error) {
	_, homeserver, err := mxid.Parse()
	if err != nil {
		return nil, err
	}

	homeserverURL, found := b.Config.Bridge.DoublePuppetServerMap[homeserver]
	if !found {
		if homeserver == b.as.HomeserverDomain {
			homeserverURL = b.as.HomeserverURL
		} else if b.Config.Bridge.DoublePuppetAllowDiscovery {
			resp, err := mautrix.DiscoverClientAPI(homeserver)
			if err != nil {
				return nil, fmt.Errorf("failed to find homeserver URL for %s: %v", homeserver, err)
			}

			homeserverURL = resp.Homeserver.BaseURL
			b.log.Debugfln("Discovered URL %s for %s to enable double puppeting for %s", homeserverURL, homeserver, mxid)
		} else {
			return nil, fmt.Errorf("double puppeting from %s is not allowed", homeserver)
		}
	}

	client, err := mautrix.NewClient(homeserverURL, mxid, accessToken)
	if err != nil {
		return nil, err
	}

	client.Logger = b.as.Log.Sub(mxid.String())
	client.Client = b.as.HTTPClient
	client.DefaultHTTPRetries = b.as.DefaultHTTPRetries

	return client, nil
}

///////////////////////////////////////////////////////////////////////////////
// mautrix.Syncer implementation
///////////////////////////////////////////////////////////////////////////////
func (p *Puppet) GetFilterJSON(_ id.UserID) *mautrix.Filter {
	everything := []event.Type{{Type: "*"}}
	return &mautrix.Filter{
		Presence: mautrix.FilterPart{
			Senders: []id.UserID{p.CustomMXID},
			Types:   []event.Type{event.EphemeralEventPresence},
		},
		AccountData: mautrix.FilterPart{NotTypes: everything},
		Room: mautrix.RoomFilter{
			Ephemeral:    mautrix.FilterPart{Types: []event.Type{event.EphemeralEventTyping, event.EphemeralEventReceipt}},
			IncludeLeave: false,
			AccountData:  mautrix.FilterPart{NotTypes: everything},
			State:        mautrix.FilterPart{NotTypes: everything},
			Timeline:     mautrix.FilterPart{NotTypes: everything},
		},
	}
}

func (p *Puppet) OnFailedSync(_ *mautrix.RespSync, err error) (time.Duration, error) {
	p.log.Warnln("Sync error:", err)
	if errors.Is(err, mautrix.MUnknownToken) {
		if !p.tryRelogin(err, "syncing") {
			return 0, err
		}

		p.customIntent.AccessToken = p.AccessToken

		return 0, nil
	}

	return 10 * time.Second, nil
}

func (p *Puppet) ProcessResponse(resp *mautrix.RespSync, _ string) error {
	if !p.customUser.LoggedIn() {
		p.log.Debugln("Skipping sync processing: custom user not connected to discord")

		return nil
	}

	// for roomID, events := range resp.Rooms.Join {
	// 	for _, evt := range events.Ephemeral.Events {
	// 		evt.RoomID = roomID
	// 		err := evt.Content.ParseRaw(evt.Type)
	// 		if err != nil {
	// 			continue
	// 		}

	// 		switch evt.Type {
	// 		case event.EphemeralEventReceipt:
	// 			if p.EnableReceipts {
	// 				go p.bridge.matrixHandler.HandleReceipt(evt)
	// 			}
	// 		case event.EphemeralEventTyping:
	// 			go p.bridge.matrixHandler.HandleTyping(evt)
	// 		}
	// 	}
	// }

	// if p.EnablePresence {
	// 	for _, evt := range resp.Presence.Events {
	// 		if evt.Sender != p.CustomMXID {
	// 			continue
	// 		}

	// 		err := evt.Content.ParseRaw(evt.Type)
	// 		if err != nil {
	// 			continue
	// 		}

	// 		go p.bridge.matrixHandler.HandlePresence(evt)
	// 	}
	// }

	return nil
}

///////////////////////////////////////////////////////////////////////////////
// mautrix.Storer implementation
///////////////////////////////////////////////////////////////////////////////
func (p *Puppet) SaveFilterID(_ id.UserID, _ string) {
}

func (p *Puppet) SaveNextBatch(_ id.UserID, nbt string) {
	p.NextBatch = nbt
	p.Update()
}

func (p *Puppet) SaveRoom(_ *mautrix.Room) {
}

func (p *Puppet) LoadFilterID(_ id.UserID) string {
	return ""
}

func (p *Puppet) LoadNextBatch(_ id.UserID) string {
	return p.NextBatch
}

func (p *Puppet) LoadRoom(_ id.RoomID) *mautrix.Room {
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// additional puppet api
///////////////////////////////////////////////////////////////////////////////
func (p *Puppet) clearCustomMXID() {
	p.CustomMXID = ""
	p.AccessToken = ""
	p.customIntent = nil
	p.customUser = nil
}

func (p *Puppet) newCustomIntent() (*appservice.IntentAPI, error) {
	if p.CustomMXID == "" {
		return nil, ErrNoCustomMXID
	}

	client, err := p.bridge.newDoublePuppetClient(p.CustomMXID, p.AccessToken)
	if err != nil {
		return nil, err
	}

	client.Syncer = p
	client.Store = p

	ia := p.bridge.as.NewIntentAPI("custom")
	ia.Client = client
	ia.Localpart, _, _ = p.CustomMXID.Parse()
	ia.UserID = p.CustomMXID
	ia.IsCustomPuppet = true

	return ia, nil
}

func (p *Puppet) StartCustomMXID(reloginOnFail bool) error {
	if p.CustomMXID == "" {
		p.clearCustomMXID()

		return nil
	}

	intent, err := p.newCustomIntent()
	if err != nil {
		p.clearCustomMXID()

		return err
	}

	resp, err := intent.Whoami()
	if err != nil {
		if !reloginOnFail || (errors.Is(err, mautrix.MUnknownToken) && !p.tryRelogin(err, "initializing double puppeting")) {
			p.clearCustomMXID()

			return err
		}

		intent.AccessToken = p.AccessToken
	} else if resp.UserID != p.CustomMXID {
		p.clearCustomMXID()

		return ErrMismatchingMXID
	}

	p.customIntent = intent
	p.customUser = p.bridge.GetUserByMXID(p.CustomMXID)
	p.startSyncing()

	return nil
}

func (p *Puppet) tryRelogin(cause error, action string) bool {
	if !p.bridge.Config.CanAutoDoublePuppet(p.CustomMXID) {
		return false
	}

	p.log.Debugfln("Trying to relogin after '%v' while %s", cause, action)

	accessToken, err := p.loginWithSharedSecret(p.CustomMXID)
	if err != nil {
		p.log.Errorfln("Failed to relogin after '%v' while %s: %v", cause, action, err)

		return false
	}

	p.log.Infofln("Successfully relogined after '%v' while %s", cause, action)
	p.AccessToken = accessToken

	return true
}

func (p *Puppet) startSyncing() {
	if !p.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}

	go func() {
		p.log.Debugln("Starting syncing...")
		p.customIntent.SyncPresence = "offline"

		err := p.customIntent.Sync()
		if err != nil {
			p.log.Errorln("Fatal error syncing:", err)
		}
	}()
}

func (p *Puppet) stopSyncing() {
	if !p.bridge.Config.Bridge.SyncWithCustomPuppets {
		return
	}

	p.customIntent.StopSync()
}

func (p *Puppet) loginWithSharedSecret(mxid id.UserID) (string, error) {
	_, homeserver, _ := mxid.Parse()

	p.log.Debugfln("Logging into %s with shared secret", mxid)

	mac := hmac.New(sha512.New, []byte(p.bridge.Config.Bridge.LoginSharedSecretMap[homeserver]))
	mac.Write([]byte(mxid))

	client, err := p.bridge.newDoublePuppetClient(mxid, "")
	if err != nil {
		return "", fmt.Errorf("failed to create mautrix client to log in: %v", err)
	}

	resp, err := client.Login(&mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: string(mxid)},
		Password:                 hex.EncodeToString(mac.Sum(nil)),
		DeviceID:                 "Discord Bridge",
		InitialDeviceDisplayName: "Discord Bridge",
	})
	if err != nil {
		return "", err
	}

	return resp.AccessToken, nil
}

func (p *Puppet) SwitchCustomMXID(accessToken string, mxid id.UserID) error {
	prevCustomMXID := p.CustomMXID
	if p.customIntent != nil {
		p.stopSyncing()
	}

	p.CustomMXID = mxid
	p.AccessToken = accessToken

	err := p.StartCustomMXID(false)
	if err != nil {
		return err
	}

	if prevCustomMXID != "" {
		delete(p.bridge.puppetsByCustomMXID, prevCustomMXID)
	}

	if p.CustomMXID != "" {
		p.bridge.puppetsByCustomMXID[p.CustomMXID] = p
	}

	p.EnablePresence = p.bridge.Config.Bridge.DefaultBridgePresence
	p.EnableReceipts = p.bridge.Config.Bridge.DefaultBridgeReceipts

	p.bridge.as.StateStore.MarkRegistered(p.CustomMXID)

	p.Update()

	// TODO leave rooms with default puppet

	return nil
}

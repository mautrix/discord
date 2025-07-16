// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	_ "embed"
	"net/http"
	"sync"

	"go.mau.fi/util/configupgrade"
	"go.mau.fi/util/exsync"
	"golang.org/x/sync/semaphore"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/database"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

//go:embed example-config.yaml
var ExampleConfig string

type DiscordBridge struct {
	bridge.Bridge

	Config *config.Config
	DB     *database.Database

	DMA          *DirectMediaAPI
	provisioning *ProvisioningAPI

	usersByMXID map[id.UserID]*User
	usersByID   map[string]*User
	usersLock   sync.Mutex

	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex

	portalsByMXID map[id.RoomID]*Portal
	portalsByID   map[database.PortalKey]*Portal
	portalsLock   sync.Mutex

	threadsByID                 map[string]*Thread
	threadsByRootMXID           map[id.EventID]*Thread
	threadsByCreationNoticeMXID map[id.EventID]*Thread
	threadsLock                 sync.Mutex

	guildsByMXID map[id.RoomID]*Guild
	guildsByID   map[string]*Guild
	guildsLock   sync.Mutex

	puppets             map[string]*Puppet
	puppetsByCustomMXID map[id.UserID]*Puppet
	puppetsLock         sync.Mutex

	attachmentTransfers         *exsync.Map[attachmentKey, *exsync.ReturnableOnce[*database.File]]
	parallelAttachmentSemaphore *semaphore.Weighted
}

func (br *DiscordBridge) GetExampleConfig() string {
	return ExampleConfig
}

func (br *DiscordBridge) GetConfigPtr() interface{} {
	br.Config = &config.Config{
		BaseConfig: &br.Bridge.Config,
	}
	br.Config.BaseConfig.Bridge = &br.Config.Bridge
	return br.Config
}

func (br *DiscordBridge) Init() {
	br.CommandProcessor = commands.NewProcessor(&br.Bridge)
	br.RegisterCommands()

	matrixHTMLParser.PillConverter = br.pillConverter

	br.DB = database.New(br.Bridge.DB, br.Log.Sub("Database"))
	discordLog = br.ZLog.With().Str("component", "discordgo").Logger()
}

func (br *DiscordBridge) Start() {
	if br.Config.Bridge.Provisioning.SharedSecret != "disable" {
		br.provisioning = newProvisioningAPI(br)
	}
	if br.Config.Bridge.PublicAddress != "" {
		br.AS.Router.HandleFunc("/mautrix-discord/avatar/{server}/{mediaID}/{checksum}", br.serveMediaProxy).Methods(http.MethodGet)
	}
	br.DMA = newDirectMediaAPI(br)
	br.WaitWebsocketConnected()
	go br.startUsers()
}

func (br *DiscordBridge) Stop() {
	for _, user := range br.usersByMXID {
		if user.Session == nil {
			continue
		}

		br.Log.Debugln("Disconnecting", user.MXID)
		user.Session.Close()
	}
}

func (br *DiscordBridge) GetIPortal(mxid id.RoomID) bridge.Portal {
	p := br.GetPortalByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *DiscordBridge) GetIUser(mxid id.UserID, create bool) bridge.User {
	p := br.GetUserByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *DiscordBridge) IsGhost(mxid id.UserID) bool {
	_, isGhost := br.ParsePuppetMXID(mxid)
	return isGhost
}

func (br *DiscordBridge) GetIGhost(mxid id.UserID) bridge.Ghost {
	p := br.GetPuppetByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (br *DiscordBridge) CreatePrivatePortal(id id.RoomID, user bridge.User, ghost bridge.Ghost) {
	//TODO implement
}

func main() {
	br := &DiscordBridge{
		usersByMXID: make(map[id.UserID]*User),
		usersByID:   make(map[string]*User),

		managementRooms: make(map[id.RoomID]*User),

		portalsByMXID: make(map[id.RoomID]*Portal),
		portalsByID:   make(map[database.PortalKey]*Portal),

		threadsByID:                 make(map[string]*Thread),
		threadsByRootMXID:           make(map[id.EventID]*Thread),
		threadsByCreationNoticeMXID: make(map[id.EventID]*Thread),

		guildsByID:   make(map[string]*Guild),
		guildsByMXID: make(map[id.RoomID]*Guild),

		puppets:             make(map[string]*Puppet),
		puppetsByCustomMXID: make(map[id.UserID]*Puppet),

		attachmentTransfers:         exsync.NewMap[attachmentKey, *exsync.ReturnableOnce[*database.File]](),
		parallelAttachmentSemaphore: semaphore.NewWeighted(3),
	}
	br.Bridge = bridge.Bridge{
		Name:              "mautrix-discord",
		URL:               "https://github.com/mautrix/discord",
		Description:       "A Matrix-Discord puppeting bridge.",
		Version:           "0.7.5",
		ProtocolName:      "Discord",
		BeeperServiceName: "discordgo",
		BeeperNetworkName: "discord",

		CryptoPickleKey: "maunium.net/go/mautrix-whatsapp",

		ConfigUpgrader: &configupgrade.StructUpgrader{
			SimpleUpgrader: configupgrade.SimpleUpgrader(config.DoUpgrade),
			Blocks:         config.SpacedBlocks,
			Base:           ExampleConfig,
		},

		Child: br,
	}
	br.InitVersion(Tag, Commit, BuildTime)

	br.Main()
}

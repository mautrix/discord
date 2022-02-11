package bridge

import (
	"errors"
	"fmt"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/config"
	"gitlab.com/beeper/discord/database"
	"gitlab.com/beeper/discord/version"
)

const (
	reconnectDelay = 10 * time.Second
)

type Bridge struct {
	Config *config.Config

	log log.Logger

	as             *appservice.AppService
	db             *database.Database
	eventProcessor *appservice.EventProcessor
	matrixHandler  *matrixHandler
	bot            *appservice.IntentAPI

	usersByMXID map[id.UserID]*User
	usersByID   map[string]*User
	usersLock   sync.Mutex

	managementRooms     map[id.RoomID]*User
	managementRoomsLock sync.Mutex

	portalsByMXID map[id.RoomID]*Portal
	portalsByID   map[database.PortalKey]*Portal
	portalsLock   sync.Mutex

	puppets     map[string]*Puppet
	puppetsLock sync.Mutex

	StateStore *database.SQLStateStore
}

func New(cfg *config.Config) (*Bridge, error) {
	// Create the logger.
	logger, err := cfg.CreateLogger()
	if err != nil {
		return nil, err
	}

	logger.Infoln("Initializing version", version.String)

	// Create and initialize the app service.
	appservice, err := cfg.CreateAppService()
	if err != nil {
		return nil, err
	}
	appservice.Log = log.Sub("matrix")

	appservice.Init()

	// Create the bot.
	bot := appservice.BotIntent()

	// Setup the database.
	db, err := cfg.CreateDatabase(logger)
	if err != nil {
		return nil, err
	}

	// Create the state store
	logger.Debugln("Initializing state store")
	stateStore := database.NewSQLStateStore(db)
	appservice.StateStore = stateStore

	// Create the bridge.
	bridge := &Bridge{
		as:     appservice,
		db:     db,
		bot:    bot,
		Config: cfg,
		log:    logger,

		usersByMXID: make(map[id.UserID]*User),
		usersByID:   make(map[string]*User),

		managementRooms: make(map[id.RoomID]*User),

		portalsByMXID: make(map[id.RoomID]*Portal),
		portalsByID:   make(map[database.PortalKey]*Portal),

		puppets: make(map[string]*Puppet),

		StateStore: stateStore,
	}

	// Setup the event processors
	bridge.setupEvents()

	return bridge, nil
}

func (b *Bridge) connect() error {
	b.log.Debugln("Checking connection to homeserver")

	for {
		resp, err := b.bot.Whoami()
		if err != nil {
			if errors.Is(err, mautrix.MUnknownToken) {
				b.log.Fatalln("Access token invalid. Is the registration installed in your homeserver correctly?")

				return fmt.Errorf("invalid access token")
			}

			b.log.Errorfln("Failed to connect to homeserver : %v", err)
			b.log.Errorfln("reconnecting in %s", reconnectDelay)

			time.Sleep(reconnectDelay)
		} else if resp.UserID != b.bot.UserID {
			b.log.Fatalln("Unexpected user ID in whoami call: got %s, expected %s", resp.UserID, b.bot.UserID)

			return fmt.Errorf("expected user id %q but got %q", b.bot.UserID, resp.UserID)
		} else {
			break
		}
	}

	b.log.Debugln("Connected to homeserver")

	return nil
}

func (b *Bridge) Start() error {
	b.log.Infoln("Bridge started")

	if err := b.connect(); err != nil {
		return err
	}

	b.log.Debugln("Starting application service HTTP server")
	go b.as.Start()

	b.log.Debugln("Starting event processor")
	go b.eventProcessor.Start()

	go b.updateBotProfile()

	go b.startUsers()

	// Finally tell the appservice we're ready
	b.as.Ready = true

	return nil
}

func (b *Bridge) Stop() {
	b.log.Infoln("Bridge stopped")
}

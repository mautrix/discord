package bridge

import (
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/appservice"

	"gitlab.com/beeper/discord/config"
	"gitlab.com/beeper/discord/version"
)

type Bridge struct {
	config *config.Config

	log log.Logger

	as             *appservice.AppService
	eventProcessor *appservice.EventProcessor
	bot            *appservice.IntentAPI
}

func New(cfg *config.Config) (*Bridge, error) {
	// Create the logger.
	logger, err := cfg.CreateLogger()
	if err != nil {
		return nil, err
	}

	logger.Infoln("Initializing version", version.String)

	// Create the app service.
	appservice, err := cfg.CreateAppService()
	if err != nil {
		return nil, err
	}
	appservice.Log = log.Sub("matrix")

	// Create the bridge.
	bridge := &Bridge{
		config: cfg,
		log:    logger,
		as:     appservice,
	}

	return bridge, nil
}

func (b *Bridge) Start() {
	b.log.Infoln("bridge started")
}

func (b *Bridge) Stop() {
	b.log.Infoln("bridge stopped")
}

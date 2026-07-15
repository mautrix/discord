package connector

import (
	"context"

	"github.com/rs/zerolog"
)

func (d *DiscordClient) refreshSafetyHub(ctx context.Context) {
	if !d.connector.Config.ReportScrubbedAccountStanding {
		return
	}

	log := zerolog.Ctx(ctx).With().Str("action", "refresh safety hub").Logger()

	log.Debug().Msg("Fetching safety hub data")
	hub, err := d.Session.SafetyHub()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to fetch safety hub information, not updating")
		return
	}

	log.Info().
		Int("account_standing", int(hub.AccountStanding.State)).
		Msg("Fetched safety hub data")

	d.vitalsMu.Lock()
	d.safetyHub = hub
	d.vitalsMu.Unlock()
}

// pokeVitals reevaluates the client's [vitals] according to current state and
// latest fetched safety hub information.
//
//   - Safety hub information is not fetched by this method.
//   - This method may end up kicking off a full sync in the background if the
//     bridge started off with bad vitals or there is one pending.
func (d *DiscordClient) pokeVitals(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	d.vitalsMu.Lock()
	{
		v := newVitals(d.Session, d.safetyHub)

		log := v.logContext(log.With()).Logger()
		log.Info().Msg("Reevaluated vitals")

		d.vitals = &v
	}
	d.vitalsMu.Unlock()

	// Emit unconditionally, somewhat relying on mautrix's deduping behavior to
	// avoid excessive bridge state sends; avoid replicating "do we need user
	// intervention?" logic here.
	d.sendCurrentState(ctx)
	// Kick off any pending full sync.
	d.beginFullSync(ctx)
}

func (d *DiscordClient) peekVitals() (v *vitals) {
	d.vitalsMu.Lock()
	defer d.vitalsMu.Unlock()
	v = d.vitals
	return
}

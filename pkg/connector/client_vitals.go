package connector

import (
	"context"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
)

func (d *DiscordClient) refreshSafetyHub(ctx context.Context) {
	if !d.connector.Config.ReportScrubbedAccountStanding {
		return
	}

	log := zerolog.Ctx(ctx).With().Str("action", "refresh safety hub").Logger()
	ctx = log.WithContext(ctx)

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

func (d *DiscordClient) pokeVitals(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	d.vitalsMu.Lock()
	{
		v := newVitals(d.Session, d.safetyHub)

		log = ptr.Ptr(v.logContext(log.With()).Logger())
		ctx = log.WithContext(ctx)
		log.Info().Msg("Reevaluated vitals")

		d.vitals = &v
	}
	d.vitalsMu.Unlock()

	// Emit unconditionally, somewhat relying on mautrix's deduping behavior to
	// avoid excessive bridge state sends.
	d.sendCurrentState(ctx)
}

func (d *DiscordClient) peekVitals() (v *vitals) {
	d.vitalsMu.Lock()
	defer d.vitalsMu.Unlock()
	v = d.vitals
	return
}

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

package discordauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func basicFetch[T any](
	ctx context.Context,
	am *AuthMachine,
	route string,
	what string,
	model *T,
	augmentReq func(*http.Request) error,
) error {
	url := am.APIBase + route
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("constructing %s request: %w", what, err)
	}

	if augmentReq != nil {
		if err := augmentReq(req); err != nil {
			return fmt.Errorf("augmenting basic %s request: %w", what, err)
		}
	}

	// TODO: Since this augments the request, verify the exact headers sent in
	// experiment fetch requests to ensure they match.
	body, err := am.exchange(ctx, req)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", what, err)
	}

	if model != nil {
		err = json.Unmarshal(body, model)
		if err != nil {
			return fmt.Errorf("unmarshaling %s: %w", what, err)
		}
	}
	return nil
}

func (am *AuthMachine) legacyExperiments(ctx context.Context) (*ExperimentsLegacy, error) {
	exps := ExperimentsLegacy{}
	err := basicFetch(ctx, am,
		"/experiments?with_guild_experiments=true",
		"legacy experiments",
		&exps,
		func(req *http.Request) error {
			// Set X-Context-Properties. This is only relevant for this endpoint.
			contextProps, err := EncodeBasicContextProperties(ContextLocationLogin)
			if err != nil {
				return fmt.Errorf("encoding login context properties: %w", err)
			}
			req.Header.Set(HeaderContextProperties, contextProps)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &exps, nil
}

func (am *AuthMachine) apexExperiments(ctx context.Context) (*ExperimentsApex, error) {
	exps := ExperimentsApex{}
	if err := basicFetch(ctx, am,
		"/apex/experiments?surface=2",
		"apex experiments",
		&exps,
		nil,
	); err != nil {
		return nil, err
	}
	return &exps, nil
}

// Prepare loads the login page and situates the AuthMachine with an
// experiments-related [Fingerprint]. It is important for Prepare to be called
// before the machine consumes credentials.
func (am *AuthMachine) Prepare(ctx context.Context) error {
	log := am.log.With().Str("action", "prepare discord auth machine").Logger()
	ctx = log.WithContext(ctx)

	if !am.Fingerprint.IsZero() {
		log.Debug().Msg("Already prepared")
		return nil
	}

	log.Info().Msg("Preparing Discord auth")

	legacy, err := am.legacyExperiments(ctx)
	if err != nil {
		return fmt.Errorf("fetching legacy experiments: %w", err)
	}

	apex, err := am.apexExperiments(ctx)
	if err != nil {
		return fmt.Errorf("fetching apex experiments: %w", err)
	}

	am.InstallationID = apex.InstallationID
	// (Apex experiments aren't fetched with the fingerprint, so only set it
	// now.)
	if !legacy.Fingerprint.IsZero() {
		am.Fingerprint = legacy.Fingerprint
	}

	return nil
}

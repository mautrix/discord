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

package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-discord/pkg/discordtransport"
)

// proxyConfigured reports whether any proxy (static or dynamic) is configured.
func (d *DiscordConnector) proxyConfigured() bool {
	return d.Bridge.Config.Proxy.IsConfigured()
}

func (d *DiscordConnector) SupportsProxy() {}

// resolveHTTPClientSettings returns the HTTP client settings that are currently
// at play. This includes any configured proxy (fetching a dynamic proxy if
// configured to do so).
//
// It may perform a blocking HTTP request to the dynamic proxy endpoint, so it
// should only be called at connection and login boundaries. Code that wants an
// HTTP client per request should use the already-resolved session.Client or
// DiscordClient.httpClient.
func (d *DiscordConnector) resolveHTTPClientSettings(
	ctx context.Context,
	reason string,
) (exhttp.ClientSettings, error) {
	proxyURL, err := d.Bridge.GetProxy(ctx, reason)
	if err != nil {
		return exhttp.ClientSettings{}, fmt.Errorf("failed to get proxy: %w", err)
	}

	settings, err := d.Bridge.GetHTTPClientSettings().WithProxy(proxyURL)
	if err != nil {
		return exhttp.ClientSettings{}, fmt.Errorf("failed to apply proxy %q: %w", proxyURL, err)
	}

	return settings, nil
}

// resolveTransport returns the REST HTTP client and the gateway (WebSocket
// upgrade) HTTP client. When proxy is true, both are configured to use any
// configured proxy. The gateway client is pinned to HTTP/1.1; see
// [discordtransport.CompileGatewayClient].
func (d *DiscordConnector) resolveTransport(
	ctx context.Context,
	reason string,
	proxy bool,
) (restClient, wsClient *http.Client, err error) {
	// NOTE(skip): This is grossly tangled. Think of a way to restructure this.
	var settings exhttp.ClientSettings
	if proxy {
		settings, err = d.resolveHTTPClientSettings(ctx, reason)
		if err != nil {
			return nil, nil, err
		}
	} else {
		settings = d.Bridge.GetHTTPClientSettings()
	}
	restClient, err = discordtransport.CompileTransport(settings, discordtransport.TransportOptions{CookieJar: true})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to compile REST transport: %w", err)
	}
	wsClient, err = discordtransport.CompileGatewayClient(settings, discordtransport.TransportOptions{CookieJar: true})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to compile gateway transport: %w", err)
	}
	return restClient, wsClient, nil
}

// applyProxyToSession resolves the proxy once and points the session's REST
// client and gateway dialer at it. Used to proxy a session that isn't yet owned
// by a DiscordClient (e.g. the @me validation during login), so the caller can
// fail the operation if the proxy can't be resolved.
func (d *DiscordConnector) applyProxyToSession(
	ctx context.Context,
	session *discordgo.Session,
	reason string,
) error {
	settings, err := d.resolveHTTPClientSettings(ctx, reason)
	if err != nil {
		return err
	}
	return discordtransport.ApplyToSession(session, settings)
}

// updateProxy re-resolves the proxy once for the given reason and applies it to
// the session's REST client, gateway dialer, and (when proxy_media is enabled)
// the media HTTP client.
//
// It returns whether the proxy was successfully updated. On failure, the
// previously applied settings are kept.
func (d *DiscordClient) updateProxy(ctx context.Context, reason string) bool {
	log := zerolog.Ctx(ctx).With().
		Str("action", "update proxy").
		Str("reason", reason).Logger()

	settings, err := d.connector.resolveHTTPClientSettings(ctx, reason)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to update proxy, keeping previous settings")
		return false
	}

	if d.Session != nil {
		if err := discordtransport.ApplyToSession(d.Session, settings); err != nil {
			log.Warn().Err(err).Msg("Failed to apply settings to session, keeping previous settings")
			return false
		}
	}

	if d.connector.Config.ProxyMedia {
		d.httpClient = settings.Compile()
	}

	log.Debug().
		Str("proxy_host", proxyHostFromSettings(settings)).
		Bool("proxying_media", d.connector.Config.ProxyMedia).
		Msg("Updated proxy")
	return true
}

func proxyHostFromSettings(settings exhttp.ClientSettings) string {
	if settings.ProxyAddress == "" {
		return "(none)"
	}
	if u, err := url.Parse(settings.ProxyAddress); err == nil {
		return u.Host
	}
	return "(unparsable)"
}

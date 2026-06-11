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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix"
)

type respGetProxy struct {
	ProxyURL string `json:"proxy_url"`
}

// getProxy returns the effective proxy URL to use for Discord traffic,
// according to the config.
func (d *DiscordConnector) getProxy(reason string) (string, error) {
	if d.Config.GetProxyFrom == "" {
		// Use the static proxy, if any.
		return d.Config.Proxy, nil
	}

	parsed, err := url.Parse(d.Config.GetProxyFrom)
	if err != nil {
		return "", fmt.Errorf("failed to parse dynamic proxy endpoint address: %w", err)
	}

	q := parsed.Query()
	q.Set("reason", reason)
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to prepare dynamic proxy request: %w", err)
	}
	req.Header.Set("User-Agent", mautrix.DefaultUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send dynamic proxy request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return "", fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	var respData respGetProxy
	err = json.NewDecoder(resp.Body).Decode(&respData)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return respData.ProxyURL, nil
}

// resolveHTTPClientSettings returns the HTTP client settings that are
// currently at play. This includes any configured proxy (fetching a dynamic
// proxy if configured to do so).
func (d *DiscordConnector) resolveHTTPClientSettings(
	resolutionReason string,
) (exhttp.ClientSettings, error) {
	proxyURL, err := d.getProxy(resolutionReason)
	if err != nil {
		return exhttp.ClientSettings{}, fmt.Errorf("failed to get proxy: %w", err)
	}

	settings, err := d.Bridge.GetHTTPClientSettings().WithProxy(proxyURL)
	if err != nil {
		return exhttp.ClientSettings{}, fmt.Errorf("failed to apply proxy %q: %w", proxyURL, err)
	}

	return settings, nil
}

// resolveTransport returns an HTTP client and WebSocket dialer
// configured to use any configured proxies.
func (d *DiscordConnector) resolveTransport(
	resolutionReason string,
) (*http.Client, *websocket.Dialer, error) {
	settings, err := d.resolveHTTPClientSettings(resolutionReason)
	if err != nil {
		return nil, nil, err
	}
	return settings.Compile(), wsDialerFromSettings(settings), nil
}

func wsDialerFromSettings(cs exhttp.ClientSettings) *websocket.Dialer {
	// Copy the default dialer so we retain its handshake timeout and buffer
	// sizes, rather than starting from a zero-valued dialer.
	dialer := *websocket.DefaultDialer
	dialer.NetDialContext = cs.Dial
	dialer.Proxy = cs.HTTPProxy
	return &dialer
}

// refreshProxy re-resolves the proxy for the given reason and applies it to
// the session's REST client, gateway dialer, and (when proxy_media is enabled)
// the media HTTP client.
//
// Upon failure, a warning is logged and the previously applied proxy settings
// are kept.
func (d *DiscordClient) refreshProxy(ctx context.Context, reason string) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "refresh proxy").
		Str("reason", reason).Logger()

	settings, err := d.connector.resolveHTTPClientSettings(reason)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to refresh proxy, keeping previous settings")
		return
	}

	if d.Session != nil {
		d.Session.Client = settings.Compile()
		d.Session.Dialer = wsDialerFromSettings(settings)
	}

	if d.connector.Config.ProxyMedia {
		d.httpClient = settings.Compile()
	}

	proxyHost := "(none)"
	if settings.ProxyAddress != "" {
		if u, err := url.Parse(settings.ProxyAddress); err == nil {
			proxyHost = u.Host
		}
	}

	log.Debug().
		Str("proxy_host", proxyHost).
		Bool("proxying_media", d.connector.Config.ProxyMedia).
		Msg("Refreshed proxy")
}

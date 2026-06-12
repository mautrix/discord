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

package discordtransport

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/imroc/req/v3"
	"go.mau.fi/util/exhttp"
	"golang.org/x/net/publicsuffix"
)

type TransportOptions struct {
	// CookieJar controls whether to create a cookie jar and attach it to the
	// returned [http.Client].
	CookieJar bool
}

func CompileTransport(
	settings exhttp.ClientSettings,
	opts TransportOptions,
) (*http.Client, error) {
	reqClient := req.C().ImpersonateChrome()

	// By default, req infers the proxy from the environment. If a proxy is not
	// specified via exhttp, we don't want one at all, so remove the
	// req-specified proxy before applying exhttp settings.
	// `MakeTransportOverride` does not remove the proxy on its own.
	reqClient.SetProxy(nil)

	// Apply exhttp ClientSettings to the req Client. Note that we reuse req's
	// transport layer (which implements Chrome impersonation) but don't use
	// its request pipeline, so we don't get automatic retry, etc.
	http := req.WithTransportOverride(settings, reqClient).Compile()

	if opts.CookieJar {
		// ClientSettings.Compile() does not instantiate a cookie jar, so do it
		// ourselves.
		jar, err := cookiejar.New(&cookiejar.Options{
			PublicSuffixList: publicsuffix.List,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create cookie jar: %w", err)
		}
		http.Jar = jar
	}

	return http, nil
}

// ApplyToSession points a discordgo session's REST client and gateway dialer at
// the given settings. The REST client impersonates Chrome's TLS fingerprint via
// [CompileTransport]; the gateway dialer only carries any configured proxy (see
// [WSDialer]).
func ApplyToSession(session *discordgo.Session, settings exhttp.ClientSettings) error {
	client, err := CompileTransport(settings, TransportOptions{CookieJar: true})
	if err != nil {
		return err
	}
	session.Client = client
	session.Dialer = WSDialer(settings)
	return nil
}

// WSDialer builds a gateway WebSocket dialer for the given settings, applying
// any configured proxy.
func WSDialer(settings exhttp.ClientSettings) *websocket.Dialer {
	// Copy the default dialer so we retain its handshake timeout and buffer
	// sizes, rather than starting from a zero-valued dialer.
	dialer := *websocket.DefaultDialer
	dialer.NetDialContext = settings.Dial
	dialer.Proxy = settings.HTTPProxy
	return &dialer
}

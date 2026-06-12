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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/imroc/req/v3"
	utls "github.com/refraction-networking/utls"
	"go.mau.fi/util/exhttp"
	"golang.org/x/net/publicsuffix"
)

type TransportOptions struct {
	// CookieJar controls whether to create a cookie jar and attach it to the
	// returned [http.Client].
	CookieJar bool
}

// compileChromeClient builds an [http.Client] whose transport impersonates
// Chrome's TLS fingerprint (via req's uTLS-backed transport) and applies any
// given exhttp settings (proxy, timeouts, HTTP/1.1 forcing, etc.).
func compileChromeClient(
	settings exhttp.ClientSettings,
	opts TransportOptions,
	onlyAdvertiseHTTP1InALPN bool,
) (*http.Client, error) {
	reqClient := req.C().ImpersonateChrome()

	// By default, req infers the proxy from the environment. If a proxy is not
	// specified via exhttp, we don't want one at all, so remove the
	// req-specified proxy before applying exhttp settings.
	// MakeTransportOverride does not remove the proxy on its own.
	reqClient.SetProxy(nil)

	if onlyAdvertiseHTTP1InALPN {
		forceHTTP1ChromeFingerprint(reqClient)
	}

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

// CompileTransport builds the REST HTTP client: Chrome-impersonating and, like
// real Chrome's API traffic, free to negotiate HTTP/2 over ALPN.
func CompileTransport(
	settings exhttp.ClientSettings,
	opts TransportOptions,
) (*http.Client, error) {
	return compileChromeClient(settings, opts, false)
}

// CompileGatewayClient builds the HTTP client used to perform the Discord
// Gateway WebSocket handshake. Implementation-wise, it is identical to
// [CompileTransport] except that it pins the connection to HTTP/1.1.
//
// This is for two reasons:
//   - coder/websocket doesn't implement WebSockets over HTTP/2 (RFC 8441).
//   - Modern Chrome doesn't actually seem to use HTTP/2 for WebSockets.
func CompileGatewayClient(
	settings exhttp.ClientSettings,
	opts TransportOptions,
) (*http.Client, error) {
	// This doesn't actually do much (it eventually tells req to not bother
	// setting up an HTTP/2 transport), since it doesn't actually affect the
	// ClientHello.
	settings.DisableHTTP2 = true
	return compileChromeClient(settings, opts, true)
}

// forceHTTP1ChromeFingerprint overrides the req client's TLS handshake so the
// uTLS ClientHello keeps Chrome's full fingerprint but advertises _only_
// http/1.1 in ALPN.
func forceHTTP1ChromeFingerprint(c *req.Client) {
	// (This is adapted from uTLS's SetTLSFingerprint.)
	c.SetTLSHandshake(func(ctx context.Context, addr string, plainConn net.Conn) (net.Conn, *tls.ConnectionState, error) {
		hostname := addr
		if i := strings.LastIndex(addr, ":"); i != -1 {
			hostname = addr[:i]
		}

		// NOTE: The ClientHelloID here _must_ match what req's
		// ImpersonateChrome uses.
		spec, err := utls.UTLSIdToSpec(utls.HelloChrome_120)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build Chrome uTLS spec: %w", err)
		}

		// The actual changes we're making here:
		exts := spec.Extensions[:0]
		for _, ext := range spec.Extensions {
			switch e := ext.(type) {
			// Drop the ALPS (application_settings) extension. Modern Chrome
			// will stop offering h2 there when ALPN omits it. Match that
			// behavior.
			case *utls.ApplicationSettingsExtension:
				continue

			// Patch the ALPN extension to exclusively offer http/1.1.
			case *utls.ALPNExtension:
				e.AlpnProtocols = []string{"http/1.1"}
			}
			exts = append(exts, ext)
		}
		spec.Extensions = exts

		tlsConfig := c.GetTLSClientConfig()
		uconn := utls.UClient(plainConn, &utls.Config{
			ServerName:         hostname,
			NextProtos:         []string{"http/1.1"},
			RootCAs:            tlsConfig.RootCAs,
			InsecureSkipVerify: tlsConfig.InsecureSkipVerify,
			KeyLogWriter:       tlsConfig.KeyLogWriter,
		}, utls.HelloCustom)
		if err := uconn.ApplyPreset(&spec); err != nil {
			return nil, nil, fmt.Errorf("failed to apply Chrome uTLS spec: %w", err)
		}
		if err := uconn.HandshakeContext(ctx); err != nil {
			return nil, nil, err
		}

		cs := uconn.ConnectionState()
		return uconn, &tls.ConnectionState{
			Version:            cs.Version,
			HandshakeComplete:  cs.HandshakeComplete,
			DidResume:          cs.DidResume,
			CipherSuite:        cs.CipherSuite,
			NegotiatedProtocol: cs.NegotiatedProtocol,
			ServerName:         cs.ServerName,
			PeerCertificates:   cs.PeerCertificates,
			VerifiedChains:     cs.VerifiedChains,
		}, nil
	})
}

// ApplyToSession points a discordgo session's REST client and gateway HTTP
// client at the given settings. Both impersonate Chrome's TLS fingerprint; the
// REST client may use HTTP/2 ([CompileTransport]) while the gateway client is
// pinned to HTTP/1.1 for the WebSocket upgrade ([CompileGatewayClient]).
func ApplyToSession(session *discordgo.Session, settings exhttp.ClientSettings) error {
	restClient, err := CompileTransport(settings, TransportOptions{CookieJar: true})
	if err != nil {
		return err
	}
	gatewayClient, err := CompileGatewayClient(settings, TransportOptions{CookieJar: true})
	if err != nil {
		return err
	}
	session.Client = restClient
	session.GatewayHTTPClient = gatewayClient
	return nil
}

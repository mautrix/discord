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

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/imroc/req/v3"
	utls "github.com/refraction-networking/utls"
)

func compileTransport(onlyAdvertiseHTTP1InALPN bool, proxy *url.URL) http.RoundTripper {
	reqClient := req.C().ImpersonateChrome()
	if proxy != nil {
		reqClient.SetProxy(http.ProxyURL(proxy))
	} else {
		reqClient.SetProxy(nil)
	}
	if onlyAdvertiseHTTP1InALPN {
		forceHTTP1ChromeFingerprint(reqClient)
		reqClient.EnableForceHTTP1()
	}
	return reqClient.Transport
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

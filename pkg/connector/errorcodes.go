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

import "maunium.net/go/mautrix/bridgev2/status"

const (
	DCNotLoggedIn             status.BridgeStateErrorCode = "dc-not-logged-in"
	DCWebsocketDisconnect4004 status.BridgeStateErrorCode = "dc-websocket-disconnect-4004"
	DCUnknownWebsocketError   status.BridgeStateErrorCode = "dc-unknown-websocket-error"
	DCHTTP40002               status.BridgeStateErrorCode = "dc-http-40002"
	DCProxyResolveFail        status.BridgeStateErrorCode = "dc-proxy-resolve-fail"
)

// [status.BridgeStateErrorCode]s for each required action. DCRequireCaptcha is
// excluded as that RequiredAction is legacy.
const (
	DCRequireAgreements                       status.BridgeStateErrorCode = "dc-require-agreements"                           // "Terms of Service and Policy Updates"
	DCRequireVerifiedEmail                    status.BridgeStateErrorCode = "dc-require-verified-email"                       // add a verified email
	DCRequireVerifiedPhone                    status.BridgeStateErrorCode = "dc-require-verified-phone"                       // add a verified phone number
	DCRequireReverifiedEmail                  status.BridgeStateErrorCode = "dc-require-reverified-email"                     // reaffirm ownership of existing email
	DCRequireReverifiedPhone                  status.BridgeStateErrorCode = "dc-require-reverified-phone"                     // reaffirm ownership of existing phone number
	DCRequireVerifiedEmailOrVerifiedPhone     status.BridgeStateErrorCode = "dc-require-verified-email-or-verified-phone"     // add a verified phone number or email
	DCRequireReverifiedEmailOrVerifiedPhone   status.BridgeStateErrorCode = "dc-require-reverified-email-or-verified-phone"   // reaffirm ownership of existing email, or add a verified phone number
	DCRequireVerifiedEmailOrReverifiedPhone   status.BridgeStateErrorCode = "dc-require-verified-email-or-reverified-phone"   // add a verified email, or reaffirm ownership of existing phone number
	DCRequireReverifiedEmailOrReverifiedPhone status.BridgeStateErrorCode = "dc-require-reverified-email-or-reverified-phone" // reaffirm ownership of existing email or phone number
	DCRequireSafetyFlows                      status.BridgeStateErrorCode = "dc-require-safety-flows"                         // server-driven safety flow UI

	// NOTE: We expect the user to use a first-party client to read their
	// system messages. Pressing the CTA button on the modal triggers a PATCH
	// /users/@me with {flags:0}, clearing the HAS_UNREAD_URGENT_MESSAGES flag
	// and dispatching a USER_UPDATE on the gateway (that we may observe).
	//
	// Critically, this HTTP request itself is likely to prompt an in-app
	// CAPTCHA challenge.
	DCUnreadSystemMessages status.BridgeStateErrorCode = "dc-unread-system-messages"
)
const accountVerificationRequiredMessage = "You need to verify your account in the Discord app."

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		DCWebsocketDisconnect4004: "Please log in to your Discord account again.",
		DCNotLoggedIn:             "Please log in to your Discord account.",
		DCProxyResolveFail:        "Failed to update proxy",
		DCHTTP40002:               accountVerificationRequiredMessage,
		// (For DCUnknownWebsocketError, provide a specific error message when
		// sending state. If there were a generic message here, it would
		// overwrite that.)
		DCRequireAgreements:                       "Discord updated their terms and policies. Please open the Discord app to review them.",
		DCRequireVerifiedEmail:                    "Please use the Discord app to add a verified email address to your account.",
		DCRequireVerifiedPhone:                    "Please use the Discord app to add a verified phone number to your account.",
		DCRequireReverifiedEmail:                  "Please use the Discord app to verify your email address.",
		DCRequireReverifiedPhone:                  "Please use the Discord app to verify your phone number.",
		DCRequireVerifiedEmailOrVerifiedPhone:     "Please use the Discord app to add a verified email address or phone number to your account.",
		DCRequireReverifiedEmailOrVerifiedPhone:   "Please use the Discord app to verify your email address, or add a verified phone number to your account.",
		DCRequireVerifiedEmailOrReverifiedPhone:   "Please use the Discord app to verify your phone number, or add a verified email address to your account.",
		DCRequireReverifiedEmailOrReverifiedPhone: "Please use the Discord app to verify your phone number or email address.",
		DCRequireSafetyFlows:                      "Please use the Discord app to complete a safety check.",
		DCUnreadSystemMessages:                    "Discord has an important message for you. Please open the Discord app to read it.",
	})
}

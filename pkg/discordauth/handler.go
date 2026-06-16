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

import "context"

// Specify a ChallengeHandler when creating an [AuthMachine] (via
// [NewAuthMachine]) to implement handling for flows that "interrupt" the login
// process, such as CAPTCHAs or MFA.
//
// In other words, this interface constitutes the essential client "hook point"
// where you may inject your own behaviors into the login flow. In these
// methods, you will likely need to update your user interface and or prompt
// the user.
//
// [AuthMachine] will call the methods on this interface as necessary at the
// correct moments and handle all of the required plumbing.
type ChallengeHandler interface {
	// Discord presented a CAPTCHA. Let the user solve it and return their
	// solution out of this method.
	SolveCaptcha(context.Context, *Captcha) (*CaptchaSolution, error)

	// The password was accepted as part of the login, but MFA is at play.
	// Inspect the MFAChallenge to see which MFA methods are permitted and
	// prompt the user accordingly. This is also how you may request an SMS
	// code. Once you have a code, return it via MFAContinue.
	ContinueMFA(context.Context, *MFAChallenge) (*MFAContinue, error)

	// The login is valid and was accepted by Discord, but the login is
	// occurring from an unfamiliar IP address. An email has been sent to the
	// user and they must visit the enclosed link and authorize the IP address
	// before the login may be retried.
	WaitForEmailVerification(context.Context) error
}

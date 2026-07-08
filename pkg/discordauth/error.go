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
	"encoding/json"
	"fmt"
	"strings"
)

// TODO(skip): Some overlap with this and discordgo. Sort that out.

type APIError struct {
	Message string  `json:"message"`
	Code    ErrCode `json:"code"`

	// Detailed errors. Returned for e.g. [InvalidFormBody].
	//
	// The keys in this map correspond to the top-level keys that were sent in
	// your request body. The value reconstructs the shape of the data that was
	// sent, with arbitrary depth.
	//
	// For example, a request body such as
	//
	//     { "friends": [ { "enjoys_pineapple_on_pizza": false } ] }
	//
	// might result in this erroneous reply:
	//
	//     {
	//       ...,
	//       "errors": {
	//         "friends": {
	//           "0": {
	//             "enjoys_pineapple_on_pizza": {
	//               "_errors": [
	//                 {
	//                   "code": "CHECK_YOUR_OPINION",
	//                   "message": "Everybody likes pineapple on pizza. Try again."
	//                 }
	//               ]
	//             }
	//           }
	//         }
	//       }
	//     }
	//
	// Notice how:
	//
	// - The intermediate values are always objects. Array indices are
	//   represented with strings.
	//
	// - The erroneous request value terminates in an object containing an
	//   array keyed under _errors.
	//
	// The _errors array further contains objects of shape { code, message }.
	Errors map[string]json.RawMessage `json:"errors"`

	// The raw HTTP response body.
	ResponseBody []byte `json:"-"`
}

// A FormError communicates detailed error information for certain JSON field.
type FormError struct {
	Code    FormErrorCode `json:"code"`
	Message string        `json:"message"`
}

type FormErrorCode string

const (
	// AccountLoginVerificationEmail is raised when the user is logging in from
	// a new IP address and must check their email for a verification link.
	AccountLoginVerificationEmail FormErrorCode = "ACCOUNT_LOGIN_VERIFICATION_EMAIL"

	// AccountCompromisedResetPassword is raised when Discord is forcing the
	// user to reset the password to their account.
	AccountCompromisedResetPassword FormErrorCode = "ACCOUNT_COMPROMISED_RESET_PASSWORD"

	// InvalidLogin is raised when the username/phone or password was
	// incorrect.
	InvalidLogin FormErrorCode = "INVALID_LOGIN"
)

// FormFieldErrors returns the [FormError] values associated with the given key
// that had been sent in the request. If the key isn't present or the errors
// array is empty for whatever reason, nil is returned.
//
// NOTE/TODO: This function does not currently support accessing fields beyond
// the first level.
func (err *APIError) FormFieldErrors(key string) ([]FormError, error) {
	leafMsg, ok := err.Errors[key]
	if !ok {
		return nil, nil
	}

	type ErrorsLeaf struct {
		Errors []FormError `json:"_errors"`
	}

	var leaf ErrorsLeaf
	if err := json.Unmarshal(leafMsg, &leaf); err != nil {
		return nil, fmt.Errorf("failed to unmarshal errors leaf: %w", err)
	}

	return leaf.Errors, nil
}

// AnyFieldHasError reports whether any field has a given [FormErrorCode].
// false is returned on error conditions.
func (err *APIError) AnyFieldHasError(code FormErrorCode) bool {
	for key := range err.Errors {
		if err.FieldHasError(key, code) {
			return true
		}
	}
	return false
}

// FieldHasError reports whether a certain field has a given [FormErrorCode].
// false is returned on error conditions.
func (err *APIError) FieldHasError(key string, code FormErrorCode) bool {
	errs, inspectionErr := err.FormFieldErrors(key)
	if inspectionErr != nil {
		return false
	}
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

var _ error = (*APIError)(nil)

// IsUserInputError reports whether the error was ultimately due to user input
// error. When this is true, it is appropriate to prompt the user for the same
// value again.
func (err APIError) IsUserInputError() bool {
	invalidForm := err.Code == InvalidFormBody && !err.RequiresEmailVerification()
	return invalidForm ||
		err.Code == MFAInvalidCode ||
		err.Code == InvalidVerificationCode
}

// RequiresEmailVerification reports whether the error is due to a correct
// login from an unfamiliar IP address.
//
// An email is automatically sent to the user containing a link to verify the
// IP. After this is done, the login can be retried.
func (err APIError) RequiresEmailVerification() bool {
	return err.Code == InvalidFormBody &&
		err.FieldHasError("login", AccountLoginVerificationEmail)
}

// RequiresPhoneVerification reports whether the error is due to Discord
// requiring phone number verification.
func (err APIError) RequiresPhoneVerification() bool {
	return err.Code == SMSAuthVerificationNeeded
}

func (err APIError) Error() string {
	msg := fmt.Sprintf("Discord API error %d: \"%s\"", err.Code, err.Message)

	if err.Code == InvalidFormBody && err.Errors != nil {
		fieldErrors := make([]string, 0)

		for key := range err.Errors {
			errors, err := err.FormFieldErrors(key)
			if err != nil {
				continue
			}

			summaries := make([]string, 0)
			for _, error := range errors {
				summaries = append(summaries, fmt.Sprintf("\"%s\" (%s)", error.Message, error.Code))
			}

			fieldErrors = append(fieldErrors, fmt.Sprintf("%s: %s", key, strings.Join(summaries, ", ")))
		}

		return msg + ": " + strings.Join(fieldErrors, "; ")
	}

	return msg
}

type ErrCode int

const (
	RateLimited         ErrCode = 31001
	RateLimitedResource ErrCode = 31002

	AccountScheduledForDeletion ErrCode = 20011
	AccountDisabled             ErrCode = 20013

	Unauthorized              ErrCode = 40001
	AccountVerificationNeeded ErrCode = 40002
	CloudflareBlocked         ErrCode = 40333

	InvalidAuthenticationToken ErrCode = 50014
	InvalidFormBody            ErrCode = 50035
	InvalidVerificationCode    ErrCode = 50037

	MFAAlreadyEnrolled                ErrCode = 60001
	MFANotEnrolled                    ErrCode = 60002
	MFARequired                       ErrCode = 60003
	MustBeVerified                    ErrCode = 60004
	MFAInvalidSecret                  ErrCode = 60005
	MFAInvalidAuthTicket              ErrCode = 60006
	MFAInvalidCode                    ErrCode = 60008
	MFAInvalidSession                 ErrCode = 60009
	SMSAuthNotEnrolled                ErrCode = 60010
	InvalidKey                        ErrCode = 60011
	SMSAuthCannotBeEnabled            ErrCode = 60012
	MFARequiredForShopListings        ErrCode = 60015
	MFAEmailIneligible                ErrCode = 60019
	CredentialUndiscoverableOrInvalid ErrCode = 60021

	SMSAuthUnableToSendMessage              ErrCode = 70003
	SMSAuthPhoneNumberRecentlyUsedElsewhere ErrCode = 70004
	SMSAuthPhoneNumberIsVoIPOrLandline      ErrCode = 70005
	SMSAuthVerificationNeeded               ErrCode = 70007
	SMSAuthPhoneNumberAlreadyUsedElsewhere  ErrCode = 70008
	PasswordResetLinkSentToEmail            ErrCode = 70009
	SMSAuthPhoneNumberCannotBeAssociated    ErrCode = 70011
)

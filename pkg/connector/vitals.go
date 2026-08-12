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
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
)

// vitals summarizes account-level health and safety signals for the logged-in
// Discord user, as observed at the time it was built.
type vitals struct {
	// Quarantined reports whether the user's account has been placed under
	// "Limited Access". Accounts in this state cannot send friend requests,
	// join new guilds, nor initiate new direct messages.
	//
	// For more information, see Discord's support article on the topic:
	// https://support.discord.com/hc/en-us/articles/6461420677527-Limited-Access-FAQ
	Quarantined bool `json:"quarantined"`
	// A RequiredAction is some interactive user flow that must be completed
	// before the user can continue using their Discord account.
	RequiredAction discordgo.RequiredAction `json:"required_action,omitempty"`
	// HasUnreadSystemMessages reports whether the user has unread messages from
	// Discord's official system account.
	//
	// First-party clients clear this flag once the user opens that system DM.
	HasUnreadSystemMessages bool `json:"has_unread_system_messages"`

	// FlaggedAsSpammer reports whether Discord has flagged the account as a
	// spammer.
	//
	// Other users see a flagged account's messages as collapsed by default.
	// However, other flagged spammers see them normally.
	FlaggedAsSpammer bool `json:"flagged_as_spammer"`

	// VerifiedEmail reports whether the user has verified their account's
	// email.
	//
	// If this is false, [discordgo.ErrCodeActionRequiredVerifiedAccount]
	// errors are likely.
	VerifiedEmail bool `json:"verified_email"`
	// HasPhone reports whether a verified phone number is associated with the
	// user account.
	HasPhone bool `json:"has_phone"`

	Safety *vitalsSafety `json:"safety,omitempty"`
}

type vitalsSafety struct {
	// Standing quantifies a user account's standing with Discord.
	//
	// A value of 100 is "all good".
	Standing discordgo.AccountStanding `json:"standing"`
	// AffectedBySpamClassification reports whether an active spam
	// classification currently applies to the account.
	//
	// This does not include classifications that were caused by guild
	// membership nor ownership.
	AffectedBySpamClassification bool `json:"affected_by_spam_classification"`
}

// newVitals returns the vitals derived from the session s and information
// fetched from the Discord safety hub.
//
// If hub is nil, [vitals.Safety] will also be nil.
func newVitals(
	s *discordgo.Session,
	hub *discordgo.SafetyHub,
) (v vitals) {
	if s == nil || s.State.User == nil {
		return
	}
	s.State.RLock()
	defer s.State.RUnlock()
	user := s.State.User
	flags := user.Flags

	v.Quarantined = flags&discordgo.UserFlagQuarantined != 0
	// This can change on the fly via USER_REQUIRED_ACTION_UPDATE from the
	// gateway.
	v.RequiredAction = s.State.RequiredAction
	v.HasUnreadSystemMessages = flags&discordgo.UserFlagHasUnreadUrgentMessages != 0
	v.FlaggedAsSpammer = flags&discordgo.UserFlagSpammer != 0
	v.VerifiedEmail = user.Verified
	v.HasPhone = user.Phone != ""

	if hub != nil {
		v.Safety = &vitalsSafety{}
		v.Safety.Standing = hub.AccountStanding.State
		v.Safety.AffectedBySpamClassification = affectedBySpamClassification(hub)
	}

	return
}

// RequiresUserIntervention reports whether the account is currently in a state
// that the bridge cannot resolve on its own, and which blocks normal operation
// until the user personally resolves the condition.
//
// Persistent flags and states that don't _necessarily_ hamper bridge operation
// are not considered by this function.
func (v *vitals) RequiresUserIntervention() bool {
	if v == nil {
		return false
	}

	if v.RequiredAction != "" {
		return true
	}

	if v.HasUnreadSystemMessages {
		// We can technically resolve this on behalf of the user via PATCH
		// /users/@me (and this is what first-party clients ultimately do), but
		// it's probably best to have them use the official app for now.
		return true
	}

	return false
}

// Unimpeded reports whether user intervention is not currently required _and_
// Discord has not applied any long-standing flags or states to the account
// that could potentially hamper bridge operation and account reputation.
func (v *vitals) Unimpeded() bool {
	if v == nil {
		return false
	}
	if v.RequiresUserIntervention() {
		return false
	}
	if v.Quarantined || v.FlaggedAsSpammer {
		return false
	}
	if v.Safety != nil {
		if v.Safety.Standing != discordgo.StandingAllGood || v.Safety.AffectedBySpamClassification {
			return false
		}
	}
	return true
}

func (v *vitals) infoMap() (map[string]any, error) {
	// This is a bit gross but guarantees that the in-memory map representation
	// remain consistent with the JSON one.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (v *vitals) logContext(c zerolog.Context) zerolog.Context {
	c = c.Bool("vitals_quarantined", v.Quarantined).
		Bool("vitals_has_unread_system_messages", v.HasUnreadSystemMessages).
		Bool("vitals_flagged_as_spammer", v.FlaggedAsSpammer).
		Bool("vitals_verified_email", v.VerifiedEmail).
		Bool("vitals_has_phone", v.HasPhone)
	if v.RequiredAction != "" {
		c = c.Str("vitals_required_action", string(v.RequiredAction))
	}
	if v.Safety != nil {
		c = c.Int("vitals_safety_standing", int(v.Safety.Standing)).
			Bool("vitals_safety_affected_by_spam_classification", v.Safety.AffectedBySpamClassification)
	}
	c = c.Bool("vitals_unimpeded", v.Unimpeded()).
		Bool("vitals_requires_user_intervention", v.RequiresUserIntervention())
	return c
}

func affectedBySpamClassification(hub *discordgo.SafetyHub) bool {
	now := time.Now()
	cs := slices.Concat(hub.Classifications, hub.GuildClassifications)
	for _, c := range cs {
		if c.MaxExpirationTime != nil && now.After(*c.MaxExpirationTime) {
			// Classification has expired.
			continue
		}
		if c.GuildMetadata != nil {
			// Disregard classifications caused by guild membership or
			// ownership.
			continue
		}
		if !(c.IsSpam || strings.ToLower(strings.TrimSpace(c.Description)) == "spam") {
			// Classification is not due to spam.
			continue
		}
		return true
	}
	return false
}

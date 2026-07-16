# Vitals

Each user login on the bridge (i.e. each `DiscordClient`) collates a number of
account-level health and safety signals into an (effectively) immutable
structure named `vitals`.

The main goals of this abstraction are to (eventually):

- Aid in tracing the cause of rare failures; vitals are logged every time they
  are evaluated.
- Have an easily serializable bag of data that can be used to provide a rough
  measure of how "at risk" the user account is.
- Automatically inform bridge operation by helping determine which API/Gateway
  actions are likely to fail. (This is not yet implemented.)
- Define simple logic around whether "user intervention" is required (which
  prompts the bridge to halt all outgoing activity and enter `BAD_CREDENTIALS`
  in order to protect the user's account and be a good API citizen).

## Signals

The following is an exhaustive enumeration of the fields that are present on the
`vitals` struct, which is reported under the `info.vitals` field of the mautrix
bridge state. For example:

```json
"info": {
  // (... other, arbitrary data ...)
  "vitals": {
    "flagged_as_spammer": false,
    "has_phone": false,
    "has_unread_system_messages": false,
    "quarantined": false,
    "required_action": "AGREEMENTS",
    "safety": {
      "affected_by_spam_classification": false,
      "standing": 100
    },
    "verified_email": true
  }
}
```

(Because `required_action` is a "hard" signal, as described below, this vitals
state implies that the bridge is in `BAD_CREDENTIALS`.)

A `?` character following the type of a field indicates that the field may be
entirely absent (i.e. not merely `null`) under the described circumstances.

### "Inert" Signals

Inert signals are passive attributes that may explain certain failures.

#### `verified_email` boolean

- Presence/nullability: Always present
- Origin: user object's `verified` field
- Live updating: Unknown (however, correctly incorporated if it were to be sent
  via `USER_UPDATE`)
- Effect on bridge: Likely to cause backfill failures (TODO: should eventually
  pause backfill queues when this happens)

User accounts that lack a verified email are very likely to receive API code
error 40002 ("You need to verify your account in order to perform this action").

Note that first-party clients strongly encourage you to verify your email in
many UI surfaces immediately after registration.

#### `has_phone` boolean

- Presence/nullability: Always present
- Origin: presence of non-empty string on the user object's `phone` field
- Live updating: Unknown (however, correctly incorporated if it were to be sent
  via `USER_UPDATE`)
- Effect on bridge: None

Denotes whether the user has a phone number associated with their account that
has been verified (via e.g. SMS).

### "Soft" Signals

Signals explicitly applied by Discord that may or may not impede bridge
operation. These are reported because they would be significant in the event of
bridge malfunction, and useful for reconstructing the cause of a failure.

#### `quarantined` boolean

- Presence/nullability: Always present
- Origin: bit `1 << 44` (`QUARANTINED`) on the user object's `flags` bit field
- Live updating: Unknown (however, correctly incorporated if it were to be sent
  via `USER_UPDATE`)
- Effect on bridge: None (TODO: should eventually affect bridge operation)

Discord's automated heuristics may decide to place arbitrary restrictions on
users' accounts, "quarantining" them. This places limitations on the following
operations (this list is likely incomplete):

- Sending outgoing friend requests
- Sending direct messages to friends added _while_ quarantined
- Joining new guilds ("servers")
- Creating private channels (starting new direct messages)

Direct messages may be sent as usual to friends who were added before the
`QUARANTINED` flag was applied.

For more information, see:

- ["Limited Access FAQ" - Discord Support](https://support.discord.com/hc/en-us/articles/6461420677527-Limited-Access-FAQ)

#### `flagged_as_spammer` boolean

- Presence/nullability: Always present
- Origin: bit `1 << 20` (`SPAMMER`) on the user object's `flags` bit field
- Live updating: Unknown (however, correctly incorporated if it were to be sent
  via `USER_UPDATE`)
- Effect on bridge: None

Other users see messages from a `SPAMMER`-flagged account as collapsed by
default. However, other `SPAMMER`-flagged accounts see them normally.

This flag can be applied automatically and heuristically, even to user accounts
with verified phone numbers (where `has_phone` is `true`).

#### `safety` object?

This is a sub-object directly under `vitals`.

It is completely absent when the bridge configuration's
`report_scrubbed_account_standing` field is `false` (which is the default
value). Otherwise, the object itself is present once a Safety Hub fetch has
succeeded.

Note that "scrubbed" merely refers to the omission of any personally
identifiable information in the following fields; Safety Hub classification data
inherently encapsulates sensitive data as it replicates any offending content
for the user to see (in first-party clients; the bridge's data modeling does not
make an effort to deserialize the sensitive fields).

##### `safety.standing` integer

- Presence/nullability: Always present with parent
- Origin: Safety Hub (GET `/api/v…/safety-hub/@me`)
- Live updating: Yes (based on best-effort, unverified heuristics)
- Effect on bridge: None (TODO: should eventually affect bridge operation)

A number that quantifies the user account's standing with Discord as visible in
the "Safety Hub". The currently known values are:

| Value | User facing description in first-party clients | Note          |
| ----- | ---------------------------------------------- | ------------- |
| 100   | "Your account is all good"                     | Natural state |
| 200   | "Your account is limited"                      |
| 300   | "Your account is very limited"                 |
| 400   | "Your account is at risk"                      |
| 500   | "Your account is suspended"                    |

##### `safety.affected_by_spam_classification` boolean

- Presence/nullability: Always present with parent
- Origin: Safety Hub (GET `/api/v…/safety-hub/@me`)
- Live updating: Yes (based on best-effort, unverified heuristics)
- Effect on bridge: None

Denotes whether an active spam classification currently applies to the user
account. This does not include classifications that were caused by guild
membership or ownership.

> TODO: The bridge needs to poke its vitals once a classification's expiration
> time is reached.

### "Hard" Signals

All "hard" signals _immediately_ prevent further usage of the bridge. When a
"hard" signal is detected, the bridge is immediately put into `BAD_CREDENTIALS`
and most outgoing requests to Discord fail with
[`ErrNotLoggedIn`][not-logged-in] (see [Bridge State](#bridge-state)). This
condition is also internally referred to as "requiring user intervention"
(`RequiresUserIntervention`).

[not-logged-in]:
	https://github.com/mautrix/go/blob/f6531777f56c4a8276b65c1439e991b860c1ecb9/bridgev2/errors.go#L35

The bridge tries its best to continue bridging _incoming_ events, especially
since it is important that the urgent Discord system message be made visible to
the user as soon as possible. However, all outgoing operations such as message
sending, editing, deletion, reactions, etc. will fail.

Whether user intervention is currently required is not directly reported in the
bridge state's `info`, but is logged at runtime. A `BAD_CREDENTIALS` state can
be used to infer if intervention is needed.

#### `required_action` string?

- Presence/nullability: Entirely absent when not applicable
- Origin: `required_action` field on the `READY` payload received from the
  Gateway
- Live updating: Yes (via `USER_REQUIRED_ACTION_UPDATE`)
- Effect on bridge: **Requires user intervention**

A required action is applied to a user account when an interactive security or
safety flow must be completed before the account may be further used.

The currently known values are:

| `required_action`                                    | Bridge state error code                           | Resolution process in a first-party client                            |
| ---------------------------------------------------- | ------------------------------------------------- | --------------------------------------------------------------------- |
| `AGREEMENTS`                                         | `dc-require-agreements`                           | Read and review potential terms of service and/or policy updates      |
| `REQUIRE_CAPTCHA` (legacy; not expected in practice) | (unmapped)                                        | N/A                                                                   |
| `REQUIRE_VERIFIED_EMAIL`                             | `dc-require-verified-email`                       | Add a verified email                                                  |
| `REQUIRE_VERIFIED_PHONE`                             | `dc-require-verified-phone`                       | Add a verified phone number                                           |
| `REQUIRE_REVERIFIED_EMAIL`                           | `dc-require-reverified-email`                     | Reaffirm ownership of existing email                                  |
| `REQUIRE_REVERIFIED_PHONE`                           | `dc-require-reverified-phone`                     | Reaffirm ownership of existing phone number                           |
| `REQUIRE_VERIFIED_EMAIL_OR_VERIFIED_PHONE`           | `dc-require-verified-email-or-verified-phone`     | Add a verified phone number or email                                  |
| `REQUIRE_REVERIFIED_EMAIL_OR_VERIFIED_PHONE`         | `dc-require-reverified-email-or-verified-phone`   | Reaffirm ownership of existing email, or add a verified phone number  |
| `REQUIRE_VERIFIED_EMAIL_OR_REVERIFIED_PHONE`         | `dc-require-verified-email-or-reverified-phone`   | Add a verified email, or reaffirm ownership of existing phone number  |
| `REQUIRE_REVERIFIED_EMAIL_OR_REVERIFIED_PHONE`       | `dc-require-reverified-email-or-reverified-phone` | Reaffirm ownership of existing email or phone number                  |
| `REQUIRE_SAFETY_FLOWS`                               | `dc-require-safety-flows`                         | Proceed through server-driven safety flow UI (age verification, etc.) |

(Note that the `AGREEMENTS` value lacks `REQUIRE_` despite containing it in the
error code.)

#### `has_unread_system_messages` boolean

- Presence/nullability: Always present
- Origin: bit `1 << 13` (`HAS_UNREAD_URGENT_MESSAGES`) on the user object's
  `flags` bit field
- Live updating: Yes (via `MESSAGE_CREATE`)
- Effect on bridge: **Requires user intervention**

Denotes that a user has unread "urgent" messages from Discord's official system
account.

These are currently known to be sent when the account standing has changed.

##### Implementation

As of 2026-07-16 it has been determined that the first-party client
synchronously mutates the in-memory (Flux) user object to incorporate this flag
when an "urgent" message is received:

```js
function Q(e) {
	let {message: t} = e;
	if ((L(t, !0), null != t.flags && r.Lt(t.flags, A.pr7.URGENT))) {
		let e = T[m.default.getId()];
		return (
			null != e &&
			((T[m.default.getId()] = e.set(
				"flags",
				r.lA(e.flags, A.nhx.HAS_UNREAD_URGENT_MESSAGES, !0),
			)),
			!0)
		);
	}
	return !1;
}
```

This behavior is replicated in our synchronous state handling path. We wait for
the user to use a first-party client to read their system messages, which leads
to this flag being removed and a resulting `USER_UPDATE` event on the Gateway.

The bridge could theoretically remove the flag itself, but we have opted not to
do this at this time as it is likely to lead to an in-app CAPTCHA challenge that
is most easily resolved in Discord's client.

## Bridge State

On the usual bridge state update path (i.e. the one that is responsible for
reporting `CONNECTED`), vitals are always consulted. If user intervention is
required, the bridge enters the `BAD_CREDENTIALS` state and most (if not all)
outgoing operations that make requests to Discord will fail with
[`ErrNotLoggedIn`][not-logged-in]. The reported `UserAction` is always
[`UserActionOpenNative`][open-native].

[open-native]:
	https://github.com/mautrix/go/blob/f6531777f56c4a8276b65c1439e991b860c1ecb9/bridgev2/status/bridgestate.go#L80

Barring any bugs, the bridge should automatically respond to any resolution
performed by the user in the first-party client, according to the "live
updating" field specified on each signal.

### Error Codes

A bridge state may only report a single error at a time, so unread system
messages currently take priority over any required action. Whether this
corresponds to the actual behavior in first-party clients is currently
unverified.

The error code for `has_unread_system_messages` being `true` is
`dc-unread-system-messages`. Each required action has a corresponding error code
that is described in the [`required_action`](#required_action-string) of this
document.

## Lifecycle

Vitals are "poked" (reevaluated) on the following events:

- Gateway `READY` (successfully connected with a fresh state snapshot).
- When the user's object is updated via Gateway `USER_UPDATE`, such as the
  `flags` bit field.
  - Critically, this handles the `HAS_UNREAD_URGENT_MESSAGES` flag being removed
    from another client.
- When an urgent system message from Discord is received.
- When the user's required action changes (`USER_REQUIRED_ACTION_UPDATE`).

As part of the reevaluation, the last fetched Safety Hub information (if
present) is incorporated into the vitals and the heuristics that follow.

After reevaluation:

- A bridge state is unconditionally
  [sent](https://github.com/mautrix/go/blob/f6531777f56c4a8276b65c1439e991b860c1ecb9/bridgev2/bridgestate.go#L293)
  to mautrix.
  - Instead of trying to be clever about when a new bridge state is truly
    needed, we implicitly rely on mautrix's deduplication logic to avoid
    excessive updates.
- If needed, a "full sync" is kicked off in the background (say, if the bridge
  started off with bad vitals and never had a chance to perform a full sync).

### Safety Hub

Safety Hub information is fetched on the following events when the bridge
configuration allows it, before vitals are poked:

- Asynchronously when an urgent system message is received.
  - When your account standing changes for whatever reason, Discord sends a
    system message that is flagged as urgent.
- Gateway `READY`.
- Gateway `RESUMED`.

The Gateway seemingly does not dedicate an event to Safety Hub information
changing, so the bridge must fetch it opportunistically.

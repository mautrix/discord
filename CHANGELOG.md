# v0.3.0 (2023-04-16)

* Added support for backfilling on room creation and missed messages on startup.
* Added options to automatically ratchet/delete megolm sessions to minimize
  access to old messages.
* Added basic support for incoming voice messages.

# v0.2.0 (2023-03-16)

* Switched to zerolog for logging.
  * The basic log config will be migrated automatically, but you may want to
    tweak it as the options are different.
* Added support for logging in with a bot account.
  The [Authentication docs](https://docs.mau.fi/bridges/go/discord/authentication.html)
  have been updated with instructions for creating a bot.
* Added support for relaying messages for unauthenticated users using a webhook.
  See [docs](https://docs.mau.fi/bridges/go/discord/relay.html) for instructions.
* Added commands to bridge and unbridge channels manually.
* Added `ping` command.
* Added support for gif stickers from Discord.
* Changed mention bridging so mentions for users logged into the bridge use the
  Matrix user's MXID even if double puppeting is not enabled.
* Actually fixed ghost user info not being synced when receiving reactions.
* Fixed uncommon bug with sending messages that only occurred after login
  before restarting the bridge.
* Fixed guild name not being synced immediately after joining a new guild.
* Fixed variation selectors when bridging emojis to Discord.

# v0.1.1 (2023-02-16)

* Started automatically subscribing to bridged guilds. This fixes two problems:
  * Typing notifications should now work automatically in guilds.
  * Huge guilds now actually get messages bridged.
* Added support for converting animated lottie stickers to raster formats using
  [lottieconverter](https://github.com/sot-tech/LottieConverter).
* Added basic bridging for call start and guild join messages.
* Improved markdown parsing to disable more features that don't exist on Discord.
* Removed width from inline images (e.g. in the `guilds status` output) to
  handle non-square images properly.
* Fixed ghost user info not being synced when receiving reactions.

# v0.1.0 (2023-01-29)

Initial release.

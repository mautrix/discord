# v0.2.0 (unreleased)

* Switched to zerolog for logging.
  * The basic log config will be migrated automatically, but you may want to
    tweak it as the options are different.
* Added support for logging in with a bot account.
* Added support for relaying messages for unauthenticated users using a webhook.
* Changed mention bridging so mentions for users logged into the bridge use the
  Matrix user's MXID even if double puppeting is not enabled.
* Actually fixed ghost user info not being synced when receiving reactions.
* Added support for gif stickers from Discord.
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

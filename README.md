# mautrix-discord

## Getting Started

To get start with this bridge you first need to create the configuration file.
You can do that by running `./discord generate-config`. By default this will
output to `config.yaml`. Edit this file as necessary.

Once you're done with the configuration file you need to generate the
registration for the Synapse. To do so run `./discord generate-registration`.
This command will update your configuration file as well where necessary.

Now that you have a registration file, be sure to add it to the
`app_service_config_files` in the `homeserver.yaml` file of your Synapse
install. Once you've done this, make sure to reload or restart Synapse.

You are no ready to start the bridge with `./discord`

From the Matrix client of your choice, create a direct message with
`@discordbot:localhost` adjusting if you changed these settings in the config.
This will be your management room with the bot.

From the management room you can now login to Discord with the `login` command.
This will present you with a QRCode that you can scan with the Discord mobile
application to login. For more detailed instructions, see the
[official documentation](https://support.discord.com/hc/en-us/articles/360039213771-QR-Code-Login-FAQ).

You should now be able to send an receive direct messages from both one on ones
and group dms. However you can't currently create the dm, so you'll have to be
invited while the bridge is running.

## Status

Complete:

 * Login via QRCode
 * Message sending for DMs and Group DMs
 * Message editing for text bodies only (see notes about attachments below)
 * Unicode standard reactions (add/remove)
 * Message deleting
 * Username formatting
 * User avatars

Bugged:

 * Changing the room title of a group dm in discord is sent as a message.

Incomplete:

 * Attachments; most details including the database layout and database api are done.

Not started:

 * Double Puppeting
 * Enumerating DM list
 * Mentions needs to be parsed, they currently show up as `<@!<userid>` in the message body.
 * Custom emoji are not yet implemented. In message emoji show up as `<:text:id>`.
 * Custom emoji reactions are not yet implemented.
 * Additional bot commands like logout

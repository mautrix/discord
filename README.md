# mautrix-discord (Forum Thread Fork)

A Matrix-Discord puppeting bridge based on [discordgo](https://github.com/bwmarrin/discordgo), with additional support for manually bridging Discord forum threads.

Upstream project: [mautrix/discord](https://github.com/mautrix/discord)

---

## Documentation

This fork README only covers forum-thread-specific additions and deployment notes.

For full bridge setup and usage, use upstream docs at [docs.mau.fi](https://docs.mau.fi/bridges/go/discord/index.html). Quick links:

- [Bridge setup](https://docs.mau.fi/bridges/go/setup.html?bridge=discord) (or [with Docker](https://docs.mau.fi/bridges/general/docker-setup.html?bridge=discord))
- Basic usage: [Authentication](https://docs.mau.fi/bridges/go/discord/authentication.html), [Relaying with webhooks](https://docs.mau.fi/bridges/go/discord/relay.html)

---

## What this fork adds

This fork is focused on practical forum-thread operations.

- Manual bridging for Discord thread channels (including forum threads)
- Explicit rejection of forum parent channel IDs in `!discord bridge`
- Matrix -> Discord relay support for thread portals when webhook relay is configured
- Thread-friendly relay webhook setup behavior (`set-relay` handles parent-channel webhook target)
- Optional bot-visible debug mode for troubleshooting

Out of scope by design:
- Auto-bridging all forum threads
- Forum tags/post metadata mapping

---

## Expected behavior

- `!discord bridge <thread_id>`: supported
- `!discord bridge <forum_parent_id>`: rejected (bridge a thread ID instead)
- Discord -> Matrix in bridged thread room: supported
- Matrix -> Discord for non-logged-in Matrix users: supported when relay webhook is configured

---

## Quick usage

1. Bridge a thread room by ID:

```text
!discord bridge <thread_id>
```

2. Configure relay for non-logged-in Matrix users:

```text
!discord set-relay --url <webhook_url>
```

You can also use `!discord set-relay --create`, but see webhook recommendations below.

---

## Webhook recommendation

Recommended workflow:

1. Create one webhook manually in Discord server/channel settings (the parent forum channel).
2. Reuse the same webhook URL in each bridged thread room with:

```text
!discord set-relay --url <webhook_url>
```

Why:
- avoids creating many webhooks under one parent channel
- easier long-term webhook management
- consistent relay setup across multiple thread portals

---

## Build and run with Docker

Build image:

```bash
docker build -t mautrix-discord:forum-thread .
```

Run (example):

```bash
docker run --rm \
  --name matrix-mautrix-discord \
  -v /path/to/config:/config:ro \
  -v /path/to/data:/data \
  mautrix-discord:forum-thread \
  /usr/bin/mautrix-discord -c /config/config.yaml -r /config/registration.yaml --no-update
```

---

## Deploy with matrix-docker-ansible-deploy

Project address: [matrix-docker-ansible-deploy](https://github.com/spantaleev/matrix-docker-ansible-deploy)

In your server `vars.yml`:

```yaml
matrix_mautrix_discord_container_image_self_build: true
matrix_mautrix_discord_container_image_self_build_repo: "https://github.com/4xura/mautrix-discord.git"
matrix_mautrix_discord_container_image_self_build_branch: "main"
matrix_mautrix_discord_version: "forum-thread-test-20260302"
```

Optional debug toggle:

```yaml
matrix_mautrix_discord_container_extra_arguments:
  - "--env=MAUTRIX_DISCORD_FORUM_THREAD_DEBUG=1"
```

Deploy:

```bash
ansible-playbook -i inventory/hosts setup.yml --tags=setup-mautrix-discord,start
```

Verify container image/tag:

```bash
docker ps
```

Expected (example):

```text
... matrix-mautrix-discord ... localhost/mautrix/discord:<your_tag> ...
```

---

## Debug mode

By default, debug information is turned off.

Toggle via env var:

```text
MAUTRIX_DISCORD_FORUM_THREAD_DEBUG=1
```

Enabled values: `1`, `true`, `yes`, `on`, `debug`

When enabled:
- command-flow debug messages are posted in the command room
- relay-flow debug notices are posted in the user management room

---

## Bugs fix

Some other bugs I've fixed in this fork against the original repo.

### Tenor GIFV Conversion

Discord GIFV/Tenor embeds were bridged as `m.video`, but `content.body` was set to the Tenor page URL (for example https://tenor.com/view/...).  

Some Matrix clients (e.g., Element) rendered this as a link/file tile instead of inline playable media.

```diff
diff --git a/portal_convert.go b/portal_convert.go
index 6823e2c..9c7b322 100644
--- a/portal_convert.go
+++ b/portal_convert.go
@@ -257,6 +257,8 @@ func (portal *Portal) convertDiscordVideoEmbed(ctx context.Context, intent *apps
 	}
 	extra := map[string]any{}
 	if content.MsgType == event.MsgVideo && embed.Type == discordgo.EmbedTypeGifv {
+		content.Body = makeGIFVFileName(embed.URL)
+		content.FileName = content.Body
 		extra["info"] = map[string]any{
 			"fi.mau.discord.gifv":  true,
 			"fi.mau.gif":           true,
@@ -274,6 +276,14 @@ func (portal *Portal) convertDiscordVideoEmbed(ctx context.Context, intent *apps
 	}
 }
 
+func makeGIFVFileName(embedURL string) string {
+	if embedURL == "" {
+		return "discord-gifv.mp4"
+	}
+	sum := sha256.Sum256([]byte(embedURL))
+	return fmt.Sprintf("discord-gifv-%s.mp4", hex.EncodeToString(sum[:4]))
+}
+
 func (portal *Portal) convertDiscordMessage(ctx context.Context, puppet *Puppet, intent *appservice.IntentAPI, msg *discordgo.Message) []*ConvertedMessage {
 	predictedLength := len(msg.Attachments) + len(msg.StickerItems)
 	if msg.Content != "" {
```

Keeps efficient MP4 relay path, but avoids URL-as-body rendering quirks in Matrix clients.

- Only affects Discord EmbedTypeGifv video embeds.
- No MP4->GIF transcoding added.
- No extra CPU-heavy conversion path (I did add, but decided to keep the original MP4-display way for the moment).

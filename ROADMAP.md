# Features & roadmap
* Matrix → Discord
  * [ ] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
    * [x] Threads
    * [ ] Custom emojis
  * [x] Message redactions
  * [x] Reactions
    * [x] Unicode emojis
    * [ ] Custom emojis (re-reacting with custom emojis sent from Discord already works)
  * [ ] Executing Discord bot commands
    * [x] Basic arguments and subcommands
    * [ ] Subcommand groups
    * [ ] Mention arguments
    * [ ] Attachment arguments
  * [ ] Presence
  * [x] Typing notifications
  * [x] Own read status
  * [ ] Power level
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Leave
    * [ ] Kick
  * [ ] Room metadata changes
    * [ ] Name
    * [ ] Avatar
    * [ ] Topic
  * [ ] Initial room metadata
* Discord → Matrix
  * [ ] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
    * [x] Threads
      * [x] Auto-joining threads when opening
      * [ ] Backfilling threads after joining
    * [x] Custom emojis
    * [x] Embeds
    * [ ] Interactive components
    * [x] Interactions (commands)
    * [x] @everyone/@here mentions into @room
  * [x] Message deletions
  * [x] Reactions
    * [x] Unicode emojis
    * [x] Custom emojis ([MSC4027](https://github.com/matrix-org/matrix-spec-proposals/pull/4027))
  * [x] Avatars
  * [ ] Presence
  * [ ] Typing notifications (currently partial support: DMs work after you type in them)
  * [x] Own read status
  * [ ] Role permissions
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [ ] Leave
    * [ ] Kick
  * [x] Channel/group DM metadata changes
    * [x] Title
    * [x] Avatar
    * [x] Description
  * [x] Initial channel/group DM metadata
  * [ ] User metadata changes
    * [ ] Display name
    * [ ] Avatar
  * [ ] Initial user metadata
    * [ ] Display name
    * [ ] Avatar
* Misc
  * [x] Login methods
    * [x] QR scan from mobile
    * [x] Manually providing access token
  * [x] Automatic portal creation
    * [x] After login
    * [x] When receiving DM
  * [ ] Private chat creation by inviting Matrix puppet of Discord user to new room
  * [x] Option to use own Matrix account for messages sent from other Discord clients

# Features & roadmap
* Matrix → Discord
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
    * [x] Threads
  * [x] Message redactions
  * [x] Reactions
    * [x] Unicode emojis
    * [ ] Custom emojis (re-reacting with custom emojis sent from Discord already works)
  * [ ] Executing Discord bot commands
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
    * [x] Interactions (commands)
  * [x] Message deletions
  * [x] Reactions
    * [x] Unicode emojis
    * [x] Custom emojis (not yet supported on Matrix)
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
  * [ ] Channel/group DM metadata changes
    * [ ] Title
    * [ ] Avatar
    * [ ] Description
  * [ ] Initial channel/group DM metadata
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
  * [ ] Automatic portal creation
    * [ ] After login
    * [x] When receiving DM
  * [ ] Private chat creation by inviting Matrix puppet of Discord user to new room
  * [x] Option to use own Matrix account for messages sent from other Discord clients

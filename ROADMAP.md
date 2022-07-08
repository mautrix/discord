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
  * [ ] Presence
  * [ ] Typing notifications
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
      * [ ] Auto-joining threads
      * [ ] Backfilling threads after joining
    * [x] Custom emojis
  * [x] Message deletions
  * [x] Reactions
    * [x] Unicode emojis
    * [x] Custom emojis (not yet supported on Matrix)
  * [x] Avatars
  * [ ] Presence
  * [ ] Typing notifications
  * [x] Own read status
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
  * [ ] Login methods
    * [x] QR scan from mobile
    * [ ] Manually providing access token
  * [ ] Automatic portal creation
    * [ ] After login
    * [x] When receiving DM
  * [ ] Private chat creation by inviting Matrix puppet of Discord user to new room
  * [x] Option to use own Matrix account for messages sent from other Discord clients

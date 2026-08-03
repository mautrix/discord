# meowcord

meowcord is a Go library that comprehensively implements [Discord's REST
API][discord-rest] and [Gateway][discord-gateway].

meowcord is a hard fork of [bwmarrin/discordgo][bwmarrin-discordgo] (via
[Beeper's fork][beeper-discordgo]), originally BSD-3-Clause.

[discord-rest]: https://docs.discord.com/developers/reference
[discord-gateway]: https://docs.discord.com/developers/events/gateway
[beeper-discordgo]: https://github.com/beeper/discordgo
[bwmarrin-discordgo]: https://github.com/bwmarrin/discordgo

<!-- prettier-ignore -->
> [!IMPORTANT]
> **meowcord does not make any stability or compatibility promises at this time
> and is not designed for external use.** meowcord is mostly intended to be used
> solely by [mautrix-discord], and therefore places emphasis on support and
> functionality for user accounts (i.e. not bot accounts).

[mautrix-discord]: https://github.com/mautrix/discord

Notable deviations and enhancements from discordgo include (but are not limited
to):

- Requires Go 1.25 (August 2025) or newer.
- Comprehensive user account support.
  - Many user-specific REST endpoints and gateway event types (OP 13, OP 14,
    etc.) have been added.
  - `X-Super-Properties`, launch signatures, heartbeat sessions, etc. are
    supported.
  - Many types have been extended with undocumented fields.
- Uses [coder/websocket][coder-ws] to communicate with the Discord Gateway
  instead of [gorilla/websocket][gorilla-ws].
- Support for [zlib transport compression
  (`compress=zlib-stream`)][zlib-stream].
- Enhanced state handling.

[zlib-stream]: https://docs.discord.com/developers/events/gateway#zlib-stream
[coder-ws]: https://github.com/coder/websocket
[gorilla-ws]: https://github.com/gorilla/websocket

## Fork Provenance & Licensing

meowcord incorporates the following commits and all of their ancestors:

- [beeper/discordgo][beeper-discordgo]:
  [`0ee5f692e9eb3a3135cbd139d8c15bc6434a8f41`](https://github.com/beeper/discordgo/commit/0ee5f692e9eb3a3135cbd139d8c15bc6434a8f41)
  (authored 2026-08-03)
- [bwmarrin/discordgo][bwmarrin-discordgo]:
  [`f43dd94faaacd5b163e9e783f14b5bd8be639fc9`](https://github.com/bwmarrin/discordgo/commit/f43dd94faaacd5b163e9e783f14b5bd8be639fc9)
  (authored 2026-02-14)

Those forks are BSD-3-Clause. This package is **now distributed under AGPL-3.0**
to match mautrix-discord.

discordgo's original license is preserved below for attribution only - it is
**not** the current license of this code:

```
Copyright (c) 2015, Bruce Marriner
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

* Redistributions of source code must retain the above copyright notice, this
  list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright notice,
  this list of conditions and the following disclaimer in the documentation
  and/or other materials provided with the distribution.

* Neither the name of discordgo nor the names of its
  contributors may be used to endorse or promote products derived from
  this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

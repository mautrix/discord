module gitlab.com/beeper/discord

go 1.17

require (
	github.com/alecthomas/kong v0.2.18
	github.com/bwmarrin/discordgo v0.23.2
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.9.0
	github.com/lopezator/migrator v0.3.0
	github.com/mattn/go-sqlite3 v1.14.11
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	gopkg.in/yaml.v2 v2.4.0
	maunium.net/go/maulogger/v2 v2.3.2
	maunium.net/go/mautrix v0.10.11
)

require (
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	golang.org/x/crypto v0.0.0-20220214200702-86341886e292 // indirect
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/sys v0.0.0-20220209214540-3681064d5158 // indirect
)

replace github.com/bwmarrin/discordgo v0.23.2 => gitlab.com/beeper/discordgo v0.23.3-0.20220219094025-13ff4cc63da7

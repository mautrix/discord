module gitlab.com/beeper/discord

go 1.17

require (
	github.com/alecthomas/kong v0.2.18
	github.com/bwmarrin/discordgo v0.23.2
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/websocket v1.4.2
	github.com/lib/pq v1.9.0
	github.com/lopezator/migrator v0.3.0
	github.com/mattn/go-sqlite3 v1.14.9
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	gopkg.in/yaml.v2 v2.4.0
	maunium.net/go/maulogger/v2 v2.3.1
	maunium.net/go/mautrix v0.10.8
)

require (
	github.com/btcsuite/btcutil v1.0.2 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	golang.org/x/crypto v0.0.0-20211215153901-e495a2d5b3d3 // indirect
	golang.org/x/net v0.0.0-20211216030914-fe4d6282115f // indirect
	golang.org/x/sys v0.0.0-20210615035016-665e8c7367d1 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/grimmy/discordgo v0.23.3-0.20220126043435-7470d1aacd64

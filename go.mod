module go.mau.fi/mautrix-discord

go 1.17

require (
	github.com/bwmarrin/discordgo v0.25.0
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.6
	github.com/mattn/go-sqlite3 v1.14.13
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	maunium.net/go/maulogger/v2 v2.3.2
	maunium.net/go/mautrix v0.11.1-0.20220528212725-21d71dc1a1d5
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/tidwall/gjson v1.14.1 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.4 // indirect
	github.com/yuin/goldmark v1.4.12 // indirect
	golang.org/x/crypto v0.0.0-20220513210258-46612604a0f9 // indirect
	golang.org/x/net v0.0.0-20220513224357-95641704303c // indirect
	golang.org/x/sys v0.0.0-20220422013727-9388b58f7150 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => gitlab.com/beeper/discordgo v0.23.3-0.20220528212118-5e6370d356e6

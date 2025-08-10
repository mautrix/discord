module go.mau.fi/mautrix-discord

go 1.23.0

toolchain go1.24.5

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/gabriel-vasile/mimetype v1.4.9
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.28
	github.com/rs/zerolog v1.34.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stretchr/testify v1.10.0
	github.com/yuin/goldmark v1.7.12
	go.mau.fi/util v0.2.2-0.20231228160422-22fdd4bbddeb
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc
	golang.org/x/sync v0.16.0
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.16.3-0.20250810202616-6bc5698125c2
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	golang.org/x/crypto v0.40.0 // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20250607214857-f23a8518ece2

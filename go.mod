module go.mau.fi/mautrix-discord

go 1.20

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/gabriel-vasile/mimetype v1.4.3
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.19
	github.com/rs/zerolog v1.31.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stretchr/testify v1.8.4
	github.com/yuin/goldmark v1.6.0
	go.mau.fi/util v0.2.1
	golang.org/x/exp v0.0.0-20231219180239-dc181d75b848
	golang.org/x/sync v0.5.0
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.16.3-0.20240218195727-4ceb1123b660
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/tidwall/gjson v1.17.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	golang.org/x/crypto v0.15.0 // indirect
	golang.org/x/net v0.18.0 // indirect
	golang.org/x/sys v0.14.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20231013182643-f333f2578a3c

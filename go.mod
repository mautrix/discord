module go.mau.fi/mautrix-discord

go 1.26.0

toolchain go1.27.3

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/gabriel-vasile/mimetype v1.4.16
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/imroc/req/v3 v3.60.0
	github.com/lib/pq v1.12.3
	github.com/mattn/go-sqlite3 v1.14.53
	github.com/refraction-networking/utls v1.8.2
	github.com/rs/zerolog v1.35.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stretchr/testify v1.12.1
	github.com/yuin/goldmark v1.8.7
	go.mau.fi/util v0.2.2-0.20231228160422-22fdd4bbddeb
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba
	golang.org/x/sync v0.24.0
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.16.3-0.20250810202616-6bc5698125c2
)

require (
	github.com/andybalholm/brotli v1.2.4 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/icholy/digest v1.2.0 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/kr/text v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.61.0 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20261104162414-49256f3e1ca7

replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260808092221-1540c0bf3d1a

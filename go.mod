module go.mau.fi/mautrix-discord

go 1.25.0

toolchain go1.26.5

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/coder/websocket v1.8.15
	github.com/google/uuid v1.6.0
	github.com/imroc/req/v3 v3.57.0
	github.com/refraction-networking/utls v1.8.1
	github.com/rs/zerolog v1.35.1
	github.com/yuin/goldmark v1.8.4
	go.mau.fi/util v0.9.12-0.20260719092501-f9c03d846391
	golang.org/x/net v0.57.0
	golang.org/x/term v0.45.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.29.1-0.20260719130752-5743d9b6f27e
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/icholy/digest v1.1.0 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lib/pq v1.12.3 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.21 // indirect
	github.com/mattn/go-sqlite3 v1.14.48 // indirect
	github.com/petermattis/goid v0.0.0-20260713124913-97594f28f5ca // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.57.1 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.2.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20260709172345-9ea1abe57597 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20260803125648-0ee5f692e9eb

replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260703124114-47a4e2aa147e

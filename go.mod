module go.mau.fi/mautrix-discord

go 1.25.0

toolchain go1.26.2

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/coder/websocket v1.8.14
	github.com/google/uuid v1.6.0
	github.com/imroc/req/v3 v3.57.0
	github.com/refraction-networking/utls v1.8.1
	github.com/rs/zerolog v1.35.1
	github.com/yuin/goldmark v1.8.2
	go.mau.fi/util v0.9.9
	golang.org/x/net v0.55.0
	golang.org/x/term v0.43.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.28.1-0.20260519145316-34b5f49408a3
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
	github.com/mattn/go-sqlite3 v1.14.44 // indirect
	github.com/petermattis/goid v0.0.0-20260330135022-df67b199bc81 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.57.1 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.mau.fi/zeroconfig v0.2.0 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/exp v0.0.0-20260508232706-74f9aab9d74a // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20260714152424-7d8ec59d414a

replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260114152409-4c060b237f73

#!/bin/sh
MAUTRIX_VERSION=$(cat go.mod | grep 'maunium.net/go/mautrix ' | awk '{ print $2 }')
GO_LDFLAGS="-X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=`date -Iseconds`' -X 'maunium.net/go/mautrix.GoModVersion=$MAUTRIX_VERSION'"
go build -ldflags="-s -w $GO_LDFLAGS" ./cmd/mautrix-discord "$@"

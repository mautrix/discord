# Discord Remote Authentication

This library implements the desktop side of Discord's remote authentication
protocol.

It is completely based off of the
[Unofficial Discord API Documentation](https://luna.gitlab.io/discord-unofficial-docs/desktop_remote_auth.html).

## Example

```go
package main

import (
	"context"
	"fmt"

	"github.com/skip2/go-qrcode"
)

func main() {
	client, err := New()
	if err != nil {
		fmt.Printf("error: %v\n", err)

		return
	}

	ctx := context.Background()

	qrChan := make(chan *qrcode.QRCode)
	go func() {
		qrCode := <-qrChan
		fmt.Println(qrCode.ToSmallString(true))
	}()

	doneChan := make(chan struct{})

	if err := client.Dial(ctx, qrChan, doneChan); err != nil {
		close(qrChan)
		close(doneChan)

		fmt.Printf("dial error: %v\n", err)

		return
	}

	<-doneChan

	user, err := client.Result()
	fmt.Printf("user: %q\n", user)
	fmt.Printf("err: %v\n", err)
}
```

package remoteauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type Client struct {
	sync.Mutex

	URL    string
	Origin string

	conn *websocket.Conn

	qrChan   chan string
	doneChan chan struct{}

	user User
	err  error

	heartbeats int
	closed     bool

	privateKey *rsa.PrivateKey
}

// New creates a new Discord remote auth client. qrChan is a channel that will
// receive the qrcode once it is available.
func New() (*Client, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	return &Client{
		URL:        "wss://remote-auth-gateway.discord.gg/?v=1",
		Origin:     "https://discord.com",
		privateKey: privateKey,
	}, nil
}

// Dial will start the QRCode login process. ctx may be used to abandon the
// process.
func (c *Client) Dial(ctx context.Context, qrChan chan string, doneChan chan struct{}) error {
	c.Lock()
	defer c.Unlock()

	header := http.Header{
		"Origin": []string{c.Origin},
	}

	c.qrChan = qrChan
	c.doneChan = doneChan

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.URL, header)
	if err != nil {
		return err
	}

	c.conn = conn

	go c.processMessages()

	return nil
}

func (c *Client) Result() (User, error) {
	c.Lock()
	defer c.Unlock()

	return c.user, c.err
}

func (c *Client) close() error {
	c.Lock()
	defer c.Unlock()

	if c.closed {
		return nil
	}

	c.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)

	c.closed = true

	defer close(c.doneChan)

	return c.conn.Close()
}

func (c *Client) write(p clientPacket) error {
	c.Lock()
	defer c.Unlock()

	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}

	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Client) decrypt(payload string) ([]byte, error) {
	// Decode the base64 string.
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return []byte{}, err
	}

	// Decrypt the data.
	return rsa.DecryptOAEP(sha256.New(), nil, c.privateKey, raw, nil)
}

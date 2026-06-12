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
	"time"

	"github.com/coder/websocket"

	"github.com/bwmarrin/discordgo"
)

const handshakeTimeout = 45 * time.Second

type Client struct {
	sync.Mutex

	URL string

	conn *websocket.Conn
	// connCtx scopes reads/writes on the remote-auth websocket; cancelled on
	// close. coder's Read/Write are context-based.
	connCtx    context.Context
	connCancel context.CancelFunc

	// wsHTTPClient should be used to perform the websocket handshake (it
	// forces HTTP/1.1).
	wsHTTPClient *http.Client

	// restHTTPClient should be used to perform the single RemoteAuthLogin REST
	// call once a ticket arrives (it can advertise HTTP/2).
	restHTTPClient *http.Client

	qrChan   chan string
	doneChan chan struct{}

	user User
	err  error

	heartbeats int
	closed     bool

	privateKey *rsa.PrivateKey
}

// New creates a new Discord remote auth client from the respective HTTP
// clients, which will be used as part of WebSocket and REST communications.
// Specify nil to use the unproxied defaults.
func New(wsHTTPClient, restHTTPClient *http.Client) (*Client, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	if wsHTTPClient == nil {
		wsHTTPClient = http.DefaultClient
	}
	if restHTTPClient == nil {
		restHTTPClient = http.DefaultClient
	}

	return &Client{
		URL:            "wss://remote-auth-gateway.discord.gg/?v=2",
		wsHTTPClient:   wsHTTPClient,
		restHTTPClient: restHTTPClient,
		privateKey:     privateKey,
	}, nil
}

// Dial will start the QRCode login process. ctx may be used to abandon the
// process.
func (c *Client) Dial(ctx context.Context, qrChan chan string, doneChan chan struct{}) error {
	c.Lock()
	defer c.Unlock()

	header := http.Header{}
	for key, value := range discordgo.DroidWSHeaders {
		header.Set(key, value)
	}

	c.qrChan = qrChan
	c.doneChan = doneChan

	dialCtx, cancelDial := context.WithTimeout(ctx, handshakeTimeout)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, c.URL, &websocket.DialOptions{
		HTTPClient: c.wsHTTPClient,
		HTTPHeader: header,
	})
	if err != nil {
		return err
	}
	// Remove the default 32 KiB read limit.
	conn.SetReadLimit(-1)

	c.conn = conn
	c.connCtx, c.connCancel = context.WithCancel(context.Background())

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

	c.closed = true

	defer close(c.doneChan)

	err := c.conn.Close(websocket.StatusNormalClosure, "")
	if c.connCancel != nil {
		c.connCancel()
	}
	return err
}

func (c *Client) write(p clientPacket) error {
	c.Lock()
	defer c.Unlock()

	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}

	return c.conn.Write(c.connCtx, websocket.MessageText, payload)
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

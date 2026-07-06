// Package native implements a minimal ESPHome native TCP API client
// (port 6053) using Noise NNpsk0_25519_ChaChaPoly_SHA256 encryption, the
// transport required by modern ESPHome firmware. It supports listing
// entities and subscribing to one round of state updates.
package native

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/flynn/noise"
)

// Native API message type IDs (see esphome/aioesphomeapi core.py).
const (
	msgHelloRequest        = 1
	msgHelloResponse       = 2
	msgDisconnectRequest   = 5
	msgPingRequest         = 7
	msgPingResponse        = 8
	msgListEntitiesRequest = 11
	msgListEntitiesDone    = 19
	msgSubscribeStates     = 20
)

// Entity describes a single ESPHome entity discovered via the native API.
type Entity struct {
	Key      uint32 `json:"key"`
	Platform string `json:"platform"`
	ObjectID string `json:"object_id,omitempty"`
	Name     string `json:"name"`
	UniqueID string `json:"unique_id,omitempty"`
}

// EntityState is the current value of an entity keyed by Entity.Key.
type EntityState struct {
	Key   uint32 `json:"key"`
	State string `json:"state"`
}

// Result is the combined entity + state listing returned by ListEntities.
type Result struct {
	Entities []Entity      `json:"entities"`
	States   []EntityState `json:"states"`
}

// Client connects to an ESPHome device's native API.
type Client struct {
	Host         string
	Port         int
	PSK          string // base64-encoded 32-byte pre-shared key
	ExpectedName string // optional server name to verify
}

// ListEntities performs the full handshake, lists entities, subscribes to
// state updates for stateWindow, and returns the combined result.
func (c *Client) ListEntities(ctx context.Context, stateWindow time.Duration) (*Result, error) {
	if c.PSK == "" {
		return nil, errors.New("native API requires an encryption PSK (api.encryption.key)")
	}
	pskBytes, err := base64.StdEncoding.DecodeString(c.PSK)
	if err != nil {
		return nil, fmt.Errorf("decode PSK: %w", err)
	}
	if len(pskBytes) != 32 {
		return nil, fmt.Errorf("invalid PSK length %d, expected 32 bytes", len(pskBytes))
	}
	if c.Port == 0 {
		c.Port = 6053
	}
	if stateWindow <= 0 {
		stateWindow = 3 * time.Second
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port)))
	if err != nil {
		return nil, fmt.Errorf("dial %s:%d: %w", c.Host, c.Port, err)
	}
	defer conn.Close()

	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	// Set an overall deadline based on the context.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}

	csSend, csRecv, err := c.noiseHandshake(conn, pskBytes)
	if err != nil {
		return nil, err
	}

	t := &transport{conn: conn, send: csSend, recv: csRecv}

	// Application-level hello over the encrypted channel.
	hello := &protoBuilder{}
	hello.string(1, "esphome-mcp")
	hello.uint32(2, 1) // api_version_major
	hello.uint32(3, 14) // api_version_minor
	if err := t.sendMessage(msgHelloRequest, hello.buf); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Read messages, processing HelloResponse and ListEntities*Response until
	// ListEntitiesDone, then send SubscribeStatesRequest and collect states.
	res := &Result{Entities: []Entity{}, States: []EntityState{}}

	// 1) Wait for HelloResponse.
	if err := t.expectMessage(ctx, msgHelloResponse, nil); err != nil {
		return nil, fmt.Errorf("await hello: %w", err)
	}

	// 2) Request entity list.
	if err := t.sendMessage(msgListEntitiesRequest, nil); err != nil {
		return nil, fmt.Errorf("send list entities: %w", err)
	}

	// 3) Collect ListEntities*Response until ListEntitiesDone.
	done := false
	for !done {
		mtype, payload, err := t.recvMessage(ctx)
		if err != nil {
			return nil, fmt.Errorf("recv list entities: %w", err)
		}
		switch mtype {
		case msgListEntitiesDone:
			done = true
		default:
			if platform, ok := listEntitiesPlatform(mtype); ok {
				key, objectID, name, uniqueID := decodeEntityCommon(payload)
				res.Entities = append(res.Entities, Entity{
					Key:      key,
					Platform: platform,
					ObjectID: objectID,
					Name:     name,
					UniqueID: uniqueID,
				})
			}
		}
	}

	// 4) Subscribe to states and collect for stateWindow.
	if err := t.sendMessage(msgSubscribeStates, nil); err != nil {
		return nil, fmt.Errorf("send subscribe states: %w", err)
	}

	deadline := time.Now().Add(stateWindow)
	_ = conn.SetReadDeadline(deadline)
	for {
		mtype, payload, err := t.recvMessage(ctx)
		if err != nil {
			// A timeout after we've collected some states is the normal end
			// of the subscription window.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
		if st, ok := decodeState(mtype, payload); ok {
			res.States = append(res.States, st)
		}
	}

	// Best-effort disconnect.
	_ = t.sendMessage(msgDisconnectRequest, nil)

	return res, nil
}

// noiseHandshake performs the ESPHome noise handshake over conn and returns
// the send/recv CipherStates for the encrypted transport.
func (c *Client) noiseHandshake(conn net.Conn, psk []byte) (*noise.CipherState, *noise.CipherState, error) {
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Pattern:               noise.HandshakeNN,
		Initiator:             true,
		Prologue:              []byte("NoiseAPIInit\x00\x00"),
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("noise init: %w", err)
	}

	// First noise handshake message (initiator -> responder).
	handshakeMsg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise write: %w", err)
	}

	// ClientHello frame: NOISE_HELLO (0x01 0x00 0x00) + header(0x01, len+1) + 0x00 + msg.
	frameLen := len(handshakeMsg) + 1
	clientHello := []byte{0x01, 0x00, 0x00, 0x01, byte(frameLen >> 8), byte(frameLen), 0x00}
	clientHello = append(clientHello, handshakeMsg...)
	if _, err := conn.Write(clientHello); err != nil {
		return nil, nil, fmt.Errorf("send client hello: %w", err)
	}

	// Server hello frame (metadata: chosen proto + name + mac).
	serverHello, err := readNoiseFrame(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("read server hello: %w", err)
	}
	if len(serverHello) == 0 || serverHello[0] != 0x01 {
		return nil, nil, fmt.Errorf("unsupported noise protocol byte: %v", serverHello)
	}
	if c.ExpectedName != "" {
		if name := parseNoiseServerName(serverHello[1:]); name != "" && name != c.ExpectedName {
			return nil, nil, fmt.Errorf("server name mismatch: expected %q, got %q", c.ExpectedName, name)
		}
	}

	// Server handshake response frame: 0x00 + noise response.
	handshakeResp, err := readNoiseFrame(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("read handshake response: %w", err)
	}
	if len(handshakeResp) == 0 || handshakeResp[0] != 0x00 {
		// 0x01 prefix indicates a handshake MAC failure (bad PSK).
		if len(handshakeResp) > 0 && handshakeResp[0] == 0x01 {
			return nil, nil, fmt.Errorf("invalid encryption key (handshake MAC failure): %s", strings.TrimSpace(string(handshakeResp[1:])))
		}
		return nil, nil, fmt.Errorf("unexpected handshake response prefix: %v", handshakeResp)
	}
	_, csSend, csRecv, err := hs.ReadMessage(nil, handshakeResp[1:])
	if err != nil {
		return nil, nil, fmt.Errorf("noise read: %w", err)
	}
	return csSend, csRecv, nil
}

// parseNoiseServerName extracts the server name from a server hello payload
// (after the protocol byte). The format is: name + 0x00 + mac + 0x00.
func parseNoiseServerName(payload []byte) string {
	i := strings.IndexByte(string(payload), 0)
	if i < 0 {
		return ""
	}
	return string(payload[:i])
}

// readNoiseFrame reads a single noise-transport frame: a 3-byte header
// [0x01, high, low] followed by `frameLen` bytes.
func readNoiseFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if header[0] != 0x01 {
		return nil, fmt.Errorf("invalid frame preamble 0x%02x (connection may require encryption or be plaintext)", header[0])
	}
	frameLen := int(header[1])<<8 | int(header[2])
	frame := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// transport is the encrypted ESPHome native API transport.
type transport struct {
	conn net.Conn
	send *noise.CipherState
	recv *noise.CipherState
}

// sendMessage encrypts and writes a single API message.
func (t *transport) sendMessage(mtype int, data []byte) error {
	// Plaintext layout: [type_high, type_low, len_high, len_low][data].
	header := []byte{
		byte(mtype >> 8), byte(mtype),
		byte(len(data) >> 8), byte(len(data)),
	}
	plaintext := append(header, data...)
	ciphertext, err := t.send.Encrypt(nil, nil, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	frame := []byte{0x01, byte(len(ciphertext) >> 8), byte(len(ciphertext))}
	frame = append(frame, ciphertext...)
	_, err = t.conn.Write(frame)
	return err
}

// recvMessage reads and decrypts a single API message, returning its type
// and payload.
func (t *transport) recvMessage(ctx context.Context) (int, []byte, error) {
	frame, err := readNoiseFrame(t.conn)
	if err != nil {
		return 0, nil, err
	}
	plaintext, err := t.recv.Decrypt(nil, nil, frame)
	if err != nil {
		return 0, nil, fmt.Errorf("decrypt: %w", err)
	}
	if len(plaintext) < 4 {
		return 0, nil, fmt.Errorf("decrypted message too short: %d bytes", len(plaintext))
	}
	mtype := int(binary.BigEndian.Uint16(plaintext[0:2]))
	// plaintext[2:4] is the length field; we trust the actual buffer length.
	payload := plaintext[4:]
	return mtype, payload, nil
}

// expectMessage reads messages until one of the expected type arrives,
// discarding others. If expected is nil, any message is accepted.
func (t *transport) expectMessage(ctx context.Context, expected int, _ []byte) error {
	for {
		mtype, _, err := t.recvMessage(ctx)
		if err != nil {
			return err
		}
		if expected == 0 || mtype == expected {
			return nil
		}
	}
}

// listEntitiesPlatform maps a ListEntities*Response message type to its
// platform name.
func listEntitiesPlatform(mtype int) (string, bool) {
	switch mtype {
	case 12:
		return "binary_sensor", true
	case 13:
		return "cover", true
	case 14:
		return "fan", true
	case 15:
		return "light", true
	case 16:
		return "sensor", true
	case 17:
		return "switch", true
	case 18:
		return "text_sensor", true
	case 43:
		return "camera", true
	case 46:
		return "climate", true
	case 49:
		return "number", true
	case 52:
		return "select", true
	case 55:
		return "siren", true
	case 58:
		return "lock", true
	case 61:
		return "button", true
	case 63:
		return "media_player", true
	case 94:
		return "alarm_control_panel", true
	case 97:
		return "text", true
	case 100:
		return "date", true
	case 103:
		return "time", true
	case 107:
		return "event", true
	case 109:
		return "valve", true
	case 112:
		return "datetime", true
	case 116:
		return "update", true
	case 132:
		return "water_heater", true
	default:
		return "", false
	}
}

// decodeState maps a *StateResponse message to a stringified EntityState.
func decodeState(mtype int, payload []byte) (EntityState, bool) {
	switch mtype {
	case 21: // BinarySensorStateResponse
		k, s := decodeStateBool(payload)
		return EntityState{Key: k, State: boolStr(s)}, true
	case 25: // SensorStateResponse
		k, s, missing := decodeStateFloat(payload)
		if missing {
			return EntityState{Key: k, State: "unavailable"}, true
		}
		return EntityState{Key: k, State: formatFloat(s)}, true
	case 26: // SwitchStateResponse
		k, s := decodeStateBool(payload)
		return EntityState{Key: k, State: boolStr(s)}, true
	case 27: // TextSensorStateResponse
		k, s, missing := decodeStateString(payload)
		if missing {
			return EntityState{Key: k, State: "unavailable"}, true
		}
		return EntityState{Key: k, State: s}, true
	case 50: // NumberStateResponse
		k, s, missing := decodeStateFloat(payload)
		if missing {
			return EntityState{Key: k, State: "unavailable"}, true
		}
		return EntityState{Key: k, State: formatFloat(s)}, true
	case 53: // SelectStateResponse
		k, s, missing := decodeStateString(payload)
		if missing {
			return EntityState{Key: k, State: "unavailable"}, true
		}
		return EntityState{Key: k, State: s}, true
	case 59: // LockStateResponse
		k, s := decodeStateEnum(payload)
		return EntityState{Key: k, State: lockStateStr(s)}, true
	case 98: // TextStateResponse
		k, s, missing := decodeStateString(payload)
		if missing {
			return EntityState{Key: k, State: "unavailable"}, true
		}
		return EntityState{Key: k, State: s}, true
	default:
		// For other state types, report the key with a placeholder so callers
		// still see the entity received a state update.
		r := newProtoReader(payload)
		if f, _, ok := r.next(); ok && f == 1 {
			if v, ok := r.fixed32(); ok {
				return EntityState{Key: v, State: "<state>"}, true
			}
		}
		return EntityState{}, false
	}
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func lockStateStr(s int32) string {
	switch s {
	case 0:
		return "none"
	case 1:
		return "locked"
	case 2:
		return "unlocked"
	case 3:
		return "jammed"
	case 4:
		return "locking"
	case 5:
		return "unlocking"
	case 6:
		return "opening"
	case 7:
		return "open"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

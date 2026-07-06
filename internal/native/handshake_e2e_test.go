package native

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// TestNoiseHandshakeEndToEnd runs a full NNpsk0 handshake between a fake
// ESPHome responder and our client handshake code over a real TCP pipe,
// then confirms an encrypted message round-trips.
func TestNoiseHandshakeEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	psk := make([]byte, 32)
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		suite := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
		hs, err := noise.NewHandshakeState(noise.Config{
			CipherSuite:           suite,
			Pattern:               noise.HandshakeNN,
			Initiator:             false,
			Prologue:              []byte("NoiseAPIInit\x00\x00"),
			PresharedKey:          psk,
			PresharedKeyPlacement: 0,
		})
		if err != nil {
			done <- err
			return
		}
		// Read client hello frame: NOISE_HELLO(3 bytes) + header(3) + 0x00 + msg.
		hdr := make([]byte, 6)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			done <- err
			return
		}
		frameLen := int(hdr[4])<<8 | int(hdr[5])
		rest := make([]byte, frameLen) // frameLen already includes the 0x00 prefix
		if _, err := io.ReadFull(conn, rest); err != nil {
			done <- err
			return
		}
		clientMsg := rest[1:] // skip 0x00 prefix
		if _, _, _, err := hs.ReadMessage(nil, clientMsg); err != nil {
			done <- err
			return
		}
		// Send server hello frame (metadata): chosen proto 0x01 + name + 0x00.
		serverHello := []byte{0x01, 't', 'e', 's', 't', 0x00}
		shFrame := append([]byte{0x01, byte(len(serverHello) >> 8), byte(len(serverHello))}, serverHello...)
		if _, err := conn.Write(shFrame); err != nil {
			done <- err
			return
		}
		// Send handshake response: 0x00 + noise response msg. WriteMessage
		// finalizes the handshake and returns the split cipher states. For the
		// responder role, the first returned state is receive, the second is send.
		resp, csRecv, csSend, err := hs.WriteMessage(nil, nil)
		if err != nil {
			done <- err
			return
		}
		respFrame := append([]byte{0x00}, resp...)
		hrHdr := []byte{0x01, byte(len(respFrame) >> 8), byte(len(respFrame))}
		if _, err := conn.Write(append(hrHdr, respFrame...)); err != nil {
			done <- err
			return
		}
		// Read one encrypted frame from the client, echo plaintext back as type 99.
		ch := make([]byte, 3)
		if _, err := io.ReadFull(conn, ch); err != nil {
			done <- err
			return
		}
		flen := int(ch[1])<<8 | int(ch[2])
		ct := make([]byte, flen)
		if _, err := io.ReadFull(conn, ct); err != nil {
			done <- err
			return
		}
		pt, err := csRecv.Decrypt(nil, nil, ct)
		if err != nil {
			done <- err
			return
		}
		// pt is the full plaintext [type(2), len(2), data...]; echo just the data.
		echo := pt[4:]
		const mtype = 99
		header := []byte{byte(mtype >> 8), byte(mtype), byte(len(echo) >> 8), byte(len(echo))}
		plaintext := append(header, echo...)
		out, err := csSend.Encrypt(nil, nil, plaintext)
		if err != nil {
			done <- err
			return
		}
		resp2 := []byte{0x01, byte(len(out) >> 8), byte(len(out))}
		if _, err := conn.Write(append(resp2, out...)); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	c := &Client{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, PSK: base64.StdEncoding.EncodeToString(psk)}
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	csSend, csRecv, err := c.noiseHandshake(conn, psk)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}

	tr := &transport{conn: conn, send: csSend, recv: csRecv}
	if err := tr.sendMessage(42, []byte("hello")); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mtype, payload, err := tr.recvMessage(ctx)
	if err != nil {
		t.Fatalf("recvMessage: %v", err)
	}
	if mtype != 99 {
		t.Errorf("got mtype %d, want 99", mtype)
	}
	if !bytes.Equal(payload, []byte("hello")) {
		t.Errorf("got payload %q, want %q", payload, "hello")
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

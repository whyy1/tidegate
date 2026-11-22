package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tidegate/internal/controller"
)

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA

	wsMaxControlPayload = 125
	wsMaxMessageBytes   = 64 << 10
	wsPingInterval      = 25 * time.Second
	wsReadTimeout       = 75 * time.Second
	wsWriteTimeout      = 10 * time.Second
)

type wsClientMsg struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if err := s.guard.CheckWebSocketOrigin(r); err != nil {
		writeError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, newHTTPError(http.StatusMethodNotAllowed, "method_not_allowed", "WebSocket 仅支持 GET 握手"))
		return
	}
	if !headerContainsToken(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") ||
		strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		writeError(w, newHTTPError(http.StatusBadRequest, "bad_websocket", "无效的 WebSocket 握手请求头"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		writeError(w, newHTTPError(http.StatusBadRequest, "bad_websocket", "缺少 Sec-WebSocket-Key 请求头"))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, newHTTPError(http.StatusInternalServerError, "internal_error", "当前服务器不支持 WebSocket 升级"))
		return
	}

	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}

	sum := sha1.Sum([]byte(key + wsMagicGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])

	var hs strings.Builder
	hs.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	hs.WriteString("Upgrade: websocket\r\n")
	hs.WriteString("Connection: Upgrade\r\n")
	hs.WriteString("Sec-WebSocket-Accept: ")
	hs.WriteString(accept)
	hs.WriteString("\r\n\r\n")

	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if _, err := conn.Write([]byte(hs.String())); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	wc := &wsConn{
		conn: conn,
		br:   rw.Reader,
		sub:  s.ctrl.Subscribe(),
		quit: make(chan struct{}),
	}
	s.trackWS(wc)
	defer func() {
		s.untrackWS(wc)
		wc.close()
	}()

	// Push initial connected agents state immediately after handshake.
	if agents, err := s.ctrl.Agents(r.Context()); err == nil {
		if b, err := json.Marshal(controller.Event{Type: controller.EventAgents, Agents: agents}); err == nil {
			if err := wc.writeFrame(wsOpText, b); err != nil {
				return
			}
		}
	}

	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		wc.writeLoop()
	}()

	wc.readLoop()
	wc.close()
	writerWG.Wait()
}

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	sub  *controller.Subscriber

	writeMu   sync.Mutex
	closeOnce sync.Once
	quit      chan struct{}
}

func (c *wsConn) close() {
	c.closeOnce.Do(func() {
		close(c.quit)
		c.sub.Close()
		_ = c.conn.Close()
	})
}

func (c *wsConn) writeLoop() {
	pingTick := time.NewTicker(wsPingInterval)
	defer pingTick.Stop()
	for {
		select {
		case <-c.quit:
			return
		case ev, ok := <-c.sub.C():
			if !ok {
				_ = c.writeFrame(wsOpClose, nil)
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if err := c.writeFrame(wsOpText, b); err != nil {
				return
			}
		case <-pingTick.C:
			if err := c.writeFrame(wsOpPing, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsConn) readLoop() {
	var msgBuf []byte
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return
		}
		switch op {
		case wsOpClose:
			_ = c.writeFrame(wsOpClose, nil)
			return
		case wsOpPing:
			if len(payload) > wsMaxControlPayload {
				return
			}
			if err := c.writeFrame(wsOpPong, payload); err != nil {
				return
			}
		case wsOpPong:
			continue
		case wsOpText:
			msgBuf = append(msgBuf[:0], payload...)
			if len(msgBuf) > wsMaxMessageBytes {
				return
			}
			if fin {
				c.handleClientJSON(msgBuf)
				msgBuf = msgBuf[:0]
			}
		case wsOpContinuation:
			if len(msgBuf)+len(payload) > wsMaxMessageBytes {
				return
			}
			msgBuf = append(msgBuf, payload...)
			if fin {
				c.handleClientJSON(msgBuf)
				msgBuf = msgBuf[:0]
			}
		default:
			return
		}
	}
}

func (c *wsConn) handleClientJSON(b []byte) {
	var msg wsClientMsg
	if err := json.Unmarshal(b, &msg); err != nil {
		return
	}
	switch msg.Type {
	case "subscribe":
		c.sub.SubscribeRun(msg.RunID)
	case "unsubscribe":
		c.sub.UnsubscribeRun(msg.RunID)
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	if !masked {
		// RFC 6455 Section 5.1: client frames must be masked.
		return false, 0, nil, errors.New("unmasked client frame")
	}

	plen := uint64(hdr[1] & 0x7F)
	switch plen {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		plen = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		plen = binary.BigEndian.Uint64(ext[:])
	}

	if opcode >= 0x8 && (!fin || plen > wsMaxControlPayload) {
		return false, 0, nil, errors.New("invalid control frame")
	}
	if plen > wsMaxMessageBytes {
		return false, 0, nil, errors.New("frame too large")
	}

	var maskKey [4]byte
	if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, int(plen))
	if plen > 0 {
		if _, err = io.ReadFull(c.br, payload); err != nil {
			return false, 0, nil, err
		}
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var hdr [10]byte
	hdr[0] = 0x80 | (opcode & 0x0F)
	n := len(payload)
	hlen := 2
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hlen = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hlen = 10
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if _, err := c.conn.Write(hdr[:hlen]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func headerContainsToken(h http.Header, key, token string) bool {
	for _, val := range h.Values(key) {
		for _, part := range strings.Split(val, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

package ingest

// A minimal RTMP server, enough for an encoder (OBS, vMix, ffmpeg, hardware
// encoders) to publish one stream: handshake, chunk stream, the connect /
// createStream / publish commands, then audio and video messages.
//
// It exists because ffmpeg's own RTMP listener does not enforce the stream key
// (it logs "Unexpected stream" and accepts the stream anyway): here the
// application and the key are checked before a single media byte is accepted.
// The media is handed out as an FLV stream, which ffmpeg then remuxes to
// MPEG-TS without re-encoding.

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"time"
)

// RTMP limits. A publisher is given little time before it is authenticated,
// and no message can make the server hold more memory than maxRTMPMessage.
const (
	rtmpHandshakeSize = 1536
	rtmpSetupTimeout  = 10 * time.Second // handshake + connect + publish
	rtmpIdleTimeout   = 15 * time.Second // no media for this long ends the session
	maxRTMPMessage    = 8 << 20
	maxRTMPChunk      = 1 << 20
	rtmpOutChunk      = 4096
	rtmpWindow        = 2_500_000
)

// RTMP message types.
const (
	rtmpSetChunkSize  = 1
	rtmpAbort         = 2
	rtmpAck           = 3
	rtmpUserControl   = 4
	rtmpWindowAckSize = 5
	rtmpPeerBandwidth = 6
	rtmpAudio         = 8
	rtmpVideo         = 9
	rtmpDataAMF3      = 15
	rtmpCommandAMF3   = 17
	rtmpDataAMF0      = 18
	rtmpCommandAMF0   = 20
)

// ErrRTMPRefused means the publisher gave a wrong application or key.
var ErrRTMPRefused = errors.New("rtmp: publish refused (wrong application or stream key)")

// rtmpMessage is one reassembled RTMP message.
type rtmpMessage struct {
	typ      byte
	streamID uint32
	ts       uint32
	payload  []byte
}

type rtmpChunkState struct {
	ts, delta uint32
	length    uint32
	typ       byte
	streamID  uint32
	extended  bool
	msgTS     uint32
	buf       []byte
}

// rtmpConn reads and writes the chunk stream of one connection.
type rtmpConn struct {
	conn     net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	inChunk  uint32
	outChunk uint32
	chunks   map[uint32]*rtmpChunkState
	window   uint32
	received uint64
	lastAck  uint64
}

type countingReader struct {
	r io.Reader
	n *uint64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += uint64(n) //nolint:gosec // G115: n is never negative
	return n, err
}

func newRTMPConn(conn net.Conn) *rtmpConn {
	c := &rtmpConn{conn: conn, inChunk: 128, outChunk: 128, chunks: map[uint32]*rtmpChunkState{}}
	c.r = bufio.NewReaderSize(countingReader{r: conn, n: &c.received}, 64<<10)
	c.w = bufio.NewWriterSize(conn, 16<<10)
	return c
}

// handshake runs the plain (version 3) handshake as the server.
func (c *rtmpConn) handshake() error {
	c0c1 := make([]byte, 1+rtmpHandshakeSize)
	if _, err := io.ReadFull(c.r, c0c1); err != nil {
		return fmt.Errorf("rtmp handshake: %w", err)
	}
	if c0c1[0] != 3 {
		return fmt.Errorf("rtmp handshake: unsupported version %d", c0c1[0])
	}
	s1 := make([]byte, rtmpHandshakeSize)
	binary.BigEndian.PutUint32(s1[0:4], uint32(time.Now().Unix())) //nolint:gosec // G115: a handshake timestamp
	if _, err := rand.Read(s1[8:]); err != nil {
		return err
	}
	// S0, S1, then S2 = echo of C1.
	_ = c.w.WriteByte(3)
	_, _ = c.w.Write(s1)
	_, _ = c.w.Write(c0c1[1:])
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("rtmp handshake: %w", err)
	}
	c2 := make([]byte, rtmpHandshakeSize)
	if _, err := io.ReadFull(c.r, c2); err != nil {
		return fmt.Errorf("rtmp handshake: %w", err)
	}
	return nil
}

func be24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

// readMessage reads chunks until one message is complete.
func (c *rtmpConn) readMessage() (rtmpMessage, error) {
	for {
		b0, err := c.r.ReadByte()
		if err != nil {
			return rtmpMessage{}, err
		}
		format := b0 >> 6
		csid := uint32(b0 & 0x3f)
		switch csid {
		case 0:
			b, err := c.r.ReadByte()
			if err != nil {
				return rtmpMessage{}, err
			}
			csid = 64 + uint32(b)
		case 1:
			var b [2]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return rtmpMessage{}, err
			}
			csid = 64 + uint32(b[0]) + uint32(b[1])*256
		}
		st := c.chunks[csid]
		if st == nil {
			if format != 0 {
				return rtmpMessage{}, fmt.Errorf("rtmp: chunk stream %d starts without a full header", csid)
			}
			st = &rtmpChunkState{}
			c.chunks[csid] = st
		}
		var hdr [11]byte
		switch format {
		case 0:
			if _, err := io.ReadFull(c.r, hdr[:11]); err != nil {
				return rtmpMessage{}, err
			}
			st.ts = be24(hdr[0:3])
			st.length = be24(hdr[3:6])
			st.typ = hdr[6]
			st.streamID = binary.LittleEndian.Uint32(hdr[7:11])
			st.delta = 0
		case 1:
			if _, err := io.ReadFull(c.r, hdr[:7]); err != nil {
				return rtmpMessage{}, err
			}
			st.delta = be24(hdr[0:3])
			st.length = be24(hdr[3:6])
			st.typ = hdr[6]
		case 2:
			if _, err := io.ReadFull(c.r, hdr[:3]); err != nil {
				return rtmpMessage{}, err
			}
			st.delta = be24(hdr[0:3])
		}
		if format < 3 {
			// A 0xFFFFFF field means a 4-byte extended timestamp follows.
			field := st.delta
			if format == 0 {
				field = st.ts
			}
			st.extended = field == 0xFFFFFF
		}
		if st.extended {
			var ext [4]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return rtmpMessage{}, err
			}
			if format == 0 {
				st.ts = binary.BigEndian.Uint32(ext[:])
			} else if format < 3 {
				st.delta = binary.BigEndian.Uint32(ext[:])
			}
		}
		if format < 3 && len(st.buf) > 0 {
			st.buf = st.buf[:0] // a new header aborts an unfinished message
		}
		if len(st.buf) == 0 {
			// First chunk of a message: its timestamp is absolute (type 0) or
			// the previous one plus the delta.
			if format == 0 {
				st.msgTS = st.ts
			} else {
				st.msgTS += st.delta
			}
			if st.length > maxRTMPMessage {
				return rtmpMessage{}, fmt.Errorf("rtmp: message of %d bytes is too large", st.length)
			}
		}
		n := min(st.length-uint32(len(st.buf)), c.inChunk) //nolint:gosec // G115: the buffer never exceeds maxRTMPMessage
		if cap(st.buf) < int(st.length) {
			nb := make([]byte, len(st.buf), st.length)
			copy(nb, st.buf)
			st.buf = nb
		}
		start := len(st.buf)
		st.buf = st.buf[:start+int(n)]
		if _, err := io.ReadFull(c.r, st.buf[start:]); err != nil {
			return rtmpMessage{}, err
		}
		if uint32(len(st.buf)) == st.length { //nolint:gosec // G115: bounded above
			msg := rtmpMessage{typ: st.typ, streamID: st.streamID, ts: st.msgTS, payload: append([]byte(nil), st.buf...)}
			st.buf = st.buf[:0]
			if err := c.afterRead(); err != nil {
				return rtmpMessage{}, err
			}
			return msg, nil
		}
	}
}

// afterRead acknowledges the bytes received when the peer asked for it.
func (c *rtmpConn) afterRead() error {
	if c.window == 0 || c.received-c.lastAck < uint64(c.window) {
		return nil
	}
	c.lastAck = c.received
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(c.received)) //nolint:gosec // G115: the sequence number wraps by design
	return c.writeMessage(2, rtmpAck, 0, b[:])
}

// writeMessage writes one message as a type-0 chunk plus type-3 continuations.
func (c *rtmpConn) writeMessage(csid byte, typ byte, streamID uint32, payload []byte) error {
	var hdr [12]byte
	hdr[0] = csid & 0x3f
	l := len(payload)
	hdr[4], hdr[5], hdr[6] = byte(l>>16), byte(l>>8), byte(l) //nolint:gosec // G115: 24-bit length field; our messages are tiny
	hdr[7] = typ
	binary.LittleEndian.PutUint32(hdr[8:12], streamID)
	if _, err := c.w.Write(hdr[:]); err != nil {
		return err
	}
	for i := 0; i < l; i += int(c.outChunk) {
		if i > 0 {
			if err := c.w.WriteByte(0xC0 | (csid & 0x3f)); err != nil {
				return err
			}
		}
		end := min(i+int(c.outChunk), l)
		if _, err := c.w.Write(payload[i:end]); err != nil {
			return err
		}
	}
	return c.w.Flush()
}

func u32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// ---- AMF0 ----

// amfObject keeps the key order of an encoded object.
type amfObject []amfProp

type amfProp struct {
	key string
	val any
}

func amfEncode(vals ...any) []byte {
	var b []byte
	var enc func(v any)
	str := func(s string) {
		b = append(b, byte(len(s)>>8), byte(len(s))) //nolint:gosec // G115: 16-bit AMF0 length; only our own short strings are encoded
		b = append(b, s...)
	}
	enc = func(v any) {
		switch x := v.(type) {
		case nil:
			b = append(b, 0x05)
		case float64:
			b = append(b, 0x00)
			b = binary.BigEndian.AppendUint64(b, math.Float64bits(x))
		case int:
			enc(float64(x))
		case bool:
			b = append(b, 0x01)
			if x {
				b = append(b, 1)
			} else {
				b = append(b, 0)
			}
		case string:
			b = append(b, 0x02)
			str(x)
		case amfObject:
			b = append(b, 0x03)
			for _, p := range x {
				str(p.key)
				enc(p.val)
			}
			b = append(b, 0, 0, 0x09)
		}
	}
	for _, v := range vals {
		enc(v)
	}
	return b
}

// amfDecode reads the values of a command; objects become maps. It refuses
// deep nesting and stops at the first value it does not know.
func amfDecode(b []byte) []any {
	var out []any
	pos := 0
	var dec func(depth int) (any, bool)
	readStr := func(long bool) (string, bool) {
		n := 0
		if long {
			if pos+4 > len(b) {
				return "", false
			}
			n = int(binary.BigEndian.Uint32(b[pos:]))
			pos += 4
		} else {
			if pos+2 > len(b) {
				return "", false
			}
			n = int(binary.BigEndian.Uint16(b[pos:]))
			pos += 2
		}
		if n < 0 || pos+n > len(b) {
			return "", false
		}
		s := string(b[pos : pos+n])
		pos += n
		return s, true
	}
	readProps := func(depth int) (map[string]any, bool) {
		m := map[string]any{}
		for pos+3 <= len(b) {
			if b[pos] == 0 && b[pos+1] == 0 && b[pos+2] == 0x09 {
				pos += 3
				return m, true
			}
			k, ok := readStr(false)
			if !ok {
				return m, false
			}
			v, ok := dec(depth + 1)
			if !ok {
				return m, false
			}
			m[k] = v
		}
		return m, false
	}
	dec = func(depth int) (any, bool) {
		if pos >= len(b) || depth > 8 {
			return nil, false
		}
		t := b[pos]
		pos++
		switch t {
		case 0x00:
			if pos+8 > len(b) {
				return nil, false
			}
			f := math.Float64frombits(binary.BigEndian.Uint64(b[pos:]))
			pos += 8
			return f, true
		case 0x01:
			if pos >= len(b) {
				return nil, false
			}
			v := b[pos] != 0
			pos++
			return v, true
		case 0x02:
			return readStr(false)
		case 0x0C:
			return readStr(true)
		case 0x03:
			return readProps(depth)
		case 0x08:
			if pos+4 > len(b) {
				return nil, false
			}
			pos += 4 // the count is only a hint
			return readProps(depth)
		case 0x05, 0x06:
			return nil, true
		default:
			return nil, false
		}
	}
	for pos < len(b) {
		v, ok := dec(0)
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}

// ---- session ----

// rtmpPublish describes an accepted publisher.
type rtmpPublish struct {
	App, Key string
	Remote   string
}

// serveRTMP runs one publishing session on conn. allow decides whether the
// application and stream key are accepted; it is called before any media is
// taken. onMedia receives audio and video messages (FLV tag type, timestamp in
// milliseconds, payload) once publishing started; returning an error ends the
// session. It returns nil when the publisher stops cleanly.
func serveRTMP(conn net.Conn, allow func(app, key string) bool, onPublish func(rtmpPublish) error,
	onMedia func(typ byte, ts uint32, payload []byte) error) error {
	c := newRTMPConn(conn)
	_ = conn.SetDeadline(time.Now().Add(rtmpSetupTimeout))
	if err := c.handshake(); err != nil {
		return err
	}
	app := ""
	publishing := false
	for {
		msg, err := c.readMessage()
		if err != nil {
			if publishing && errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if publishing {
			_ = conn.SetReadDeadline(time.Now().Add(rtmpIdleTimeout))
		}
		switch msg.typ {
		case rtmpSetChunkSize:
			if len(msg.payload) < 4 {
				return errors.New("rtmp: short set chunk size")
			}
			size := binary.BigEndian.Uint32(msg.payload) & 0x7fffffff
			if size == 0 || size > maxRTMPChunk {
				return fmt.Errorf("rtmp: chunk size %d not accepted", size)
			}
			c.inChunk = size
		case rtmpWindowAckSize:
			if len(msg.payload) >= 4 {
				c.window = binary.BigEndian.Uint32(msg.payload)
			}
		case rtmpUserControl:
			// Ping request (6) -> ping response (7) with the same timestamp.
			if len(msg.payload) >= 6 && binary.BigEndian.Uint16(msg.payload) == 6 {
				resp := append([]byte{0, 7}, msg.payload[2:6]...)
				if err := c.writeMessage(2, rtmpUserControl, 0, resp); err != nil {
					return err
				}
			}
		case rtmpAck, rtmpAbort, rtmpPeerBandwidth:
		case rtmpCommandAMF0, rtmpCommandAMF3:
			p := msg.payload
			if msg.typ == rtmpCommandAMF3 && len(p) > 0 {
				p = p[1:] // AMF3 commands carry AMF0 after a format byte
			}
			args := amfDecode(p)
			if len(args) == 0 {
				continue
			}
			name, _ := args[0].(string)
			txn := 0.0
			if len(args) > 1 {
				txn, _ = args[1].(float64)
			}
			switch name {
			case "connect":
				if len(args) > 2 {
					if props, ok := args[2].(map[string]any); ok {
						app, _ = props["app"].(string)
					}
				}
				if err := c.sendConnectResult(txn); err != nil {
					return err
				}
			case "createStream":
				if err := c.writeMessage(3, rtmpCommandAMF0, 0, amfEncode("_result", txn, nil, 1.0)); err != nil {
					return err
				}
			case "publish":
				key := ""
				if len(args) > 3 {
					key, _ = args[3].(string)
				}
				if !allow(cleanRTMPName(app), cleanRTMPName(key)) {
					_ = c.sendStatus(msg.streamID, "error", "NetStream.Publish.BadName", "publish refused")
					return ErrRTMPRefused
				}
				if err := onPublish(rtmpPublish{App: cleanRTMPName(app), Remote: conn.RemoteAddr().String()}); err != nil {
					_ = c.sendStatus(msg.streamID, "error", "NetStream.Publish.Failed", "server error")
					return err
				}
				if err := c.sendStatus(msg.streamID, "status", "NetStream.Publish.Start", "publishing"); err != nil {
					return err
				}
				publishing = true
				_ = conn.SetDeadline(time.Time{})
				_ = conn.SetReadDeadline(time.Now().Add(rtmpIdleTimeout))
			case "deleteStream", "FCUnpublish", "closeStream":
				if publishing {
					return nil
				}
			}
		case rtmpAudio, rtmpVideo:
			if !publishing {
				return errors.New("rtmp: media before publish")
			}
			if err := onMedia(msg.typ, msg.ts, msg.payload); err != nil {
				return err
			}
		case rtmpDataAMF0, rtmpDataAMF3:
			// @setDataFrame/onMetaData: the FLV demuxer finds the codecs in the
			// sequence headers, so the metadata is not needed.
		}
	}
}

// cleanRTMPName drops a query string and surrounding slashes ("live/" or
// "key?token=..."), which encoders add in different ways.
func cleanRTMPName(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	return strings.Trim(s, "/")
}

func (c *rtmpConn) sendConnectResult(txn float64) error {
	if err := c.writeMessage(2, rtmpWindowAckSize, 0, u32(rtmpWindow)); err != nil {
		return err
	}
	if err := c.writeMessage(2, rtmpPeerBandwidth, 0, append(u32(rtmpWindow), 2)); err != nil {
		return err
	}
	if err := c.writeMessage(2, rtmpSetChunkSize, 0, u32(rtmpOutChunk)); err != nil {
		return err
	}
	c.outChunk = rtmpOutChunk
	props := amfObject{{"fmsVer", "FMS/3,0,1,123"}, {"capabilities", 31.0}}
	info := amfObject{{"level", "status"}, {"code", "NetConnection.Connect.Success"}, {"description", "Connection succeeded."}, {"objectEncoding", 0.0}}
	return c.writeMessage(3, rtmpCommandAMF0, 0, amfEncode("_result", txn, props, info))
}

func (c *rtmpConn) sendStatus(streamID uint32, level, code, desc string) error {
	info := amfObject{{"level", level}, {"code", code}, {"description", desc}}
	return c.writeMessage(5, rtmpCommandAMF0, streamID, amfEncode("onStatus", 0.0, nil, info))
}

// keyMatches compares a stream key in constant time.
func keyMatches(want, got string) bool {
	return len(want) > 0 && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// ---- FLV ----

// flvWriter turns RTMP media messages into an FLV stream.
type flvWriter struct {
	w      io.Writer
	header bool
}

func (f *flvWriter) writeTag(typ byte, ts uint32, data []byte) error {
	if !f.header {
		f.header = true
		// "FLV", version 1, audio+video, header size 9, previous tag size 0.
		if _, err := f.w.Write([]byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9, 0, 0, 0, 0}); err != nil {
			return err
		}
	}
	n := len(data)
	hdr := []byte{typ, byte(n >> 16), byte(n >> 8), byte(n), byte(ts >> 16), byte(ts >> 8), byte(ts), byte(ts >> 24), 0, 0, 0} //nolint:gosec // G115: FLV 24-bit size (n <= maxRTMPMessage) and split 32-bit timestamp
	if _, err := f.w.Write(hdr); err != nil {
		return err
	}
	if _, err := f.w.Write(data); err != nil {
		return err
	}
	_, err := f.w.Write(u32(uint32(11 + n))) //nolint:gosec // G115: n <= maxRTMPMessage
	return err
}

// Package tstest generates synthetic MPEG-TS for tests: valid PAT/PMT with
// CRC, continuity counters and PCR, but placeholder elementary payloads. It
// is enough to exercise ingest, health and failover without real media.
package tstest

import (
	"context"
	"net"
	"time"

	"nexostream/media-engine/ingest"
)

// Generator produces a single-program transport stream.
type Generator struct {
	Program    int
	PMTPID     uint16
	VideoPID   uint16
	AudioPID   uint16
	VideoType  byte // 0x1B H.264, 0x02 MPEG-2, 0x24 HEVC; 0 = no video
	AudioType  byte // 0x0F AAC, 0x03 MP2, 0x81 AC-3; 0 = no audio
	cc         map[uint16]byte
	pcr        uint64
	pktCounter int
}

// New returns a generator with H.264 video and AAC audio.
func New() *Generator {
	return &Generator{Program: 1, PMTPID: 0x1000, VideoPID: 0x100, AudioPID: 0x101, VideoType: 0x1B, AudioType: 0x0F}
}

func (g *Generator) nextCC(pid uint16) byte {
	if g.cc == nil {
		g.cc = map[uint16]byte{}
	}
	c := g.cc[pid]
	g.cc[pid] = (c + 1) & 0x0F
	return c
}

func (g *Generator) psiPacket(pid uint16, section []byte) []byte {
	p := make([]byte, ingest.PacketSize)
	for i := range p {
		p[i] = 0xFF
	}
	p[0] = 0x47
	p[1] = 0x40 | byte(pid>>8)&0x1F
	p[2] = byte(pid)
	p[3] = 0x10 | g.nextCC(pid)
	p[4] = 0 // pointer field
	copy(p[5:], section)
	return p
}

func withCRC(sec []byte) []byte {
	c := ingest.CRC32MPEG(sec)
	return append(sec, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

// PAT returns a PAT packet.
func (g *Generator) PAT() []byte {
	body := []byte{
		0x00, 0, 0, // table id, length placeholder
		0x00, 0x01, // transport stream id
		0xC1, 0x00, 0x00, // version 0, current, section 0/0
		byte(g.Program >> 8), byte(g.Program), 0xE0 | byte(g.PMTPID>>8), byte(g.PMTPID),
	}
	l := len(body) - 3 + 4
	body[1] = 0xB0 | byte(l>>8)
	body[2] = byte(l)
	return g.psiPacket(0, withCRC(body))
}

// PMT returns a PMT packet.
func (g *Generator) PMT() []byte {
	pcr := g.VideoPID
	if g.VideoType == 0 {
		pcr = g.AudioPID
	}
	body := []byte{
		0x02, 0, 0,
		byte(g.Program >> 8), byte(g.Program),
		0xC1, 0x00, 0x00,
		0xE0 | byte(pcr>>8), byte(pcr),
		0xF0, 0x00, // program info length 0
	}
	if g.VideoType != 0 {
		body = append(body, g.VideoType, 0xE0|byte(g.VideoPID>>8), byte(g.VideoPID), 0xF0, 0x00)
	}
	if g.AudioType != 0 {
		body = append(body, g.AudioType, 0xE0|byte(g.AudioPID>>8), byte(g.AudioPID), 0xF0, 0x00)
	}
	l := len(body) - 3 + 4
	body[1] = 0xB0 | byte(l>>8)
	body[2] = byte(l)
	return g.psiPacket(g.PMTPID, withCRC(body))
}

// esPacket returns a payload packet; withPCR adds an adaptation field.
func (g *Generator) esPacket(pid uint16, withPCR bool) []byte {
	p := make([]byte, ingest.PacketSize)
	p[0] = 0x47
	p[1] = byte(pid>>8) & 0x1F
	p[2] = byte(pid)
	if withPCR {
		p[3] = 0x30 | g.nextCC(pid)
		p[4] = 7
		p[5] = 0x10
		g.pcr += 2700 // 30 ms at 90 kHz
		b := g.pcr
		p[6], p[7], p[8], p[9] = byte(b>>25), byte(b>>17), byte(b>>9), byte(b>>1)
		p[10] = byte(b<<7) | 0x7E
		p[11] = 0
		for i := 12; i < len(p); i++ {
			p[i] = 0xAA
		}
	} else {
		p[3] = 0x10 | g.nextCC(pid)
		for i := 4; i < len(p); i++ {
			p[i] = 0xAA
		}
	}
	return p
}

// Next returns n packets: PSI every 40 packets, then video/audio interleaved.
func (g *Generator) Next(n int) []byte {
	out := make([]byte, 0, n*ingest.PacketSize)
	for i := 0; i < n; i++ {
		switch {
		case g.pktCounter%40 == 0:
			out = append(out, g.PAT()...)
		case g.pktCounter%40 == 1:
			out = append(out, g.PMT()...)
		case g.VideoType != 0 && (g.AudioType == 0 || g.pktCounter%5 != 0):
			out = append(out, g.esPacket(g.VideoPID, g.pktCounter%20 == 2)...)
		default:
			out = append(out, g.esPacket(g.AudioPID, g.VideoType == 0 && g.pktCounter%20 == 2)...)
		}
		g.pktCounter++
	}
	return out
}

// Send streams the generator to a UDP address at roughly bitrate bits/s,
// 7 packets per datagram, until ctx is canceled.
func Send(ctx context.Context, g *Generator, addr string, bitrate int) error {
	conn, err := net.Dial("udp4", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	const perDatagram = 7
	dgramBits := perDatagram * ingest.PacketSize * 8
	interval := time.Duration(float64(time.Second) * float64(dgramBits) / float64(bitrate))
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := conn.Write(g.Next(perDatagram)); err != nil {
				// The receiver may not be listening yet (ICMP refused): keep going.
				continue
			}
		}
	}
}

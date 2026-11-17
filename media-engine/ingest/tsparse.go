package ingest

import (
	"sort"
	"sync"
	"time"

	"nexostream/internal/model"
)

// PacketSize is the MPEG-TS packet size.
const PacketSize = 188

const (
	syncByte = 0x47
	pidPAT   = 0x0000
	pidNull  = 0x1FFF
)

// Snapshot is a consistent view of what an Analyzer has seen.
type Snapshot struct {
	Bytes            int64
	Packets          int64
	CCErrors         int64
	TEIErrors        int64
	SyncErrors       int64
	Program          int
	PMTVersion       int
	StreamsSignature string
	Streams          []model.Stream
	FirstPacketAt    time.Time
	LastPacketAt     time.Time
	LastVideoAt      time.Time
	LastAudioAt      time.Time
	LastPCRAdvanceAt time.Time
	PMTSeen          bool
}

// HasVideo reports whether the PMT declares a video stream.
func (s Snapshot) HasVideo() bool { return s.firstOf("video") != nil }

// HasAudio reports whether the PMT declares an audio stream.
func (s Snapshot) HasAudio() bool { return s.firstOf("audio") != nil }

// Video returns the first video stream, if any.
func (s Snapshot) Video() *model.Stream { return s.firstOf("video") }

// Audio returns the first audio stream, if any.
func (s Snapshot) Audio() *model.Stream { return s.firstOf("audio") }

func (s Snapshot) firstOf(kind string) *model.Stream {
	for i := range s.Streams {
		if s.Streams[i].Kind == kind {
			st := s.Streams[i]
			return &st
		}
	}
	return nil
}

// Analyzer inspects MPEG-TS packets. It is safe for concurrent use: one
// goroutine feeds packets while others read snapshots.
type Analyzer struct {
	mu       sync.Mutex
	program  int // preferred program number (0 = first in PAT)
	snap     Snapshot
	lastCC   map[uint16]uint8
	sections map[uint16][]byte
	pmtPID   int
	pcrPID   int
	lastPCR  uint64
	pidKind  map[uint16]string
	pmtCRC   uint32
}

// NewAnalyzer creates an analyzer. program selects a program in an MPTS;
// 0 means the first program listed in the PAT.
func NewAnalyzer(program int) *Analyzer {
	return &Analyzer{
		program:  program,
		lastCC:   map[uint16]uint8{},
		sections: map[uint16][]byte{},
		pmtPID:   -1,
		pcrPID:   -1,
		pidKind:  map[uint16]string{},
		snap:     Snapshot{PMTVersion: -1},
	}
}

// Snapshot returns a copy of the current state.
func (a *Analyzer) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.snap
	s.Streams = append([]model.Stream(nil), a.snap.Streams...)
	return s
}

// Feed processes a buffer of whole TS packets received at time now.
// Bytes that are not aligned on a sync byte are counted as sync errors.
func (a *Analyzer) Feed(buf []byte, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.snap.FirstPacketAt.IsZero() {
		a.snap.FirstPacketAt = now
	}
	a.snap.Bytes += int64(len(buf))
	for off := 0; off+PacketSize <= len(buf); off += PacketSize {
		p := buf[off : off+PacketSize]
		if p[0] != syncByte {
			a.snap.SyncErrors++
			continue
		}
		a.packet(p, now)
	}
}

func (a *Analyzer) packet(p []byte, now time.Time) {
	a.snap.Packets++
	a.snap.LastPacketAt = now
	if p[1]&0x80 != 0 {
		a.snap.TEIErrors++
		return
	}
	pusi := p[1]&0x40 != 0
	pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
	afc := (p[3] >> 4) & 0x3
	cc := p[3] & 0x0F
	if pid == pidNull {
		return
	}

	payload := 4
	discontinuity := false
	if afc == 2 || afc == 3 {
		afl := int(p[4])
		if afl > 0 && 5+afl <= PacketSize {
			flags := p[5]
			discontinuity = flags&0x80 != 0
			if flags&0x10 != 0 && afl >= 7 && int(pid) == a.pcrPID {
				base := uint64(p[6])<<25 | uint64(p[7])<<17 | uint64(p[8])<<9 | uint64(p[9])<<1 | uint64(p[10])>>7
				if base != a.lastPCR {
					a.lastPCR = base
					a.snap.LastPCRAdvanceAt = now
				}
			}
		}
		payload = 5 + afl
	}
	hasPayload := afc == 1 || afc == 3

	// Continuity counter: increments only on packets with payload; one
	// duplicate packet is allowed (ISO/IEC 13818-1 2.4.3.3).
	if hasPayload {
		if last, ok := a.lastCC[pid]; ok && !discontinuity {
			if cc != (last+1)&0x0F && cc != last {
				a.snap.CCErrors++
			}
		}
		a.lastCC[pid] = cc
	}

	if kind, ok := a.pidKind[pid]; ok && hasPayload {
		switch kind {
		case "video":
			a.snap.LastVideoAt = now
		case "audio":
			a.snap.LastAudioAt = now
		}
	}

	if !hasPayload || payload >= PacketSize {
		return
	}
	if pid == pidPAT || int(pid) == a.pmtPID {
		a.section(pid, pusi, p[payload:])
	}
}

// section reassembles PSI sections that may span several packets.
func (a *Analyzer) section(pid uint16, pusi bool, data []byte) {
	if pusi {
		ptr := int(data[0])
		if 1+ptr >= len(data) {
			return
		}
		a.sections[pid] = append([]byte(nil), data[1+ptr:]...)
	} else if buf, ok := a.sections[pid]; ok {
		a.sections[pid] = append(buf, data...)
	} else {
		return
	}
	buf := a.sections[pid]
	if len(buf) < 3 {
		return
	}
	total := 3 + (int(buf[1]&0x0F)<<8 | int(buf[2]))
	if len(buf) < total {
		return
	}
	sec := buf[:total]
	delete(a.sections, pid)
	if crc32MPEG(sec) != 0 {
		return // corrupted section
	}
	switch {
	case pid == pidPAT && sec[0] == 0x00:
		a.parsePAT(sec)
	case int(pid) == a.pmtPID && sec[0] == 0x02:
		a.parsePMT(sec)
	}
}

func (a *Analyzer) parsePAT(sec []byte) {
	end := len(sec) - 4
	for i := 8; i+4 <= end; i += 4 {
		prog := int(sec[i])<<8 | int(sec[i+1])
		pid := int(sec[i+2]&0x1F)<<8 | int(sec[i+3])
		if prog == 0 {
			continue // network PID
		}
		if a.program == 0 || prog == a.program {
			if a.pmtPID != pid {
				a.pmtPID = pid
				a.snap.Program = prog
			}
			return
		}
	}
}

func (a *Analyzer) parsePMT(sec []byte) {
	crc := uint32(sec[len(sec)-4])<<24 | uint32(sec[len(sec)-3])<<16 | uint32(sec[len(sec)-2])<<8 | uint32(sec[len(sec)-1])
	if a.snap.PMTSeen && crc == a.pmtCRC {
		return // unchanged
	}
	a.pmtCRC = crc
	version := int(sec[5]>>1) & 0x1F
	a.pcrPID = int(sec[8]&0x1F)<<8 | int(sec[9])
	pil := int(sec[10]&0x0F)<<8 | int(sec[11])
	end := len(sec) - 4
	var streams []model.Stream
	kinds := map[uint16]string{}
	for i := 12 + pil; i+5 <= end; {
		st := int(sec[i])
		pid := uint16(sec[i+1]&0x1F)<<8 | uint16(sec[i+2])
		esl := int(sec[i+3]&0x0F)<<8 | int(sec[i+4])
		descEnd := i + 5 + esl
		if descEnd > end {
			break
		}
		codec, kind, lang := classify(st, sec[i+5:descEnd])
		streams = append(streams, model.Stream{PID: int(pid), StreamType: st, Codec: codec, Kind: kind, Language: lang})
		kinds[pid] = kind
		i = descEnd
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].PID < streams[j].PID })
	a.pidKind = kinds
	a.snap.Streams = streams
	a.snap.PMTVersion = version
	a.snap.PMTSeen = true
	a.snap.StreamsSignature = signature(streams)
}

func signature(s []model.Stream) string {
	b := make([]byte, 0, len(s)*8)
	for _, st := range s {
		b = append(b, st.Codec...)
		b = append(b, '/')
		b = append(b, st.Kind...)
		b = append(b, ';')
	}
	return string(b)
}

// classify maps a PMT stream_type (and descriptors) to a codec name.
func classify(streamType int, desc []byte) (codec, kind, lang string) {
	tags := map[byte][]byte{}
	for i := 0; i+2 <= len(desc); {
		tag, l := desc[i], int(desc[i+1])
		if i+2+l > len(desc) {
			break
		}
		tags[tag] = desc[i+2 : i+2+l]
		i += 2 + l
	}
	if v, ok := tags[0x0A]; ok && len(v) >= 3 {
		lang = string(v[:3])
	}
	switch streamType {
	case 0x01:
		return "mpeg1video", "video", lang
	case 0x02:
		return "mpeg2video", "video", lang
	case 0x1B:
		return "h264", "video", lang
	case 0x24:
		return "hevc", "video", lang
	case 0x03, 0x04:
		return "mp2", "audio", lang
	case 0x0F:
		return "aac", "audio", lang
	case 0x11:
		return "aac_latm", "audio", lang
	case 0x81:
		return "ac3", "audio", lang
	case 0x87:
		return "eac3", "audio", lang
	case 0x86:
		return "scte35", "data", lang
	case 0x06:
		switch {
		case has(tags, 0x6A):
			return "ac3", "audio", lang
		case has(tags, 0x7A):
			return "eac3", "audio", lang
		case has(tags, 0x7B):
			return "dts", "audio", lang
		case has(tags, 0x59):
			return "dvb_subtitle", "data", lang
		case has(tags, 0x56):
			return "teletext", "data", lang
		}
		return "private", "data", lang
	}
	return "unknown", "data", lang
}

func has(m map[byte][]byte, k byte) bool { _, ok := m[k]; return ok }

var crcTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc32MPEG computes CRC-32/MPEG-2. A section including its CRC yields 0.
func crc32MPEG(b []byte) uint32 {
	c := uint32(0xFFFFFFFF)
	for _, v := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^v]
	}
	return c
}

// CRC32MPEG is exported for test generators.
func CRC32MPEG(b []byte) uint32 { return crc32MPEG(b) }

package model

import "time"

// ChannelStatus is the operational state reported by the worker running a
// channel (GET /channels/{id}/status).
type ChannelStatus struct {
	ChannelID    string            `json:"channel_id"`
	State        ChannelState      `json:"state"`
	Since        time.Time         `json:"since"`
	Reason       string            `json:"reason,omitempty"`
	WorkerID     string            `json:"worker_id,omitempty"`
	Revision     int64             `json:"revision"`
	ActiveSource *SourceRef        `json:"active_source,omitempty"`
	Processing   string            `json:"processing,omitempty"` // passthrough | transcode | audio_transcode
	Input        *InputStatus      `json:"input,omitempty"`
	Outputs      map[string]string `json:"outputs,omitempty"`
	// ThumbnailURL is a recent picture of the channel (JPEG, 320 px).
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	// Viewers is the number of open play sessions on the node.
	Viewers int `json:"viewers"`
	// Pushes is the state of every push destination (tab Output).
	Pushes []PushStatus `json:"pushes,omitempty"`
	// Sources is the state of every input (tab Input).
	Sources []SourceStatus `json:"sources,omitempty"`
	// Pipeline is the processing decision (tab Transcoder).
	Pipeline  *PipelineInfo `json:"pipeline,omitempty"`
	LastError string        `json:"last_error,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SourceRef identifies the active source.
type SourceRef struct {
	Priority int    `json:"priority"`
	URL      string `json:"url"`
}

// InputStatus describes what is being received.
type InputStatus struct {
	BitrateBps int64       `json:"bitrate_bps"`
	Video      *VideoInfo  `json:"video,omitempty"`
	Audio      []AudioInfo `json:"audio"`
	// Subtitles are DVB subtitle and teletext tracks of the input.
	Subtitles          []SubtitleInfo `json:"subtitles,omitempty"`
	CCErrorsLastMinute int64          `json:"cc_errors_last_minute"`
	LastPacketAt       *time.Time     `json:"last_packet_at,omitempty"`
}

// VideoInfo describes the input video stream.
type VideoInfo struct {
	Codec      string  `json:"codec"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	Interlaced bool    `json:"interlaced,omitempty"`
}

// PushStatus is the state of one push destination.
type PushStatus struct {
	URL       string    `json:"url"`   // stream key hidden
	State     string    `json:"state"` // connecting | running | retrying
	Since     time.Time `json:"since"`
	Restarts  int       `json:"restarts"`
	Bytes     int64     `json:"bytes"`
	LastError string    `json:"last_error,omitempty"`
}

// SourceStatus is the state of one input of a running channel.
type SourceStatus struct {
	Priority     int        `json:"priority"`
	URL          string     `json:"url"`
	Active       bool       `json:"active"`
	Listening    bool       `json:"listening"` // a receiver is open
	BitrateBps   int64      `json:"bitrate_bps"`
	LastPacketAt *time.Time `json:"last_packet_at,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// PipelineInfo describes what the pipeline does with the input.
type PipelineInfo struct {
	Engine      string `json:"engine"`
	Processing  string `json:"processing"`
	VideoCopy   bool   `json:"video_copy"`
	AudioCopy   bool   `json:"audio_copy"`
	AudioTracks int    `json:"audio_tracks"`
	// SubtitleTracks are DVB subtitles and teletext kept in the MPEG-TS outputs.
	SubtitleTracks int           `json:"subtitle_tracks,omitempty"`
	Video          *VideoProfile `json:"video,omitempty"` // when transcoding the video
	Audio          *AudioProfile `json:"audio,omitempty"` // when converting the audio
	Encoder        string        `json:"encoder,omitempty"`
	Notes          []string      `json:"notes,omitempty"`
	// Renditions are the ABR qualities, highest first.
	Renditions []RenditionInfo `json:"renditions,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
}

// RenditionInfo is one quality of an ABR channel.
type RenditionInfo struct {
	Profile    string `json:"profile"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	BitrateBps int    `json:"bitrate_bps"`
	Encoder    string `json:"encoder"`
}

// SubtitleInfo describes one subtitle or teletext track of the input.
type SubtitleInfo struct {
	Codec    string `json:"codec"` // dvb_subtitle | teletext
	Language string `json:"language,omitempty"`
}

// AudioInfo describes one input audio stream.
type AudioInfo struct {
	Codec      string `json:"codec"`
	Channels   int    `json:"channels,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	Language   string `json:"language,omitempty"`
}

// StoppedStatus is the status of a channel nobody is running.
func StoppedStatus(id string) ChannelStatus {
	return ChannelStatus{ChannelID: id, State: StateStopped}
}

// ProbeResult is the analysis of a source (POST /sources/probe).
type ProbeResult struct {
	URL        string      `json:"url"`
	Reachable  bool        `json:"reachable"`
	BitrateBps int64       `json:"bitrate_bps"`
	Program    int         `json:"program,omitempty"`
	Video      *VideoInfo  `json:"video,omitempty"`
	Audio      []AudioInfo `json:"audio"`
	Streams    []Stream    `json:"streams"`
	CCErrors   int64       `json:"cc_errors"`
	Seconds    float64     `json:"seconds"`
	Error      string      `json:"error,omitempty"`
}

// Stream is an elementary stream found in the PMT.
type Stream struct {
	PID        int    `json:"pid"`
	StreamType int    `json:"stream_type"`
	Codec      string `json:"codec"`
	Kind       string `json:"kind"` // video | audio | data
	Language   string `json:"language,omitempty"`
}

package agent

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"nexostream/internal/model"
	"nexostream/internal/version"
)

// metrics are the Prometheus series of spec section 14.1.
type metrics struct {
	reg            *prometheus.Registry
	up             *prometheus.GaugeVec
	state          *prometheus.GaugeVec
	inputBytes     *prometheus.CounterVec
	outputBytes    *prometheus.CounterVec
	droppedBytes   *prometheus.CounterVec
	ccErrors       *prometheus.CounterVec
	lastPacket     *prometheus.GaugeVec
	switches       *prometheus.CounterVec
	restarts       *prometheus.CounterVec
	segments       *prometheus.CounterVec
	viewers        *prometheus.GaugeVec
	bitrate        *prometheus.GaugeVec
	dvrUsed        *prometheus.GaugeVec
	dvrSegments    *prometheus.CounterVec
	dvrIntegrity   *prometheus.CounterVec
	dvrTotal       *prometheus.GaugeVec
	workerChannels *prometheus.GaugeVec

	// The viewer path of the node (HLS, DASH, DVR and MPEG-TS requests that
	// go through the agent). Labeled by protocol, never by channel, so the
	// cardinality stays fixed: these series exist to answer "is the Go
	// process the limit for viewers?" (ADR 0009) with measurements.
	playRequests *prometheus.CounterVec   // proto, class (2xx, 3xx, 4xx, 5xx)
	playSeconds  *prometheus.HistogramVec // proto: time to answer (not for long MPEG-TS connections)
	playAuth     *prometheus.HistogramVec // proto: time spent admitting the request (rules, backend)
	playBytes    *prometheus.CounterVec   // proto: bytes sent to viewers
	playInflight *prometheus.GaugeVec     // proto: requests being served now
}

// playBuckets covers a cached playlist (sub-millisecond) to a slow backend or
// a large segment on a busy disk (seconds).
var playBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func newMetrics(workerID string) *metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	ch := []string{"channel_id"}
	m := &metrics{
		reg: reg,
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_channel_up",
			Help: "1 when the channel is RUNNING, ON_BACKUP or RECOVERING."}, ch),
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_channel_state",
			Help: "1 for the current state of the channel."}, []string{"channel_id", "state"}),
		inputBytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_channel_input_bytes_total",
			Help: "Bytes received from the active source."}, ch),
		outputBytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_channel_output_bytes_total",
			Help: "Bytes delivered by the MPEG-TS over HTTP relay."}, ch),
		droppedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_channel_input_dropped_bytes_total",
			Help: "Input bytes dropped because the pipeline did not keep up."}, ch),
		ccErrors: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_channel_cc_errors_total",
			Help: "MPEG-TS continuity counter errors on the active source."}, ch),
		lastPacket: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_channel_last_packet_timestamp_seconds",
			Help: "Unix time of the last packet received from the active source."}, ch),
		switches: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_channel_source_switches_total",
			Help: "Source switches (automatic and manual)."}, ch),
		restarts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_pipeline_restarts_total",
			Help: "Pipeline restarts."}, ch),
		segments: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_hls_segments_written_total",
			Help: "HLS segments written."}, ch),
		viewers: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_viewers",
			Help: "Open play sessions (HLS, MPEG-TS over HTTP and DVR viewers) on this node."}, ch),
		bitrate: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_channel_input_bitrate_bps",
			Help: "Input bitrate of the active source over the last second."}, ch),
		dvrIntegrity: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_dvr_integrity_errors_total",
			Help: "Problems found by the periodic check of recorded hours (index, size, MPEG-TS sync)."}, ch),
		dvrSegments: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_dvr_segments_recorded_total",
			Help: "Segments recorded by the DVR."}, ch),
		dvrUsed: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_dvr_storage_used_bytes",
			Help: "Used space of the DVR volume."}, []string{"volume"}),
		dvrTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_dvr_storage_total_bytes",
			Help: "Total space of the DVR volume."}, []string{"volume"}),
		workerChannels: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_worker_channels",
			Help: "Channels running on the node."}, []string{"worker_id"}),
		playRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_play_requests_total",
			Help: "Viewer requests answered by the node, by protocol (hls, dvr, mpegts) and status class."}, []string{"proto", "class"}),
		playSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nexostream_play_request_duration_seconds",
			Help: "Time to answer a viewer request (playlists and segments; long MPEG-TS connections are not observed).", Buckets: playBuckets}, []string{"proto"}),
		playAuth: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nexostream_play_admission_duration_seconds",
			Help: "Time spent admitting a viewer request: access rules, session lookup and the authorization backend.", Buckets: playBuckets}, []string{"proto"}),
		playBytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nexostream_play_sent_bytes_total",
			Help: "Bytes sent to viewers by the node, by protocol."}, []string{"proto"}),
		playInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_play_inflight_requests",
			Help: "Viewer requests being served now, by protocol (MPEG-TS connections stay open, so they count while connected)."}, []string{"proto"}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nexostream_build_info",
		Help: "Build information."}, []string{"version", "commit", "worker_id"})
	build.WithLabelValues(version.Version, version.Commit, workerID).Set(1)
	reg.MustRegister(m.up, m.state, m.inputBytes, m.outputBytes, m.droppedBytes, m.ccErrors, m.lastPacket,
		m.switches, m.restarts, m.segments, m.viewers, m.bitrate, m.dvrUsed, m.dvrSegments, m.dvrIntegrity, m.dvrTotal, m.workerChannels,
		m.playRequests, m.playSeconds, m.playAuth, m.playBytes, m.playInflight, build)
	return m
}

func (m *metrics) setState(id string, s model.ChannelState) {
	for _, st := range model.AllStates {
		v := 0.0
		if st == s {
			v = 1
		}
		m.state.WithLabelValues(id, string(st)).Set(v)
	}
	up := 0.0
	if s.Up() {
		up = 1
	}
	m.up.WithLabelValues(id).Set(up)
}

// forget removes the series of a channel that left the node.
func (m *metrics) forget(id string) {
	for _, v := range []*prometheus.GaugeVec{m.up, m.lastPacket, m.viewers, m.bitrate} {
		v.DeleteLabelValues(id)
	}
	for _, st := range model.AllStates {
		m.state.DeleteLabelValues(id, string(st))
	}
	for _, v := range []*prometheus.CounterVec{m.inputBytes, m.outputBytes, m.droppedBytes, m.ccErrors, m.switches, m.restarts, m.segments, m.dvrSegments, m.dvrIntegrity} {
		v.DeleteLabelValues(id)
	}
}

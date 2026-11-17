// Types mirroring the API (docs/api.md, /api/v1/openapi.json).

export type ChannelState =
  | "STOPPED"
  | "STARTING"
  | "RUNNING"
  | "DEGRADED"
  | "SWITCHING"
  | "ON_BACKUP"
  | "RECOVERING"
  | "FAILED";

export const STATES: ChannelState[] = [
  "STOPPED",
  "STARTING",
  "RUNNING",
  "DEGRADED",
  "SWITCHING",
  "ON_BACKUP",
  "RECOVERING",
  "FAILED",
];

export type Mode = "auto" | "passthrough" | "transcode";

export interface Source {
  id?: string;
  priority: number;
  url: string;
  interface?: string;
}

export interface Outputs {
  hls: boolean;
  mpegts_http: boolean;
  mpegts_udp?: string;
  dash?: boolean;
  push?: string[];
}

export interface PushStatus {
  url: string;
  state: "connecting" | "running" | "retrying";
  since: string;
  restarts: number;
  bytes: number;
  last_error?: string;
}

export interface DVRConfig {
  enabled: boolean;
  retention_hours: number;
  path?: string;
  storage?: string;
  /** ABR channel: record every quality (default: only the highest). */
  all_qualities?: boolean;
}

/** Who may play a channel (ADR 0009), like Flussonic's auth backend. */
export interface AuthConfig {
  enabled: boolean;
  backend_url?: string;
  allow_ips?: string[];
  deny_ips?: string[];
  max_sessions?: number;
  cache_seconds?: number;
  allow_on_error?: boolean;
}

export interface EPGConfig {
  url?: string;
  xmltv_id?: string;
}

/** Logo burned into the video of a transcoded channel. */
export interface OverlayConfig {
  enabled: boolean;
  /** File name inside the overlay folder of the media nodes (never a path). */
  image?: string;
  position?: "top-left" | "top-right" | "bottom-left" | "bottom-right";
  margin?: number;
  width?: number;
  opacity?: number;
}

export interface Program {
  channel_id: string;
  start: string;
  stop: string;
  title: string;
  description?: string;
  category?: string;
}

export interface EPGStatus {
  fetched_at: string;
  programs: number;
  error?: string;
}

export interface PlaySession {
  id: string;
  channel_id: string;
  worker_id: string;
  proto: "hls" | "mpegts" | "dvr";
  ip: string;
  user_agent?: string;
  user_id?: string;
  token?: string;
  started_at: string;
  last_seen: string;
  closed_at?: string;
  bytes: number;
  requests: number;
  reason?: string;
}

export interface SourceStatus {
  priority: number;
  url: string;
  active: boolean;
  listening: boolean;
  bitrate_bps: number;
  last_packet_at?: string;
  error?: string;
}

export interface PipelineInfo {
  engine: string;
  processing: string;
  video_copy: boolean;
  audio_copy: boolean;
  audio_tracks: number;
  subtitle_tracks?: number;
  video?: Profile["video"];
  audio?: Profile["audio"];
  encoder?: string;
  notes?: string[];
  renditions?: { profile: string; width: number; height: number; bitrate_bps: number; encoder: string }[];
  started_at: string;
}

/** Protection of the HLS output and the DVR (ADR 0008). */
export interface DRMConfig {
  enabled: boolean;
  method?: "aes-128";
  key_rotation_minutes?: number;
}

export interface PlaybackToken {
  channel_id: string;
  token: string;
  expires_at: string;
  hls_url?: string;
  dvr_url?: string;
}

export interface DVRStorage {
  id: string;
  mode: "single" | "raid0" | "raid1";
  disks: string[];
  created_at?: string;
  updated_at?: string;
}

export interface DiskUsage {
  path: string;
  used_bytes: number;
  total_bytes: number;
  down: boolean;
}

export interface Recordings {
  channel_id: string;
  from: string;
  to: string;
  ranges: { from: string; to: string }[];
  segments: number;
  bytes: number;
}

export interface ConfigError {
  line: number;
  message: string;
}

export interface ConfigPlan {
  create: string[];
  update: string[];
  delete: string[];
  unchanged: number;
}

export interface ConfigResult {
  ok: boolean;
  errors: ConfigError[];
  settings: "" | "update" | "unchanged";
  servers: ConfigPlan;
  channels: ConfigPlan;
  storages: ConfigPlan;
  applied: boolean;
}

/** Settings page (GET/PUT /settings). */
export interface Settings {
  system_name: string;
  brand_tagline: string;
  brand_color: string;
  brand_logo?: string;
  brand_login_message: string;
  default_profile: string;
  allow_hevc_passthrough: boolean;
  udp_read_buffer_bytes: number;
  default_failover: Failover;
  hls_segment_seconds: number;
  hls_list_size: number;
  dvr_default_retention_hours: number;
  dvr_default_storage: string;
  scheduler_max_cpu_percent: number;
  cost_passthrough: number;
  cost_transcode_per_megapixel: number;
  cost_gpu_offload: number;
  worker_offline_seconds: number;
  drain_migrations_per_pass: number;
  access_token_minutes: number;
  refresh_token_hours: number;
  login_attempts_per_minute: number;
  updated_at: string;
  updated_by: string;
}

export interface SettingsView {
  settings: Settings;
  defaults: Settings;
  saved: boolean;
}

export interface ServerSettings {
  worker_id: string;
  ingest_interfaces: string[];
  dvr_dir: string;
  public_hls_base: string;
  public_ts_base: string;
  public_dvr_base: string;
  roles?: ("passthrough" | "transcode" | "dvr")[];
  updated_at: string;
  updated_by: string;
}

export interface ServerSettingsView {
  worker_id: string;
  known: boolean;
  state?: "ONLINE" | "DRAINING" | "OFFLINE";
  interfaces: string[];
  settings: ServerSettings;
}

export interface ChannelDefaults {
  system_name: string;
  default_profile: string;
  default_failover: Failover;
  dvr_default_retention_hours: number;
  dvr_default_storage: string;
}

export interface Failover {
  no_packets_seconds: number;
  confirm_window_seconds: number;
  low_bitrate_ratio: number;
  low_bitrate_seconds: number;
  missing_stream_seconds: number;
  return_to_primary: boolean;
  primary_stable_seconds: number;
}

export interface VideoInfo {
  codec: string;
  width?: number;
  height?: number;
  fps?: number;
  interlaced?: boolean;
}

export interface AudioInfo {
  codec: string;
  channels?: number;
  sample_rate?: number;
  language?: string;
}

export interface ChannelStatus {
  channel_id: string;
  pushes?: PushStatus[];
  state: ChannelState;
  since?: string;
  reason?: string;
  worker_id?: string;
  revision: number;
  active_source?: { priority: number; url: string };
  processing?: string;
  input?: {
    bitrate_bps: number;
    video?: VideoInfo;
    audio: AudioInfo[];
    subtitles?: { codec: string; language?: string }[];
    cc_errors_last_minute: number;
    last_packet_at?: string;
  };
  outputs?: Record<string, string>;
  thumbnail_url?: string;
  viewers: number;
  sources?: SourceStatus[];
  pipeline?: PipelineInfo;
  last_error?: string;
  updated_at?: string;
}

export interface Channel {
  id: string;
  name: string;
  group: string;
  enabled: boolean;
  mode: Mode;
  profiles: string[];
  sources: Source[];
  outputs: Outputs;
  dvr: DVRConfig;
  drm?: DRMConfig;
  auth?: AuthConfig;
  epg?: EPGConfig;
  overlay?: OverlayConfig;
  failover: Failover;
  revision: number;
  created_at: string;
  updated_at: string;
  status: ChannelStatus;
}

export interface Worker {
  id: string;
  state: "ONLINE" | "DRAINING" | "OFFLINE";
  draining: boolean;
  last_heartbeat_at: string;
  started_at: string;
  version: string;
  capabilities: { gpu?: string; encoders: string[] | null; interfaces: string[] | null; engine: string };
  resources: {
    cpu_percent: number;
    memory_used_bytes: number;
    memory_total_bytes: number;
    gpu_percent: number | null;
    network_in_bps: number;
    network_out_bps: number;
    dvr_used_bytes?: number;
    dvr_total_bytes?: number;
    dvr_disks?: DiskUsage[];
  };
  channels: number;
}

export interface Profile {
  id: string;
  encoder: "auto" | "cpu" | "nvenc" | "qsv";
  video: {
    codec: string;
    profile: string;
    width: number;
    height: number;
    bitrate_bps: number;
    max_bitrate_bps: number;
    gop_seconds: number;
    deinterlace: string;
    preset?: string;
  };
  audio: { codec: string; bitrate_bps: number; sample_rate: number; channels: number };
}

export interface NsEvent {
  id: string;
  type: string;
  at: string;
  channel_id?: string;
  worker_id?: string;
  from?: ChannelState;
  to?: ChannelState;
  reason?: string;
  details?: Record<string, unknown>;
}

export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  actor_type: string;
  action: string;
  target: string;
  source_ip: string;
  request_id: string;
  details?: Record<string, unknown>;
}

export interface User {
  id: string;
  username: string;
  role: "admin" | "operator" | "viewer";
  disabled: boolean;
  created_at: string;
  /** Set while the account is locked after repeated failed sign-ins. */
  locked_until?: string;
}

export interface ApiToken {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  created_by: string;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  last_used_at: string | null;
}

export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}

export interface Principal {
  actor: string;
  actor_type: string;
  user_id?: string;
  role: string;
  scopes: string[];
}

export interface Summary {
  channels: {
    total: number;
    enabled: number;
    by_state: Record<ChannelState, number>;
    by_processing: Record<string, number>;
    input_bitrate_bps: number;
    viewers_ts_http: number;
  };
  workers: {
    total: number;
    by_state: Record<string, number>;
    network_in_bps: number;
    network_out_bps: number;
    items: Worker[];
  };
  failing: {
    id: string;
    name: string;
    state: ChannelState;
    reason?: string;
    since?: string;
    worker_id?: string;
  }[];
  recent_events: NsEvent[];
  version: string;
  generated_at: string;
}

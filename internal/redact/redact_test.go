package redact

import (
	"strings"
	"testing"
)

func TestURL(t *testing.T) {
	for raw, want := range map[string]string{
		// SRT: the passphrase is hidden, the options that describe the link stay.
		"srt://0.0.0.0:9000?mode=listener&passphrase=correct-horse-battery-1": "srt://0.0.0.0:9000?mode=listener&passphrase=••••",
		"srt://cdn.example:9000?latency=200&mode=caller&streamid=secret-id":   "srt://cdn.example:9000?latency=200&mode=caller&streamid=••••",
		// RTMP stream keys.
		"rtmp://a.rtmp.youtube.com/live2/abcd-1234-efgh": "rtmp://a.rtmp.youtube.com/live2/••••",
		"rtmps://live.example.com:443/app/KEY":           "rtmps://live.example.com:443/app/••••",
		"rtmp://server/live":                             "rtmp://server/live", // no key in the path
		// Passwords in the address.
		"rtsp://admin:hunter2@192.168.0.20:554/stream1": "rtsp://192.168.0.20:554/stream1",
		"rtsp://token@camera/stream":                    "rtsp://camera/stream",
		// Tokens in the query string.
		"https://cdn.example/live/index.m3u8?token=abc123&k=1": "https://cdn.example/live/index.m3u8?k=••••&token=••••",
		"https://auth.example/check?api_key=xyz":               "https://auth.example/check?api_key=••••",
		// Nothing secret: unchanged.
		"udp://239.1.1.1:1234":                "udp://239.1.1.1:1234",
		"https://cdn.example/live/index.m3u8": "https://cdn.example/live/index.m3u8",
		"":                                    "",
	} {
		if got := URL(raw); got != want {
			t.Errorf("URL(%q)\n got  %q\n want %q", raw, got, want)
		}
	}
	if got := URL("%%%not a url"); got != "(url inválida)" {
		t.Errorf("invalid input: %q", got)
	}
	if got := URL("no-scheme-here"); got != "(url inválida)" {
		t.Errorf("a string without a scheme: %q", got)
	}
}

// Whatever the input, none of the secrets may survive.
func TestURLNeverLeaksTheSecret(t *testing.T) {
	for _, secret := range []string{"S3cr3tPassw0rd", "key-abcdef-123456", "tok_9f8e7d6c"} {
		for _, raw := range []string{
			"srt://:9000?mode=listener&passphrase=" + secret,
			"rtsp://user:" + secret + "@cam/stream",
			"rtmp://host/app/" + secret,
			"https://x.example/a?token=" + secret + "&mode=1",
			"https://x.example/a?Token=" + secret, // case does not matter
			"https://x.example/a?x=1&x=" + secret, // repeated option
		} {
			if got := URL(raw); strings.Contains(got, secret) {
				t.Errorf("URL(%q) = %q still contains the secret", raw, got)
			}
		}
	}
}

func TestURLs(t *testing.T) {
	if URLs(nil) != nil {
		t.Fatal("nil in, nil out")
	}
	got := URLs([]string{"rtmp://a/live/KEY", "srt://h:1?mode=caller"})
	if got[0] != "rtmp://a/live/••••" || got[1] != "srt://h:1?mode=caller" {
		t.Fatalf("%v", got)
	}
}

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		"Error opening output rtmp://a.rtmp.youtube.com/live2/abcd-1234: I/O error": "Error opening output rtmp://a.rtmp.youtube.com/live2/••••: I/O error",
		"Connection to srt://h:9000?mode=caller&passphrase=correct-horse-1 failed.": "Connection to srt://h:9000?mode=caller&passphrase=•••• failed.",
		"opening rtsp://admin:hunter2@10.0.0.5/stream, retrying":                    "opening rtsp://10.0.0.5/stream, retrying",
		"no address in this sentence":                                               "no address in this sentence",
		"(see https://docs.example/page).":                                          "(see https://docs.example/page).",
		"two: rtmp://a/live/K1 and rtmp://b/live/K2":                                "two: rtmp://a/live/•••• and rtmp://b/live/••••",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	if strings.Contains(Text("x rtmp://h/a/SECRETKEY y"), "SECRETKEY") {
		t.Error("the key survived")
	}
}

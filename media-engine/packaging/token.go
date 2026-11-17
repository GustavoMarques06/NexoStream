package packaging

import (
	"bufio"
	"bytes"
	"net/url"
	"strings"
)

// AddToken appends ?token= to every URI of a playlist (segments, keys) that
// does not carry one yet, so every request of a viewer reaches the server
// with the token given in the playlist URL, like Flussonic does.
func AddToken(playlist []byte, token string) []byte {
	if token == "" {
		return playlist
	}
	param := "token=" + url.QueryEscape(token)
	add := func(u string) string {
		if strings.Contains(u, "token=") {
			return u
		}
		if strings.Contains(u, "?") {
			return u + "&" + param
		}
		return u + "?" + param
	}
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(playlist))
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case !strings.HasPrefix(t, "#"):
			line = add(t)
		case strings.Contains(t, `URI="`):
			i := strings.Index(t, `URI="`) + len(`URI="`)
			if j := strings.Index(t[i:], `"`); j >= 0 {
				line = t[:i] + add(t[i:i+j]) + t[i+j:]
			}
		}
		b.WriteString(line + "\n")
	}
	return b.Bytes()
}

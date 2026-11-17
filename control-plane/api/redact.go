package api

import (
	"context"

	"nexostream/internal/model"
	"nexostream/internal/redact"
)

// canSeeSecrets reports whether the caller may read the secrets stored in a
// channel: source passwords and SRT passphrases, push stream keys, the tokens
// in the authorization and guide URLs. Only who can edit channels needs them
// (the editor shows them); a read-only user or token sees them masked.
func canSeeSecrets(ctx context.Context) bool {
	p := principalFrom(ctx)
	return p == nil || p.Has(model.ScopeChannelsWrite)
}

// redactedChannel returns a copy of the channel with its secrets masked. The
// original (and what is stored) is not changed.
func redactedChannel(c model.Channel) model.Channel {
	if len(c.Sources) > 0 {
		src := make([]model.Source, len(c.Sources))
		for i, s := range c.Sources {
			s.URL = redact.URL(s.URL)
			src[i] = s
		}
		c.Sources = src
	}
	c.Outputs.Push = redact.URLs(c.Outputs.Push)
	c.Auth.BackendURL = redact.URL(c.Auth.BackendURL)
	c.EPG.URL = redact.URL(c.EPG.URL)
	return c
}

// forCaller returns the channel as the caller may see it.
func forCaller(ctx context.Context, c model.Channel) model.Channel {
	if canSeeSecrets(ctx) {
		return c
	}
	return redactedChannel(c)
}

// Package statuscache keeps the latest operational status of each channel
// (Redis in production, memory in development). Status is reconstructible:
// workers republish it every few seconds, so entries expire with a TTL.
package statuscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"nexostream/internal/model"
)

// TTL after which a status is considered stale (3 missed updates).
const TTL = 20 * time.Second

// Cache stores channel statuses.
type Cache interface {
	Set(ctx context.Context, st model.ChannelStatus) error
	Get(ctx context.Context, id string) (model.ChannelStatus, bool, error)
	GetMany(ctx context.Context, ids []string) (map[string]model.ChannelStatus, error)
	Delete(ctx context.Context, id string) error
	Ping(ctx context.Context) error
}

// Memory is an in-process Cache.
type Memory struct {
	mu  sync.RWMutex
	m   map[string]entry
	now func() time.Time
}

type entry struct {
	st  model.ChannelStatus
	exp time.Time
}

// NewMemory returns an empty cache.
func NewMemory() *Memory { return &Memory{m: map[string]entry{}, now: time.Now} }

func (c *Memory) Set(_ context.Context, st model.ChannelStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[st.ChannelID] = entry{st, c.now().Add(TTL)}
	return nil
}

func (c *Memory) Get(_ context.Context, id string) (model.ChannelStatus, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[id]
	if !ok || c.now().After(e.exp) {
		return model.ChannelStatus{}, false, nil
	}
	return e.st, true, nil
}

func (c *Memory) GetMany(ctx context.Context, ids []string) (map[string]model.ChannelStatus, error) {
	out := map[string]model.ChannelStatus{}
	for _, id := range ids {
		if st, ok, _ := c.Get(ctx, id); ok {
			out[id] = st
		}
	}
	return out, nil
}

func (c *Memory) Delete(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
	return nil
}

func (c *Memory) Ping(context.Context) error { return nil }

// Redis is a Cache backed by Redis.
type Redis struct {
	c *redis.Client
}

// NewRedis connects using a redis:// or rediss:// URL.
func NewRedis(url, password string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis url: %w", err)
	}
	if password != "" {
		opt.Password = password
	}
	return &Redis{c: redis.NewClient(opt)}, nil
}

func key(id string) string { return "nexostream:status:" + id }

func (r *Redis) Set(ctx context.Context, st model.ChannelStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return r.c.Set(ctx, key(st.ChannelID), b, TTL).Err()
}

func (r *Redis) Get(ctx context.Context, id string) (model.ChannelStatus, bool, error) {
	b, err := r.c.Get(ctx, key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return model.ChannelStatus{}, false, nil
	}
	if err != nil {
		return model.ChannelStatus{}, false, err
	}
	var st model.ChannelStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

func (r *Redis) GetMany(ctx context.Context, ids []string) (map[string]model.ChannelStatus, error) {
	out := map[string]model.ChannelStatus{}
	if len(ids) == 0 {
		return out, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = key(id)
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		var st model.ChannelStatus
		if json.Unmarshal([]byte(s), &st) == nil {
			out[ids[i]] = st
		}
	}
	return out, nil
}

func (r *Redis) Delete(ctx context.Context, id string) error { return r.c.Del(ctx, key(id)).Err() }
func (r *Redis) Ping(ctx context.Context) error              { return r.c.Ping(ctx).Err() }

// Close releases the connection.
func (r *Redis) Close() error { return r.c.Close() }

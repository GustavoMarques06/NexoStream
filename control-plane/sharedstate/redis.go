package sharedstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is a State shared by every API instance.
type Redis struct {
	c   *redis.Client
	now func() time.Time
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
	// Fail fast: a request must not wait seconds on a Redis that is down.
	opt.MaxRetries = -1
	opt.DialTimeout, opt.ReadTimeout, opt.WriteTimeout = 500*time.Millisecond, 500*time.Millisecond, 500*time.Millisecond
	return &Redis{c: redis.NewClient(opt), now: time.Now}, nil
}

// Close releases the connection.
func (r *Redis) Close() error { return r.c.Close() }

// Allow counts the hit in a one-minute window. Windows are fixed (a client can
// use the limit at the end of one minute and again at the start of the next),
// which is accurate enough for a brute-force brake and costs one round trip.
func (r *Redis) Allow(ctx context.Context, bucket, key string, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	window := r.now().Unix() / 60
	k := "nexostream:rl:" + bucket + ":" + key + ":" + strconv.FormatInt(window, 10)
	pipe := r.c.Pipeline()
	incr := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return incr.Val() <= int64(limit), nil
}

func replyKey(key string) string {
	h := sha256.Sum256([]byte(key)) // bounded length, no user text in key names
	return "nexostream:idem:" + hex.EncodeToString(h[:])
}

func (r *Redis) GetReply(ctx context.Context, key string) (Reply, bool, error) {
	b, err := r.c.Get(ctx, replyKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Reply{}, false, nil
	}
	if err != nil {
		return Reply{}, false, err
	}
	var rep Reply
	if err := json.Unmarshal(b, &rep); err != nil {
		return Reply{}, false, nil // unreadable entry: behave as if absent
	}
	return rep, true, nil
}

func (r *Redis) PutReply(ctx context.Context, key string, rep Reply, ttl time.Duration) error {
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return r.c.Set(ctx, replyKey(key), b, ttl).Err()
}

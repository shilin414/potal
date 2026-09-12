// Package redisx wraps the shared Redis client.
//
// One logical DB; every key carries the instance key prefix
// (xiaoan3:) so the design stays Redis-Cluster friendly.
package redisx

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/creation-agent-studio/backend-go/internal/platform/config"
)

// Client is the prefixed Redis client used everywhere.
type Client struct {
	*goredis.Client
	prefix string
}

func Open(ctx context.Context, cfg config.RedisConfig) (*Client, error) {
	opts := &goredis.Options{
		Addr:         cfg.Addr(),
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
	c := goredis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping redis %s: %w", cfg.Addr(), err)
	}
	return &Client{Client: c, prefix: cfg.KeyPrefix}, nil
}

// NewWithPrefix builds a client wrapper without a connection (tests,
// key-naming assertions only). All other methods would panic on nil.
func NewWithPrefix(prefix string) *Client { return &Client{prefix: prefix} }

// Key builds a namespaced key: prefix:part1:part2.
func (c *Client) Key(parts ...string) string {
	out := c.prefix
	for _, p := range parts {
		out += ":" + p
	}
	return out
}

// PubSub channel names (must match the reference implementation so mixed
// fleets during cutover stay observable):
//
//	xiaoan3:run:{id}:events
func (c *Client) RunEventsChannel(runID string) string {
	return c.Key("run", runID, "events")
}

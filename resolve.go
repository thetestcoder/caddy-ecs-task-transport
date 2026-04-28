package previewrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

type rawResponse struct {
	PreviewID   string `json:"preview_id"`
	TaskIP      string `json:"task_ip"`
	PreviewPort int    `json:"preview_port"`
	Status      string `json:"status"`
}

const lookupQuery = `SELECT raw_response FROM vite_studio_domain_mappings WHERE hostname = $1 AND status = 'live' LIMIT 1`

// resolveUpstream returns the upstream target for a hostname, using the
// in-memory cache and falling back to the database with singleflight
// deduplication.
func (h *PreviewRouter) resolveUpstream(ctx context.Context, hostname string) (upstreamTarget, error) {
	if target, ok := h.cache.Get(hostname); ok {
		h.metrics.cacheHits.Add(1)
		h.logger.Debug("cache hit",
			zap.String("hostname", hostname),
			zap.String("task_ip", target.IP),
			zap.Int("port", target.Port),
		)
		return target, nil
	}
	h.metrics.cacheMisses.Add(1)
	h.logger.Debug("cache miss, querying database", zap.String("hostname", hostname))

	v, err, shared := h.sfGroup.Do(hostname, func() (interface{}, error) {
		return h.queryDB(ctx, hostname)
	})
	if err != nil {
		return upstreamTarget{}, err
	}

	target := v.(upstreamTarget)
	h.cache.Set(hostname, target)

	h.logger.Info("mapping resolved",
		zap.String("hostname", hostname),
		zap.String("task_ip", target.IP),
		zap.Int("port", target.Port),
		zap.Bool("singleflight_shared", shared),
	)
	return target, nil
}

func (h *PreviewRouter) queryDB(ctx context.Context, hostname string) (upstreamTarget, error) {
	queryCtx, cancel := context.WithTimeout(ctx, time.Duration(h.DBQueryTimeout))
	defer cancel()

	start := time.Now()
	h.logger.Debug("executing db query",
		zap.String("hostname", hostname),
		zap.Duration("timeout", time.Duration(h.DBQueryTimeout)),
	)

	var rawJSON []byte
	err := h.pool.QueryRow(queryCtx, lookupQuery, hostname).Scan(&rawJSON)
	queryDur := time.Since(start)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Info("db query returned no rows",
				zap.String("hostname", hostname),
				zap.Duration("query_time", queryDur),
			)
			return upstreamTarget{}, errNotFound
		}
		h.metrics.dbErrors.Add(1)
		h.logger.Error("db query failed",
			zap.String("hostname", hostname),
			zap.Duration("query_time", queryDur),
			zap.Error(err),
		)
		return upstreamTarget{}, fmt.Errorf("db query: %w", err)
	}

	h.logger.Debug("db query succeeded",
		zap.String("hostname", hostname),
		zap.Duration("query_time", queryDur),
		zap.Int("response_bytes", len(rawJSON)),
	)

	var resp rawResponse
	if err := json.Unmarshal(rawJSON, &resp); err != nil {
		h.logger.Error("failed to parse raw_response JSON",
			zap.String("hostname", hostname),
			zap.Error(err),
			zap.ByteString("raw_response", rawJSON),
		)
		return upstreamTarget{}, fmt.Errorf("parse raw_response: %w", err)
	}

	h.logger.Debug("mapping record parsed",
		zap.String("hostname", hostname),
		zap.String("preview_id", resp.PreviewID),
		zap.String("task_ip", resp.TaskIP),
		zap.Int("preview_port", resp.PreviewPort),
		zap.String("status", resp.Status),
	)

	if resp.TaskIP == "" {
		h.logger.Warn("mapping has empty task_ip in raw_response",
			zap.String("hostname", hostname),
			zap.String("preview_id", resp.PreviewID),
		)
		return upstreamTarget{}, errNoTaskIP
	}

	port := resp.PreviewPort
	if port == 0 {
		h.logger.Debug("preview_port missing, using default",
			zap.String("hostname", hostname),
			zap.Int("default_port", h.DefaultPort),
		)
		port = h.DefaultPort
	}

	return upstreamTarget{IP: resp.TaskIP, Port: port}, nil
}

package usage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Recorder struct {
	rdb *redis.Client
	ch  chan record
}

type record struct {
	TaskID    string
	UserID    string
	MCPName   string
	ReqBytes  int64
	RespBytes int64
}

const (
	bufferSize = 4096
	keyTTL     = 7 * 24 * time.Hour
)

func NewRecorder(rdb *redis.Client) *Recorder {
	r := &Recorder{
		rdb: rdb,
		ch:  make(chan record, bufferSize),
	}
	return r
}

func (r *Recorder) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case rec := <-r.ch:
			r.flush(ctx, rec)
		}
	}
}

func (r *Recorder) Record(taskID, userID, mcpName string) {
	r.RecordWithBytes(taskID, userID, mcpName, 0, 0)
}

func (r *Recorder) RecordWithBytes(taskID, userID, mcpName string, reqBytes, respBytes int64) {
	select {
	case r.ch <- record{TaskID: taskID, UserID: userID, MCPName: mcpName, ReqBytes: reqBytes, RespBytes: respBytes}:
	default:
		slog.Warn("usage recorder channel full, dropping record", "mcp", mcpName)
	}
}

func (r *Recorder) flush(ctx context.Context, rec record) {
	date := time.Now().Format("20060102")

	pipe := r.rdb.Pipeline()

	// count
	taskKey := fmt.Sprintf("mcp:usage:%s", date)
	taskField := fmt.Sprintf("%s:%s", rec.TaskID, rec.MCPName)
	pipe.HIncrBy(ctx, taskKey, taskField, 1)
	pipe.Expire(ctx, taskKey, keyTTL)

	userKey := fmt.Sprintf("mcp:usage:user:%s", date)
	userField := fmt.Sprintf("%s:%s", rec.UserID, rec.MCPName)
	pipe.HIncrBy(ctx, userKey, userField, 1)
	pipe.Expire(ctx, userKey, keyTTL)

	// reqBytes / respBytes
	if rec.ReqBytes > 0 || rec.RespBytes > 0 {
		bytesKey := fmt.Sprintf("mcp:usage:bytes:%s", date)
		taskBytesField := fmt.Sprintf("%s:%s:req", rec.TaskID, rec.MCPName)
		pipe.HIncrBy(ctx, bytesKey, taskBytesField, rec.ReqBytes)
		taskRespField := fmt.Sprintf("%s:%s:resp", rec.TaskID, rec.MCPName)
		pipe.HIncrBy(ctx, bytesKey, taskRespField, rec.RespBytes)
		pipe.Expire(ctx, bytesKey, keyTTL)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("usage record write failed", "error", err)
	}
}

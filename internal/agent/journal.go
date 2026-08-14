package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Operation Journal：Agent 的持久操作日志。它以 bbolt 存储每个 operation 的
// payload digest、attempt、checkpoint 与终态结果，保证 Control Plane 重投时
// 不重复执行副作用：
//
//   - 同 operation 且同 digest：重投恢复执行中任务或重放终态结果；
//   - 同 operation 但不同 digest：拒绝执行（可能是指令被篡改或冲突）；
//   - 进程重启后日志仍在磁盘上，结果与终态跨重启保持。
const (
	journalBucketName    = "operations"
	journalMaxEntries    = 512
	journalStatusRunning = "running"
)

// journalEntry 是一条持久化的操作记录。
type journalEntry struct {
	Digest     string `json:"digest"` // 任务输入的 sha256 hex（不含租约栅栏字段）
	Status     string `json:"status"` // running | succeeded | failed
	Attempt    uint32 `json:"attempt"`
	Checkpoint string `json:"checkpoint,omitempty"`
	ErrorCode  string `json:"errorCode,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	ResultJSON []byte `json:"resultJson,omitempty"`
	Observed   []byte `json:"observed,omitempty"` // 序列化的 ServerObserved，终态后随结果重放
	UpdatedAt  int64  `json:"updatedAt"`
}

// OperationJournal 是 bbolt 支持的持久操作日志。所有方法内部自带事务，
// 可被多个 worker goroutine 并发调用。
type OperationJournal struct {
	db *bolt.DB
}

// OpenOperationJournal 打开（必要时创建）位于 path 的操作日志。
func OpenOperationJournal(path string) (*OperationJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open operation journal: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(journalBucketName))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init operation journal: %w", err)
	}
	return &OperationJournal{db: db}, nil
}

// Close 关闭底层 bbolt 数据库。
func (j *OperationJournal) Close() error {
	if j == nil || j.db == nil {
		return nil
	}
	return j.db.Close()
}

// Lookup 读取指定 operation 的记录；不存在时返回 ok=false。
func (j *OperationJournal) Lookup(operationID string) (journalEntry, bool, error) {
	var entry journalEntry
	found := false
	err := j.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(journalBucketName))
		if bucket == nil {
			return errors.New("operation journal bucket is missing")
		}
		raw := bucket.Get([]byte(operationID))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &entry)
	})
	return entry, found, err
}

// RecordRunning 把任务标记为执行中并保存 payload digest。同 operation 且
// digest 不同时返回 ErrJournalDigestMismatch 且不覆盖原记录。
var ErrJournalDigestMismatch = errors.New("operation digest mismatch")

func (j *OperationJournal) RecordRunning(operationID, digest string, attempt uint32) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(journalBucketName))
		if bucket == nil {
			return errors.New("operation journal bucket is missing")
		}
		if raw := bucket.Get([]byte(operationID)); raw != nil {
			var existing journalEntry
			if err := json.Unmarshal(raw, &existing); err == nil && existing.Digest != "" && existing.Digest != digest {
				return ErrJournalDigestMismatch
			}
		}
		entry := journalEntry{
			Digest:    digest,
			Status:    journalStatusRunning,
			Attempt:   attempt,
			UpdatedAt: time.Now().UTC().UnixNano(),
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(operationID), encoded); err != nil {
			return err
		}
		return j.evictLocked(bucket)
	})
}

// Complete 写入终态结果。执行中的同 digest 记录才可推进到终态。
func (j *OperationJournal) Complete(operationID, digest string, attempt uint32, succeeded bool, errorCode string, retryable bool, checkpoint string, resultJSON, observed []byte) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(journalBucketName))
		if bucket == nil {
			return errors.New("operation journal bucket is missing")
		}
		raw := bucket.Get([]byte(operationID))
		if raw == nil {
			return errors.New("operation has no running journal record")
		}
		var existing journalEntry
		if err := json.Unmarshal(raw, &existing); err != nil {
			return err
		}
		if existing.Digest != digest {
			return ErrJournalDigestMismatch
		}
		status := "failed"
		if succeeded {
			status = "succeeded"
		}
		entry := journalEntry{
			Digest:     digest,
			Status:     status,
			Attempt:    attempt,
			Checkpoint: checkpoint,
			ErrorCode:  errorCode,
			Retryable:  retryable,
			ResultJSON: resultJSON,
			Observed:   observed,
			UpdatedAt:  time.Now().UTC().UnixNano(),
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(operationID), encoded)
	})
}

// evictLocked 在记录数超过上限时按 UpdatedAt 淘汰最旧记录（每次写操作
// 摊销 O(n)，n 上限为 journalMaxEntries）。
func (j *OperationJournal) evictLocked(bucket *bolt.Bucket) error {
	type dated struct {
		key       string
		updatedAt int64
	}
	var entries []dated
	err := bucket.ForEach(func(key, raw []byte) error {
		var entry journalEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			entry.UpdatedAt = 0
		}
		entries = append(entries, dated{key: string(key), updatedAt: entry.UpdatedAt})
		return nil
	})
	if err != nil {
		return err
	}
	if len(entries) <= journalMaxEntries {
		return nil
	}
	sort.Slice(entries, func(i, k int) bool { return entries[i].updatedAt < entries[k].updatedAt })
	for _, item := range entries[:len(entries)-journalMaxEntries] {
		if err := bucket.Delete([]byte(item.key)); err != nil {
			return err
		}
	}
	return nil
}

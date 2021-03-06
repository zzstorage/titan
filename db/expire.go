package db

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"sync"
	"time"

	"github.com/distributedio/titan/conf"
	"github.com/distributedio/titan/db/store"
	"github.com/distributedio/titan/metrics"
	"go.uber.org/zap"
)

var (
	expireKeyPrefix     = []byte("$sys:0:at:")
	hashExpireKeyPrefix = expireKeyPrefix[:len(expireKeyPrefix)-1]
	sysExpireLeader     = []byte("$sys:0:EXL:EXLeader")

	// $sys:0:at:{ts}:{metaKey}
	expireTimestampOffset = len(expireKeyPrefix)
	expireMetakeyOffset   = expireTimestampOffset + 8 /*sizeof(int64)*/ + len(":")
)

const (
	expire_worker        = "expire"
	expire_unhash_worker = "expire-unhash"
	EXPIRE_HASH_NUM      = 256
)

type LeaderStatus struct {
	isLeader bool
	cond     *sync.Cond
}

func NewLeaderStatus() *LeaderStatus {
	return &LeaderStatus{
		cond: sync.NewCond(new(sync.Mutex)),
	}
}

func (ls *LeaderStatus) setIsLeader(isLeader bool) {
	ls.cond.L.Lock()
	defer ls.cond.L.Unlock()

	ls.isLeader = isLeader
	ls.cond.Broadcast()
}

func (ls *LeaderStatus) getIsLeader() bool {
	ls.cond.L.Lock()
	defer ls.cond.L.Unlock()

	ls.cond.Wait()
	return ls.isLeader
}

// IsExpired judge object expire through now
func IsExpired(obj *Object, now int64) bool {
	if obj.ExpireAt == 0 || obj.ExpireAt > now {
		return false
	}
	return true
}

func expireKey(key []byte, ts int64) []byte {
	hashnum := crc32.ChecksumIEEE(key)
	hashPrefix := fmt.Sprintf("%04d", hashnum%EXPIRE_HASH_NUM)
	var buf []byte
	buf = append(buf, hashExpireKeyPrefix...)
	buf = append(buf, []byte(hashPrefix)...)
	buf = append(buf, ':')
	buf = append(buf, EncodeInt64(ts)...)
	buf = append(buf, ':')
	buf = append(buf, key...)
	return buf
}

func expireAt(txn store.Transaction, mkey []byte, objID []byte, objType ObjectType, oldAt int64, newAt int64) error {
	oldKey := expireKey(mkey, oldAt)
	newKey := expireKey(mkey, newAt)

	if oldAt > 0 {
		if err := txn.Delete(oldKey); err != nil {
			return err
		}
	}

	if newAt > 0 {
		if err := txn.Set(newKey, objID); err != nil {
			return err
		}
	}
	action := ""
	if oldAt > 0 && newAt > 0 {
		action = "updated"
	} else if oldAt > 0 {
		action = "removed"
	} else if newAt > 0 {
		action = "added"
	}
	if action != "" {
		metrics.GetMetrics().ExpireKeysTotal.WithLabelValues(action).Inc()
	}
	return nil
}

func unExpireAt(txn store.Transaction, mkey []byte, expireAt int64) error {
	if expireAt == 0 {
		return nil
	}
	oldKey := expireKey(mkey, expireAt)
	if err := txn.Delete(oldKey); err != nil {
		return err
	}
	metrics.GetMetrics().ExpireKeysTotal.WithLabelValues("removed").Inc()
	return nil
}

// setExpireIsLeader get leader from db
func setExpireIsLeader(db *DB, conf *conf.Expire, ls *LeaderStatus) error {
	ticker := time.NewTicker(conf.Interval)
	defer ticker.Stop()
	id := UUID()
	for range ticker.C {
		if conf.Disable {
			ls.setIsLeader(false)
			continue
		}

		isLeader, err := isLeader(db, sysExpireLeader, id, conf.LeaderLifeTime)
		if err != nil {
			zap.L().Error("[Expire] check expire leader failed", zap.Error(err))
			ls.setIsLeader(false)
			continue
		}
		if !isLeader {
			if logEnv := zap.L().Check(zap.DebugLevel, "[Expire] not expire leader"); logEnv != nil {
				logEnv.Write(zap.ByteString("leader", sysExpireLeader),
					zap.ByteString("uuid", id),
					zap.Duration("leader-life-time", conf.LeaderLifeTime))
			}
			ls.setIsLeader(isLeader)
			continue
		}
		ls.setIsLeader(isLeader)
	}
	return nil
}

func startExpire(db *DB, conf *conf.Expire, ls *LeaderStatus, expireHash string) {
	ticker := time.NewTicker(conf.Interval)
	defer ticker.Stop()
	lastExpireEndTs := int64(0)
	for range ticker.C {
		if !ls.getIsLeader() {
			continue
		}

		start := time.Now()
		if expireHash != "" {
			lastExpireEndTs = runExpire(db, conf.BatchLimit, expireHash, lastExpireEndTs)
			metrics.GetMetrics().WorkerRoundCostHistogramVec.WithLabelValues(expire_worker).Observe(time.Since(start).Seconds())
		} else {
			lastExpireEndTs = runExpire(db, conf.UnhashBatchLimit, expireHash, lastExpireEndTs)
			metrics.GetMetrics().WorkerRoundCostHistogramVec.WithLabelValues(expire_unhash_worker).Observe(time.Since(start).Seconds())
		}

	}
}

// split a meta key with format: {namespace}:{id}:M:{key}
func splitMetaKey(key []byte) ([]byte, DBID, []byte) {
	idx := bytes.Index(key, []byte{':'})
	namespace := key[:idx]
	id := toDBID(key[idx+1 : idx+4])
	rawkey := key[idx+6:]
	return namespace, id, rawkey
}

func toTikvDataKey(namespace []byte, id DBID, key []byte) []byte {
	var b []byte
	b = append(b, namespace...)
	b = append(b, ':')
	b = append(b, id.Bytes()...)
	b = append(b, ':', 'D', ':')
	b = append(b, key...)
	return b
}

func toTikvScorePrefix(namespace []byte, id DBID, key []byte) []byte {
	var b []byte
	b = append(b, namespace...)
	b = append(b, ':')
	b = append(b, id.Bytes()...)
	b = append(b, ':', 'S', ':')
	b = append(b, key...)
	return b
}

func runExpire(db *DB, batchLimit int, expireHash string, lastExpireEndTs int64) int64 {
	curExpireTimestampOffset := expireTimestampOffset
	curExpireMetakeyOffset := expireMetakeyOffset
	var curExpireKeyPrefix []byte //expireKeyPrefix of current go routine
	var expireLogFlag string
	var metricsLabel string
	if expireHash != "" {
		curExpireKeyPrefix = append(curExpireKeyPrefix, hashExpireKeyPrefix...)
		curExpireKeyPrefix = append(curExpireKeyPrefix, expireHash...)
		curExpireKeyPrefix = append(curExpireKeyPrefix, ':')
		curExpireTimestampOffset += len(expireHash)
		curExpireMetakeyOffset += len(expireHash)
		expireLogFlag = fmt.Sprintf("[Expire-%s]", expireHash)
		metricsLabel = expire_worker
	} else {
		curExpireKeyPrefix = append(curExpireKeyPrefix, expireKeyPrefix...)
		expireLogFlag = "[Expire]"
		metricsLabel = expire_unhash_worker
	}

	txn, err := db.Begin()
	if err != nil {
		zap.L().Error(expireLogFlag+" txn begin failed", zap.Error(err))
		return 0
	}

	now := time.Now().UnixNano()
	//iter get keys [key, upperBound), so using now+1 as 2nd parameter will get "at:now:" prefixed keys
	//we seek end in "at:<now>" replace in "at;" , it can reduce the seek range and seek the deleted expired keys as little as possible.
	//the behavior should reduce the expire delay in days and get/mget timeout, which are caused by rocksdb tomstone problem
	var endPrefix []byte
	endPrefix = append(endPrefix, curExpireKeyPrefix...)
	endPrefix = append(endPrefix, EncodeInt64(now+1)...)

	var startPrefix []byte
	if lastExpireEndTs > 0 {
		startPrefix = append(startPrefix, curExpireKeyPrefix...)
		startPrefix = append(startPrefix, EncodeInt64(lastExpireEndTs)...)
		startPrefix = append(startPrefix, ':')
	} else {
		startPrefix = curExpireKeyPrefix
	}

	start := time.Now()
	iter, err := txn.t.Iter(startPrefix, endPrefix)
	metrics.GetMetrics().WorkerSeekCostHistogramVec.WithLabelValues(metricsLabel).Observe(time.Since(start).Seconds())
	if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" seek expire keys"); logEnv != nil {
		logEnv.Write(zap.Int64("[startTs", lastExpireEndTs), zap.Int64("endTs)", now+1))
	}
	if err != nil {
		zap.L().Error(expireLogFlag+" seek failed", zap.ByteString("prefix", curExpireKeyPrefix), zap.Error(err))
		txn.Rollback()
		return 0
	}
	limit := batchLimit

	thisExpireEndTs := int64(0)
	ts := now
	for iter.Valid() && iter.Key().HasPrefix(curExpireKeyPrefix) && limit > 0 {
		rawKey := iter.Key()
		ts = DecodeInt64(rawKey[curExpireTimestampOffset : curExpireTimestampOffset+8])
		if ts > now {
			if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" not need to expire key"); logEnv != nil {
				logEnv.Write(zap.String("raw-key", string(rawKey)), zap.Int64("last-timestamp", ts))
			}
			break
		}
		mkey := rawKey[curExpireMetakeyOffset:]
		if err := doExpire(txn, mkey, iter.Value(), expireLogFlag, ts); err != nil {
			txn.Rollback()
			return 0
		}

		// Remove from expire list
		if err := txn.t.Delete(rawKey); err != nil {
			zap.L().Error(expireLogFlag+" delete failed",
				zap.ByteString("mkey", mkey),
				zap.Error(err))
			txn.Rollback()
			return 0
		}

		if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" delete expire list item"); logEnv != nil {
			logEnv.Write(zap.Int64("ts", ts), zap.ByteString("mkey", mkey))
		}

		start = time.Now()
		err := iter.Next()
		cost := time.Since(start)
		if cost >= time.Millisecond {
			metrics.GetMetrics().WorkerSeekCostHistogramVec.WithLabelValues(metricsLabel).Observe(cost.Seconds())
		}
		if err != nil {
			zap.L().Error(expireLogFlag+" next failed",
				zap.ByteString("mkey", mkey),
				zap.Error(err))
			txn.Rollback()
			return 0
		}

		//just use the latest processed expireKey(don't include the last expire key in the loop which is > now) as next seek's start key
		thisExpireEndTs = ts
		limit--
	}
	if limit == batchLimit {
		//means: no expire keys or all expire keys > now in current loop
		thisExpireEndTs = now
	}

	now = time.Now().UnixNano()
	if ts < now {
		diff := (now - ts) / int64(time.Second)
		metrics.GetMetrics().ExpireDelaySecondsVec.WithLabelValues("delay-" + expireHash).Set(float64(diff))
	} else {
		metrics.GetMetrics().ExpireDelaySecondsVec.WithLabelValues("delay-" + expireHash).Set(0)
	}

	start = time.Now()
	err = txn.Commit(context.Background())
	metrics.GetMetrics().WorkerCommitCostHistogramVec.WithLabelValues(metricsLabel).Observe(time.Since(start).Seconds())
	if err != nil {
		txn.Rollback()
		zap.L().Error(expireLogFlag+" commit failed", zap.Error(err))
	}

	if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" expired end"); logEnv != nil {
		logEnv.Write(zap.Int("expired_num", batchLimit-limit))
	}

	if expireHash != "" {
		metrics.GetMetrics().ExpireKeysTotal.WithLabelValues("expired").Add(float64(batchLimit - limit))
	} else {
		metrics.GetMetrics().ExpireKeysTotal.WithLabelValues("expired-unhash").Add(float64(batchLimit - limit))
	}
	return thisExpireEndTs
}

func gcDataKey(txn *Transaction, namespace []byte, dbid DBID, key, id []byte, expireLogFlag string) error {
	dkey := toTikvDataKey(namespace, dbid, id)
	if err := gc(txn.t, dkey); err != nil {
		zap.L().Error(expireLogFlag+" gc failed",
			zap.ByteString("key", key),
			zap.ByteString("namepace", namespace),
			zap.Int64("db_id", int64(dbid)),
			zap.ByteString("obj_id", id),
			zap.Error(err))
		return err
	}
	if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" gc data key"); logEnv != nil {
		logEnv.Write(zap.ByteString("obj_id", id))
	}
	return nil
}

func doExpire(txn *Transaction, mkey, id []byte, expireLogFlag string, expireAt int64) error {
	namespace, dbid, key := splitMetaKey(mkey)
	obj, err := getObject(txn, mkey)
	// Check for dirty data due to copying or flushdb/flushall
	if err == ErrKeyNotFound {
		return gcDataKey(txn, namespace, dbid, key, id, expireLogFlag)
	}
	if err != nil {
		return err
	}
	idLen := len(obj.ID)
	if len(id) > idLen {
		id = id[:idLen]
	}

	//if a not-string structure haven't been deleted and set by user again after expire-time, because the expire go-routine is too slow and delayed.
	//the id in old expire-keys's value is different with the new one in the new key's value
	//so comparing id in doExpire() is necessary and also can handle below scenarios(should just delete old id object's data):
	//a not-string structure was set with unhashed expire-key, and then deleted and set again with hashed expire-key
	//or a string was set with unhashed expire-key, and set again with hashed expire-key
	if !bytes.Equal(obj.ID, id) {
		return gcDataKey(txn, namespace, dbid, key, id, expireLogFlag)
	}

	//compare expire-key's ts with object.expireat(their object id is same in the condition),
	//if different, means it's a not-string structure and its expire-key was rewriten in hashed prefix, but old ones was writen in unhashed prefix
	if obj.ExpireAt != expireAt {
		if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" it should be unhashed expire key un-matching key's expireAt, skip doExpire"); logEnv != nil {
			logEnv.Write(zap.ByteString("mkey", mkey), zap.Int64("this expire key's ts", expireAt), zap.Int64("key's expireAt", obj.ExpireAt))
		}
		return nil
	}

	// Delete object meta
	if err := txn.t.Delete(mkey); err != nil {
		zap.L().Error(expireLogFlag+" delete failed",
			zap.ByteString("key", key),
			zap.Error(err))
		return err
	}

	if logEnv := zap.L().Check(zap.DebugLevel, expireLogFlag+" delete metakey"); logEnv != nil {
		logEnv.Write(zap.ByteString("mkey", mkey))
	}
	if obj.Type == ObjectString {
		return nil
	}
	return gcDataKey(txn, namespace, dbid, key, id, expireLogFlag)
}

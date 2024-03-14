// Copyright 2024 The CubeFS Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package metanode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"

	// "runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cubefs/cubefs/util"
	// "github.com/cubefs/cubefs/util/exporter"
	"github.com/cubefs/cubefs/util/log"
	"github.com/tecbot/gorocksdb"
)

const (
	DefLRUCacheSize       = 256 * util.MB
	DefWriteBuffSize      = 4 * util.MB
	DefRetryCount         = 3
	DefMaxWriteBatchCount = 1000
	DefWalSizeLimitMB     = 5
	DefWalMemSizeLimitMB  = 2
	DefMaxLogFileSize     = 1 //MB
	DefLogFileRollTimeDay = 3 // 3 day
	DefLogReservedCnt     = 3
	DefWalTTL             = 60 //second
)

var (
	ErrRocksdbAccess             = errors.New("access rocksdb error")
	ErrRocksdbOperation          = errors.New("rocksdb operation error")
	ErrInvalidRocksdbWriteHandle = errors.New("invalid rocksdb write batch")
	ErrInvalidRocksdbTableType   = errors.New("invalid rocksdb table type")
	ErrInvalidRocksdbSnapshot    = errors.New("invalid rocksdb snapshot")
)

type TableType byte

const (
	BaseInfoTable TableType = iota
	DentryTable
	InodeTable
	ExtendTable
	MultipartTable
	TransactionTable
	TransactionRollbackInodeTable
	TransactionRollbackDentryTable
	DeletedExtentsTable
	DeletedObjExtentsTable
	MaxTable
)

const (
	SyncWalTable = 255
)

func getTableTypeKey(treeType TreeType) TableType {
	switch treeType {
	case InodeType:
		return InodeTable
	case DentryType:
		return DentryTable
	case MultipartType:
		return MultipartTable
	case ExtendType:
		return ExtendTable
	case TransactionType:
		return TransactionTable
	case TransactionRollbackInodeType:
		return TransactionRollbackInodeTable
	case TransactionRollbackDentryType:
		return TransactionRollbackDentryTable
	case DeletedExtentsType:
		return DeletedExtentsTable
	case DeletedObjExtentsType:
		return DeletedObjExtentsTable
	default:
	}
	panic(ErrInvalidRocksdbTableType)
}

const (
	dbInitSt uint32 = iota
	dbOpenningSt
	dbOpenedSt
	dbClosingSt
	dbClosedSt
)

func isRetryError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "Try again") {
		return true
	}
	return false
}

type RocksDbInfo struct {
	dir                  string
	defOption            *gorocksdb.Options
	defBasedTableOptions *gorocksdb.BlockBasedTableOptions
	defReadOption        *gorocksdb.ReadOptions
	defWriteOption       *gorocksdb.WriteOptions
	defFlushOption       *gorocksdb.FlushOptions
	defSyncOption        *gorocksdb.WriteOptions
	db                   *gorocksdb.DB
	mutex                sync.RWMutex
	state                uint32
	syncCnt              uint64
	SyncFlag             bool
}

func NewRocksDb() (dbInfo *RocksDbInfo) {
	dbInfo = &RocksDbInfo{
		state: dbInitSt,
	}
	return
}

func (dbInfo *RocksDbInfo) CloseDb() (err error) {
	log.LogDebugf("close RocksDB, Path(%s), State(%v)", dbInfo.dir, atomic.LoadUint32(&dbInfo.state))

	if ok := atomic.CompareAndSwapUint32(&dbInfo.state, dbOpenedSt, dbClosingSt); !ok {
		if atomic.LoadUint32(&dbInfo.state) == dbClosedSt {
			//already closed
			return nil
		}
		return fmt.Errorf("db state error, cur: %v, to:%v", dbInfo.state, dbClosingSt)
	}

	dbInfo.mutex.Lock()
	defer func() {
		if err == nil {
			atomic.CompareAndSwapUint32(&dbInfo.state, dbClosingSt, dbClosedSt)
		} else {
			atomic.CompareAndSwapUint32(&dbInfo.state, dbClosingSt, dbOpenedSt)
		}
		dbInfo.mutex.Unlock()
	}()

	dbInfo.db.Close()
	dbInfo.defReadOption.Destroy()
	dbInfo.defWriteOption.Destroy()
	dbInfo.defFlushOption.Destroy()
	dbInfo.defSyncOption.Destroy()
	dbInfo.defBasedTableOptions.Destroy()
	dbInfo.defOption.Destroy()

	dbInfo.db = nil
	dbInfo.defReadOption = nil
	dbInfo.defWriteOption = nil
	dbInfo.defFlushOption = nil
	dbInfo.defSyncOption = nil
	dbInfo.defBasedTableOptions = nil
	dbInfo.defOption = nil
	return
}
func (dbInfo *RocksDbInfo) CommitEmptyRecordToSyncWal(flushWal bool) {
	var err error
	dbInfo.syncCnt += 1
	log.LogWarnf("db[%v] start sync, flush wal flag:%v", dbInfo.dir, flushWal)
	if err = dbInfo.accessDb(); err != nil {
		log.LogErrorf("db[%v] sync finished; failed:%v", dbInfo.dir, err)
		return
	}
	defer dbInfo.releaseDb()
	key := make([]byte, 1)
	value := make([]byte, 8)
	key[0] = SyncWalTable
	binary.BigEndian.PutUint64(value, dbInfo.syncCnt)
	dbInfo.defSyncOption.SetSync(dbInfo.SyncFlag)
	dbInfo.db.Put(dbInfo.defSyncOption, key, value)

	if flushWal {
		dbInfo.defFlushOption.SetWait(dbInfo.SyncFlag)
		err = dbInfo.db.Flush(dbInfo.defFlushOption)
	}

	if err != nil {
		log.LogErrorf("db[%v] sync(sync:%v-flush:%v) finished; failed:%v", dbInfo.dir, dbInfo.SyncFlag, flushWal, err)
		return
	}
	log.LogWarnf("db[%v] sync(sync:%v-flush:%v) finished; success", dbInfo.dir, dbInfo.SyncFlag, flushWal)
	return
}

func (dbInfo *RocksDbInfo) interOpenDb(dir string, walFileSize, walMemSize, logFileSize, logReversed, logReversedCnt, walTTL uint64) (err error) {
	var stat fs.FileInfo

	stat, err = os.Stat(dir)
	if err == nil && !stat.IsDir() {
		log.LogErrorf("interOpenDb path:[%s] is not dir", dir)
		return fmt.Errorf("path:[%s] is not dir", dir)
	}

	if err != nil && !os.IsNotExist(err) {
		log.LogErrorf("interOpenDb stat error: dir: %v, err: %v", dir, err)
		return err
	}

	//mkdir all  will return nil when path exist and path is dir
	if err = os.MkdirAll(dir, os.ModePerm); err != nil {
		log.LogErrorf("interOpenDb mkdir error: dir: %v, err: %v", dir, err)
		return err
	}

	log.LogInfof("rocks db dir:[%s]", dir)

	//adjust param
	if walFileSize == 0 {
		walFileSize = DefWalSizeLimitMB
	}
	if walMemSize == 0 {
		walMemSize = DefWalMemSizeLimitMB
	}
	if logFileSize == 0 {
		logFileSize = DefMaxLogFileSize
	}
	if logReversed == 0 {
		logReversed = DefLogFileRollTimeDay
	}
	if logReversedCnt == 0 {
		logReversedCnt = DefLogReservedCnt
	}
	if walTTL == 0 {
		walTTL = DefWalTTL
	}

	basedTableOptions := gorocksdb.NewDefaultBlockBasedTableOptions()
	basedTableOptions.SetBlockCache(gorocksdb.NewLRUCache(DefLRUCacheSize))
	opts := gorocksdb.NewDefaultOptions()
	opts.SetBlockBasedTableFactory(basedTableOptions)
	opts.SetCreateIfMissing(true)
	opts.SetWriteBufferSize(DefWriteBuffSize)
	opts.SetMaxWriteBufferNumber(2)
	opts.SetCompression(gorocksdb.NoCompression)

	opts.SetWalSizeLimitMb(walFileSize)
	opts.SetMaxTotalWalSize(walMemSize * util.MB)
	opts.SetMaxLogFileSize(int(logFileSize * util.MB))
	opts.SetLogFileTimeToRoll(int(logReversed * 60 * 60 * 24))
	opts.SetKeepLogFileNum(int(logReversedCnt))
	opts.SetWALTtlSeconds(walTTL)
	//opts.SetParanoidChecks(true)
	for index := 0; index < DefRetryCount; {
		dbInfo.db, err = gorocksdb.OpenDb(opts, dir)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("interOpenDb open db err:%v", err)
			break
		}
		log.LogErrorf("interOpenDb open db with retry error:%v", err)
		index++
	}
	if err != nil {
		log.LogErrorf("interOpenDb open db err:%v", err)
		return ErrRocksdbOperation
	}
	dbInfo.dir = dir
	dbInfo.defOption = opts
	dbInfo.defBasedTableOptions = basedTableOptions
	dbInfo.defReadOption = gorocksdb.NewDefaultReadOptions()
	dbInfo.defWriteOption = gorocksdb.NewDefaultWriteOptions()
	dbInfo.defFlushOption = gorocksdb.NewDefaultFlushOptions()
	dbInfo.defSyncOption = gorocksdb.NewDefaultWriteOptions()
	dbInfo.SyncFlag = true
	dbInfo.defSyncOption.SetSync(dbInfo.SyncFlag)

	//dbInfo.defWriteOption.DisableWAL(true)
	return nil
}

func (dbInfo *RocksDbInfo) OpenDb(dir string, walFileSize, walMemSize, logFileSize, logReversed, logReversedCnt, walTTL uint64) (err error) {
	ok := atomic.CompareAndSwapUint32(&dbInfo.state, dbInitSt, dbOpenningSt)
	ok = ok || atomic.CompareAndSwapUint32(&dbInfo.state, dbClosedSt, dbOpenningSt)
	if !ok {
		if atomic.LoadUint32(&dbInfo.state) == dbOpenedSt {
			//already opened
			return nil
		}
		return fmt.Errorf("db state error, cur: %v, to:%v", dbInfo.state, dbOpenningSt)
	}

	dbInfo.mutex.Lock()
	defer func() {
		if err == nil {
			atomic.CompareAndSwapUint32(&dbInfo.state, dbOpenningSt, dbOpenedSt)
		} else {
			log.LogErrorf("OpenDb failed, dir:%s error:%v", dir, err)
			atomic.CompareAndSwapUint32(&dbInfo.state, dbOpenningSt, dbInitSt)
		}
		dbInfo.mutex.Unlock()
	}()

	return dbInfo.interOpenDb(dir, walFileSize, walMemSize, logFileSize, logReversed, logReversedCnt, walTTL)
}

func (dbInfo *RocksDbInfo) ReOpenDb(dir string, walFileSize, walMemSize, logFileSize, logReversed, logReversedCnt, walTTL uint64) (err error) {
	if ok := atomic.CompareAndSwapUint32(&dbInfo.state, dbClosedSt, dbOpenningSt); !ok {
		if atomic.LoadUint32(&dbInfo.state) == dbOpenedSt {
			//already opened
			return nil
		}
		return fmt.Errorf("db state error, cur: %v, to:%v", dbInfo.state, dbOpenningSt)
	}

	dbInfo.mutex.Lock()
	defer func() {
		if err == nil {
			atomic.CompareAndSwapUint32(&dbInfo.state, dbOpenningSt, dbOpenedSt)
		} else {
			atomic.CompareAndSwapUint32(&dbInfo.state, dbOpenningSt, dbClosedSt)
		}
		dbInfo.mutex.Unlock()
	}()

	if dbInfo == nil || (dbInfo.dir != "" && dbInfo.dir != dir) {
		return fmt.Errorf("rocks db dir changed, need new db instance")
	}

	return dbInfo.interOpenDb(dir, walFileSize, walMemSize, logFileSize, logReversed, logReversedCnt, walTTL)

}

func genRocksDBReadOption(snap *gorocksdb.Snapshot) (ro *gorocksdb.ReadOptions) {
	ro = gorocksdb.NewDefaultReadOptions()
	ro.SetFillCache(false)
	ro.SetSnapshot(snap)
	return
}

func (dbInfo *RocksDbInfo) iterator(ro *gorocksdb.ReadOptions) *gorocksdb.Iterator {
	return dbInfo.db.NewIterator(ro)
}

func (dbInfo *RocksDbInfo) rangeWithIter(it *gorocksdb.Iterator, start []byte, end []byte, cb func(k, v []byte) (bool, error)) error {
	it.Seek(start)
	for ; it.ValidForPrefix(start); it.Next() {
		key := it.Key().Data()
		value := it.Value().Data()
		if bytes.Compare(end, key) < 0 {
			break
		}
		if hasNext, err := cb(key, value); err != nil {
			log.LogErrorf("[RocksDB Op] RangeWithIter key: %v value: %v err: %v", key, value, err)
			return err
		} else if !hasNext {
			return nil
		}
	}
	return nil
}

func (dbInfo *RocksDbInfo) rangeWithIterByPrefix(it *gorocksdb.Iterator, prefix, start, end []byte, cb func(k, v []byte) (bool, error)) error {
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		key := it.Key().Data()
		value := it.Value().Data()
		if bytes.Compare(key, start) < 0 {
			continue
		}
		if bytes.Compare(end, key) < 0 {
			break
		}
		if hasNext, err := cb(key, value); err != nil {
			log.LogErrorf("[RocksTree] RangeWithIter key: %v value: %v err: %v", key, value, err)
			return err
		} else if !hasNext {
			return nil
		}
	}
	return nil
}

func (dbInfo *RocksDbInfo) descRangeWithIter(it *gorocksdb.Iterator, start []byte, end []byte, cb func(k, v []byte) (bool, error)) error {
	it.SeekForPrev(end)
	for ; it.ValidForPrefix(start); it.Prev() {
		key := it.Key().Data()
		value := it.Value().Data()
		if bytes.Compare(key, start) < 0 {
			break
		}
		if hasNext, err := cb(key, value); err != nil {
			log.LogErrorf("[RocksDB Op] RangeWithIter key: %v value: %v err: %v", key, value, err)
			return err
		} else if !hasNext {
			return nil
		}
	}
	return nil
}

func (dbInfo *RocksDbInfo) accessDb() error {
	if atomic.LoadUint32(&dbInfo.state) != dbOpenedSt {
		log.LogErrorf("[RocksDB Op] can not access db, db is not opened. Cur state:%v", dbInfo.state)
		return ErrRocksdbAccess
	}

	dbInfo.mutex.RLock()
	if atomic.LoadUint32(&dbInfo.state) != dbOpenedSt {
		dbInfo.mutex.RUnlock()
		log.LogErrorf("[RocksDB Op] can not access db, db is not opened. Cur state:%v", dbInfo.state)
		return ErrRocksdbAccess
	}
	return nil
}

func (dbInfo *RocksDbInfo) releaseDb() {
	dbInfo.mutex.RUnlock()
}

// NOTE: hold the lock while using snapshot
func (dbInfo *RocksDbInfo) OpenSnap() *gorocksdb.Snapshot {
	if err := dbInfo.accessDb(); err != nil {
		log.LogErrorf("[RocksDB Op] OpenSnap failed:%v", err)
		return nil
	}

	snap := dbInfo.db.NewSnapshot()
	if snap == nil {
		dbInfo.releaseDb()
	}
	return snap
}

func (dbInfo *RocksDbInfo) ReleaseSnap(snap *gorocksdb.Snapshot) {
	if snap == nil {
		return
	}
	defer dbInfo.releaseDb()

	dbInfo.db.ReleaseSnapshot(snap)
}

func (dbInfo *RocksDbInfo) RangeWithSnap(start, end []byte, snap *gorocksdb.Snapshot, cb func(k, v []byte) (bool, error)) (err error) {
	if snap == nil {
		return ErrInvalidRocksdbSnapshot
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	ro := genRocksDBReadOption(snap)
	it := dbInfo.iterator(ro)
	defer func() {
		it.Close()
		ro.Destroy()
	}()
	return dbInfo.rangeWithIter(it, start, end, cb)
}

func (dbInfo *RocksDbInfo) GetBytesWithSnap(snap *gorocksdb.Snapshot, key []byte) (value []byte, err error) {
	if snap == nil {
		err = ErrInvalidRocksdbSnapshot
		return
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	ro := genRocksDBReadOption(snap)
	for index := 0; index < DefRetryCount; {
		value, err = dbInfo.db.GetBytes(ro, key)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] GetBytes failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] GetBytes failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] GetBytes err:%v", err)
		err = ErrRocksdbOperation
		return
	}
	return
}

func (dbInfo *RocksDbInfo) RangeWithSnapByPrefix(prefix, start, end []byte, snap *gorocksdb.Snapshot, cb func(k, v []byte) (bool, error)) (err error) {
	if snap == nil {
		return ErrInvalidRocksdbSnapshot
	}

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	ro := genRocksDBReadOption(snap)
	it := dbInfo.iterator(ro)
	defer func() {
		it.Close()
		ro.Destroy()
	}()
	return dbInfo.rangeWithIterByPrefix(it, prefix, start, end, cb)
}

func (dbInfo *RocksDbInfo) DescRangeWithSnap(start, end []byte, snap *gorocksdb.Snapshot, cb func(k, v []byte) (bool, error)) (err error) {
	if snap == nil {
		return ErrInvalidRocksdbSnapshot
	}

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	ro := genRocksDBReadOption(snap)
	it := dbInfo.iterator(ro)
	defer func() {
		it.Close()
		ro.Destroy()
	}()
	return dbInfo.descRangeWithIter(it, start, end, cb)
}

func (dbInfo *RocksDbInfo) Range(start, end []byte, cb func(k, v []byte) (bool, error)) (err error) {
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	snapshot := dbInfo.db.NewSnapshot()
	ro := genRocksDBReadOption(snapshot)
	it := dbInfo.iterator(ro)
	defer func() {
		it.Close()
		ro.Destroy()
		dbInfo.db.ReleaseSnapshot(snapshot)
	}()
	return dbInfo.rangeWithIter(it, start, end, cb)
}

func (dbInfo *RocksDbInfo) DescRange(start, end []byte, cb func(k, v []byte) (bool, error)) (err error) {
	if err = dbInfo.accessDb(); err != nil {
		return err
	}
	defer dbInfo.releaseDb()

	snapshot := dbInfo.db.NewSnapshot()
	ro := genRocksDBReadOption(snapshot)
	it := dbInfo.iterator(ro)
	defer func() {
		it.Close()
		ro.Destroy()
		dbInfo.db.ReleaseSnapshot(snapshot)
	}()
	return dbInfo.descRangeWithIter(it, start, end, cb)
}

func (dbInfo *RocksDbInfo) GetBytes(key []byte) (bytes []byte, err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] GetBytes failed, error:%v", err)
		}
	}()

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	for index := 0; index < DefRetryCount; {
		bytes, err = dbInfo.db.GetBytes(dbInfo.defReadOption, key)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] GetBytes failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] GetBytes failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] GetBytes err:%v", err)
		err = ErrRocksdbOperation
		return
	}
	return
}

func (dbInfo *RocksDbInfo) HasKey(key []byte) (bool, error) {
	bs, err := dbInfo.GetBytes(key)
	if err != nil {
		return false, err
	}
	return len(bs) > 0, nil
}

func (dbInfo *RocksDbInfo) Put(key, value []byte) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] Put failed, error:%v", err)
		}
	}()

	if err = dbInfo.accessDb(); err != nil {
		return err
	}
	defer dbInfo.releaseDb()
	for index := 0; index < DefRetryCount; {
		err = dbInfo.db.Put(dbInfo.defWriteOption, key, value)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] Put failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] Put failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] Put err:%v", err)
		err = ErrRocksdbOperation
		return
	}
	return
}

func (dbInfo *RocksDbInfo) Del(key []byte) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] Del failed, error:%v", err)
		}
	}()

	if err = dbInfo.accessDb(); err != nil {
		return err
	}
	defer dbInfo.releaseDb()
	for index := 0; index < DefRetryCount; {
		err = dbInfo.db.Delete(dbInfo.defWriteOption, key)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] Del failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] Del failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] Del err:%v", err)
		err = ErrRocksdbOperation
		return
	}
	return
}

func (dbInfo *RocksDbInfo) CreateBatchHandler() (interface{}, error) {
	var err error
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] CreateBatchHandler failed, error:%v", err)
		}
	}()

	if err = dbInfo.accessDb(); err != nil {
		return nil, err
	}
	defer dbInfo.releaseDb()
	batch := gorocksdb.NewWriteBatch()
	return batch, nil
}

func (dbInfo *RocksDbInfo) AddItemToBatch(handle interface{}, key, value []byte) (err error) {
	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		return ErrInvalidRocksdbWriteHandle
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	batch.Put(key, value)
	return nil
}

func (dbInfo *RocksDbInfo) DelItemToBatch(handle interface{}, key []byte) (err error) {
	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		return ErrInvalidRocksdbWriteHandle
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	batch.Delete(key)
	return nil
}

func (dbInfo *RocksDbInfo) DelRangeToBatch(handle interface{}, start []byte, end []byte) (err error) {
	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		return ErrInvalidRocksdbWriteHandle
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	batch.DeleteRange(start, end)
	return nil
}

func (dbInfo *RocksDbInfo) CommitBatchAndRelease(handle interface{}) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] CommitBatchAndRelease failed, err:%v", err)
		}
	}()

	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		err = ErrInvalidRocksdbWriteHandle
		return
	}

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	for index := 0; index < DefRetryCount; {
		err = dbInfo.db.Write(dbInfo.defWriteOption, batch)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] CommitBatchAndRelease write failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] CommitBatchAndRelease write failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] CommitBatchAndRelease write failed:%v", err)
		err = ErrRocksdbOperation
		return
	}
	batch.Destroy()
	return
}

func (dbInfo *RocksDbInfo) HandleBatchCount(handle interface{}) (count int, err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] CommitBatchAndRelease failed, err:%v", err)
		}
	}()

	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		err = ErrInvalidRocksdbWriteHandle
		return
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	count = batch.Count()
	return
}

func (dbInfo *RocksDbInfo) CommitBatch(handle interface{}) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] CommitBatch failed, err:%v", err)
		}
	}()

	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		err = ErrInvalidRocksdbWriteHandle
		return
	}

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	for index := 0; index < DefRetryCount; {
		err = dbInfo.db.Write(dbInfo.defWriteOption, batch)
		if err == nil {
			break
		}
		if !isRetryError(err) {
			log.LogErrorf("[RocksDB Op] CommitBatch write failed, error(%v)", err)
			break
		}
		log.LogErrorf("[RocksDB Op] CommitBatch write failed with retry error(%v), continue", err)
		index++
	}
	if err != nil {
		log.LogErrorf("[RocksDB Op] CommitBatch write failed, error(%v)", err)
		err = ErrRocksdbOperation
		return
	}
	return
}

func (dbInfo *RocksDbInfo) ReleaseBatchHandle(handle interface{}) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] ReleaseBatchHandle failed, err:%v", err)
		}
	}()

	if handle == nil {
		return
	}

	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		err = ErrInvalidRocksdbWriteHandle
		return
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	batch.Destroy()
	return
}

func (dbInfo *RocksDbInfo) ClearBatchWriteHandle(handle interface{}) (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] ClearBatchWriteHandle failed, err:%v", err)
		}
	}()

	batch, ok := handle.(*gorocksdb.WriteBatch)
	if !ok {
		err = ErrInvalidRocksdbWriteHandle
		return
	}
	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()
	batch.Clear()
	return
}

func (dbInfo *RocksDbInfo) Flush() (err error) {
	defer func() {
		if err != nil {
			log.LogErrorf("[RocksDB Op] ClearBatchWriteHandle failed, err:%v", err)
		}
	}()

	if err = dbInfo.accessDb(); err != nil {
		return
	}
	defer dbInfo.releaseDb()

	return dbInfo.db.Flush(dbInfo.defFlushOption)
}

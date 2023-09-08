// Copyright 2018 The CubeFS Authors.
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

package cache_engine

import (
	"context"
	"fmt"
	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/errors"
	"github.com/cubefs/cubefs/util/exporter"
	"github.com/cubefs/cubefs/util/log"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CacheBlockOpenOpt = os.O_CREATE | os.O_RDWR | os.O_EXCL
)

type CacheStat int

const (
	CacheNew = iota
	CacheReady
	CacheClose
)

type CacheBlock struct {
	file        *os.File
	volume      string
	inode       uint64
	fixedOffset uint64
	version     uint32
	rootPath    string
	filePath    string
	modifyTime  int64
	usedSize    int64 //usedSize是缓存文件的真实Size
	allocSize   int64 //allocSize是为了避免并发回源导致tmpfs满，而预先将所有source按照4K对齐后的Size之和，是逻辑值
	blockKey    string
	readSource  ReadExtentData
	cacheStat   int32
	initOnce    sync.Once
	readyCh     chan struct{}
	closeCh     chan struct{}
	sync.Mutex
}

// NewCacheBlock create and returns a new extent instance.
func NewCacheBlock(path string, volume string, inode, fixedOffset uint64, version uint32, allocSize uint64, reader ReadExtentData) (cb *CacheBlock) {
	cb = new(CacheBlock)
	cb.volume = volume
	cb.inode = inode
	cb.fixedOffset = fixedOffset
	cb.version = version
	cb.blockKey = GenCacheBlockKey(volume, inode, fixedOffset, version)
	cb.allocSize = int64(allocSize)
	cb.filePath = path + "/" + cb.blockKey
	cb.rootPath = path
	cb.readSource = reader
	cb.readyCh = make(chan struct{}, 1)
	cb.closeCh = make(chan struct{}, 1)
	return
}

func (cb *CacheBlock) String() string {
	return fmt.Sprintf("volume(%v) inode(%v) offset(%v) version(%v)", cb.volume, cb.inode, cb.fixedOffset, cb.version)
}

// Close this extent and release FD.
func (cb *CacheBlock) Close() (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.LogErrorf("key(%v) recover on close:%v", cb.blockKey, r)
		}
	}()
	close(cb.closeCh)
	if cb.file == nil {
		return
	}
	if err = cb.file.Close(); err != nil {
		return
	}
	return
}

func (cb *CacheBlock) Delete() (err error) {
	if !cb.Exist() {
		return
	}
	_ = cb.Close()
	err = os.Remove(cb.filePath)
	if err != nil {
		return err
	}
	return
}

func (cb *CacheBlock) Exist() (exsit bool) {
	_, err := os.Stat(cb.filePath)
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

// WriteAt writes data to an cacheBlock, only append write supported
func (cb *CacheBlock) WriteAt(data []byte, offset, size int64) (err error) {
	if err = cb.checkOffsetAndSize(offset, size); err != nil {
		return
	}
	if _, err = cb.file.WriteAt(data[:size], offset); err != nil {
		return
	}
	atomic.StoreInt64(&cb.modifyTime, time.Now().Unix())
	cb.usedSize = int64(math.Max(float64(cb.usedSize), float64(offset+size)))
	return
}

// Read reads data from an extent.
func (cb *CacheBlock) Read(ctx context.Context, data []byte, offset, size int64, printSource func() string) (crc uint32, err error) {
	if err = cb.waitCacheReady(ctx); err != nil {
		return
	}
	if offset >= cb.allocSize || offset > cb.usedSize {
		return 0, fmt.Errorf("invalid read, offset:%d, allocSize:%d, usedSize:%d", offset, cb.allocSize, cb.usedSize)
	}
	realSize := cb.usedSize - offset
	if realSize >= size {
		realSize = size
	} else {
		if log.IsWarnEnabled() {
			if strings.HasPrefix(cb.blockKey, "dcc_3vol") {
				log.LogWarnf("action[Read] overflow read, cache block:%v, offset:%d, size:%d, usedSize:%d, allocSize:%d, request: \n%v", cb.blockKey, offset, size, cb.usedSize, cb.allocSize, printSource())
			}
		}
	}
	if _, err = cb.file.ReadAt(data[:realSize], offset); err != nil {
		return
	}
	return
}

// todo: use end size to replace allocSize
func (cb *CacheBlock) checkOffsetAndSize(offset, size int64) error {
	if offset+size > cb.allocSize {
		return NewParameterMismatchErr(fmt.Sprintf("offset=%v size=%v allocSize:%d", offset, size, cb.allocSize))
	}
	if offset >= cb.allocSize || size == 0 {
		return NewParameterMismatchErr(fmt.Sprintf("offset=%v size=%v allocSize:%d", offset, size, cb.allocSize))
	}
	return nil
}

func (cb *CacheBlock) initFilePath() (err error) {
	err = os.Mkdir(cb.rootPath+"/"+cb.volume, 0666)
	if err != nil {
		if !os.IsExist(err) {
			return
		}
		err = nil
	}
	if cb.file, err = os.OpenFile(cb.filePath, CacheBlockOpenOpt, 0666); err != nil {
		return err
	}
	atomic.StoreInt64(&cb.modifyTime, time.Now().Unix())
	cb.usedSize = 0
	if log.IsDebugEnabled() {
		log.LogDebugf("init cache block(%s) to tmpfs", cb.blockKey)
	}
	return
}

func (cb *CacheBlock) Init(sources []*proto.DataSource) {
	metric := exporter.NewModuleTPUs("InitBlock")
	defer func() {
		metric.Set(nil)
	}()
	//parallel read source data
	sourceTaskCh := make(chan *proto.DataSource, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := sync.WaitGroup{}
	for i := 0; i < int(math.Min(float64(20), float64(len(sources)))); i++ {
		wg.Add(1)
		go cb.prepareSource(ctx, cancel, &wg, sourceTaskCh)
	}
	var stop bool
	var sb = strings.Builder{}
	for _, s := range sources {
		select {
		case sourceTaskCh <- s:
			sb.WriteString(fmt.Sprintf("dp(%v) extent(%v) offset(%v) size(%v) fileOffset(%v) hosts(%v)\n", s.PartitionID, s.ExtentID, s.ExtentOffset, s.Size_, s.FileOffset, strings.Join(s.Hosts, ",")))
		case <-ctx.Done():
			stop = true
		}
		if stop {
			break
		}
	}
	close(sourceTaskCh)
	wg.Wait()

	if ctx.Err() != nil {
		cb.markClose()
		return
	}
	if log.IsWarnEnabled() {
		if strings.HasPrefix(cb.blockKey, "dcc_3vol") {
			log.LogWarnf("action[Init], cache block:%v, sources:\n%v", cb.blockKey, sb.String())
		}
	}
	cb.markReady()
	return
}

func (cb *CacheBlock) prepareSource(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, taskCh chan *proto.DataSource) (err error) {
	defer func() {
		if err != nil {
			cancel()
		}
		wg.Done()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-taskCh:
			if task == nil {
				return
			}
			localStart := int64(task.FileOffset) & (proto.CACHE_BLOCK_SIZE - 1)
			writeCacheAfterRead := func(data []byte, size int64) error {
				e := cb.WriteAt(data, localStart, size)
				if e != nil {
					return e
				}
				localStart += size
				return nil
			}
			if log.IsDebugEnabled() {
				log.LogDebugf("action[prepareSource] cache block(%s), dp:%d, extent:%d, ExtentOffset:%v, Size:%v, localStart:%d", cb.blockKey, task.PartitionID, task.ExtentID, task.ExtentOffset, task.Size_, localStart)
			}
			if _, err = cb.readSource(task, writeCacheAfterRead); err != nil {
				log.LogErrorf("action[prepareSource] cache block(%s), dp:%d, extent:%d, ExtentOffset:%v, Size:%v, localStart:%d, readSource err:%v", cb.blockKey, task.PartitionID, task.ExtentID, task.ExtentOffset, task.Size_, localStart, err)
				return
			}
		}
	}
}

func (cb *CacheBlock) waitCacheReady(ctx context.Context) error {
	if atomic.LoadInt32(&cb.cacheStat) == CacheReady {
		return nil
	}
	if atomic.LoadInt32(&cb.cacheStat) == CacheNew {
		log.LogInfof("action[waitCacheReady] key(%s)", cb.blockKey)
		select {
		case <-cb.readyCh:
			return nil
		case <-cb.closeCh:
			return CacheClosedError
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if atomic.LoadInt32(&cb.cacheStat) == CacheClose {
		return CacheClosedError
	}
	return errors.New("unknown status")
}

func (cb *CacheBlock) markClose() {
	atomic.StoreInt32(&cb.cacheStat, CacheClose)
}

func (cb *CacheBlock) markReady() {
	atomic.StoreInt32(&cb.cacheStat, CacheReady)
	close(cb.readyCh)
}

// align AllocSize with PageSize-4KB
func computeAllocSize(sources []*proto.DataSource) (alloc uint64) {
	for _, s := range sources {
		blockOffset := s.FileOffset & (proto.CACHE_BLOCK_SIZE - 1)
		blockEnd := blockOffset + s.Size_ - 1
		pageOffset := blockOffset / proto.PageSize
		pageEnd := blockEnd / proto.PageSize
		if blockEnd < blockOffset {
			return 0
		}
		for i := pageOffset; i <= pageEnd; i++ {
			alloc += proto.PageSize
		}
	}
	return
}

func (cb *CacheBlock) InitOnce(engine *CacheEngine, sources []*proto.DataSource) {
	defer func() {
		if r := recover(); r != nil {
			warnMsg := fmt.Sprintf("cache block init occurred panic:%v", r)
			log.LogErrorf(warnMsg)
			exporter.Warning(warnMsg)
		}
	}()
	cb.initOnce.Do(func() {
		cb.Init(sources)
	})
	if atomic.LoadInt32(&cb.cacheStat) == CacheClose {
		engine.deleteCacheBlock(cb.blockKey)
	}
}

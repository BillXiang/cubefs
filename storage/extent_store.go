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

package storage

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"hash/crc32"
	"io"
	"sort"
	"strings"
	"syscall"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/log"
	"github.com/cubefs/cubefs/util/unit"
)

const (
	ExtCrcHeaderFileName     = "EXTENT_CRC"
	ExtBaseExtentIDFileName  = "EXTENT_META"
	TinyDeleteFileOpt        = os.O_CREATE | os.O_RDWR | os.O_APPEND
	TinyExtDeletedFileName   = "TINYEXTENT_DELETE"
	NormalExtDeletedFileName = "NORMALEXTENT_DELETE"
	MaxExtentCount           = 20000
	MinExtentID              = 1024
	DeleteTinyRecordSize     = 24
	UpdateCrcInterval        = 600
	RepairInterval           = 10
	RandomWriteType          = 2
	AppendWriteType          = 1
	BaseExtentIDPersistStep  = 500
	BaseExtentIDSyncStep     = 400

	LoadInProgress int32 = 0
	LoadFinish     int32 = 1
)

var (
	RegexpExtentFile, _ = regexp.Compile("^(\\d)+$")
	SnapShotFilePool    = &sync.Pool{New: func() interface{} {
		return new(proto.File)
	}}
	ValidateCrcInterval = int64(20 * RepairInterval)
)

var (
	PartitionIsLoaddingErr = fmt.Errorf("partition is loadding")
)

func GetSnapShotFileFromPool() (f *proto.File) {
	f = SnapShotFilePool.Get().(*proto.File)
	return
}

func PutSnapShotFileToPool(f *proto.File) {
	SnapShotFilePool.Put(f)
}

type ExtentFilter func(info *ExtentInfoBlock) bool

// Filters
var (
	NormalExtentFilter = func() ExtentFilter {
		now := time.Now()
		return func(ei *ExtentInfoBlock) bool {
			return !proto.IsTinyExtent(ei[FileID]) && now.Unix()-int64(ei[ModifyTime]) > RepairInterval && ei[Size] > 0
		}
	}

	TinyExtentFilter = func(filters []uint64) ExtentFilter {
		return func(ei *ExtentInfoBlock) bool {
			if !proto.IsTinyExtent(ei[FileID]) {
				return false
			}
			for _, filterID := range filters {
				if filterID == ei[FileID] {
					return true
				}
			}
			return false
		}
	}

	ExtentFilterForValidateCRC = func() ExtentFilter {
		now := time.Now()
		return func(ei *ExtentInfoBlock) bool {
			return now.Unix()-int64(ei[ModifyTime]) > ValidateCrcInterval && ei[Size] > 0
		}
	}
)

type IOType int

const (
	IOWrite IOType = iota
	IORead
)

type IOInterceptor func(io IOType, do func())

func (i IOInterceptor) intercept(io IOType, do func()) {
	if i != nil {
		i(io, do)
		return
	}
	do()
	return
}

// ExtentStore defines fields used in the storage engine.
// Packets smaller than 128K are stored in the "tinyExtent", a place to persist the small files.
// packets larger than or equal to 128K are stored in the normal "extent", a place to persist large files.
// The difference between them is that the extentID of a tinyExtent starts at 5000000 and ends at 5000128.
// Multiple small files can be appended to the same tinyExtent.
// In addition, the deletion of small files is implemented by the punch hole from the underlying file system.
type ExtentStore struct {
	dataPath                 string
	baseExtentID             uint64 // TODO what is baseExtentID
	baseExtentIDPersistCount uint64
	//extentInfoMap                     sync.Map // map that stores all the extent information
	infoStore                         *ExtentInfoStore
	extentCnt                         int64
	cache                             *ExtentCache // extent cache
	mutex                             sync.Mutex
	storeSize                         int      // size of the extent store
	metadataFp                        *os.File // metadata file pointer?
	tinyExtentDeleteFp                *os.File
	normalExtentDeleteFp              *os.File
	closeC                            chan bool
	closed                            bool
	availableTinyExtentC              chan uint64 // available tinyExtent channel
	availableTinyExtentMap            sync.Map
	availableTinyExtentMutex          sync.Mutex
	brokenTinyExtentC                 chan uint64 // broken tinyExtent channel
	brokenTinyExtentMap               sync.Map
	brokenTinyExtentMutex             sync.Mutex
	blockSize                         int
	partitionID                       uint64
	verifyExtentFp                    *os.File
	hasAllocSpaceExtentIDOnVerfiyFile uint64
	loadStatus                        int32
	loadMux                           sync.Mutex
	normalExtentDeleteMap             sync.Map

	ioInterceptor IOInterceptor
}

func MkdirAll(name string) (err error) {
	return os.MkdirAll(name, 0755)
}

func NewExtentStore(dataDir string, partitionID uint64, storeSize int,
	cacheCapacity int, ln CacheListener, isCreatePartition bool, ioi IOInterceptor) (s *ExtentStore, err error) {
	s = new(ExtentStore)
	s.dataPath = dataDir
	s.partitionID = partitionID
	s.infoStore = NewExtentInfoStore(partitionID)
	s.ioInterceptor = ioi
	if err = MkdirAll(dataDir); err != nil {
		return nil, fmt.Errorf("NewExtentStore [%v] err[%v]", dataDir, err)
	}
	if s.tinyExtentDeleteFp, err = os.OpenFile(path.Join(s.dataPath, TinyExtDeletedFileName), TinyDeleteFileOpt, 0666); err != nil {
		return
	}
	stat, err := s.tinyExtentDeleteFp.Stat()
	if err != nil {
		return
	}
	if stat.Size()%DeleteTinyRecordSize != 0 {
		needWriteEmpty := DeleteTinyRecordSize - (stat.Size() % DeleteTinyRecordSize)
		data := make([]byte, needWriteEmpty)
		s.tinyExtentDeleteFp.Write(data)
	}
	if s.verifyExtentFp, err = os.OpenFile(path.Join(s.dataPath, ExtCrcHeaderFileName), os.O_CREATE|os.O_RDWR, 0666); err != nil {
		return
	}
	if s.metadataFp, err = os.OpenFile(path.Join(s.dataPath, ExtBaseExtentIDFileName), os.O_CREATE|os.O_RDWR, 0666); err != nil {
		return
	}
	if s.normalExtentDeleteFp, err = os.OpenFile(path.Join(s.dataPath, NormalExtDeletedFileName), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0666); err != nil {
		return
	}

	s.cache = NewExtentCache(cacheCapacity, time.Minute*5, ln)
	if err = s.initBaseFileID(); err != nil {
		err = fmt.Errorf("init base field ID: %v", err)
		return
	}
	s.hasAllocSpaceExtentIDOnVerfiyFile = s.GetPreAllocSpaceExtentIDOnVerfiyFile()
	s.storeSize = storeSize
	s.closeC = make(chan bool, 1)
	s.closed = false
	err = s.initTinyExtent()
	if err != nil {
		return
	}
	if isCreatePartition {
		atomic.StoreInt32(&s.loadStatus, LoadFinish)
	} else {
		atomic.StoreInt32(&s.loadStatus, LoadInProgress)
	}
	return
}

func (s *ExtentStore) WalkExtentsInfo(f func(info *ExtentInfoBlock)) {
	s.infoStore.Range(func(extentID uint64, ei *ExtentInfoBlock) {
		f(ei)
	})
}

// SnapShot returns the information of all the extents on the current data partition.
// When the master sends the loadDataPartition request, the snapshot is used to compare the replicas.
func (s *ExtentStore) SnapShot() (files []*proto.File, err error) {
	var (
		normalExtentSnapshot, tinyExtentSnapshot []ExtentInfoBlock
	)

	if normalExtentSnapshot, err = s.GetAllWatermarks(proto.NormalExtentType, NormalExtentFilter()); err != nil {
		return
	}

	files = make([]*proto.File, 0, len(normalExtentSnapshot))
	for _, ei := range normalExtentSnapshot {
		file := GetSnapShotFileFromPool()
		file.Name = strconv.FormatUint(ei[FileID], 10)
		file.Size = uint32(ei[Size])
		file.Modified = int64(ei[ModifyTime])
		file.Crc = uint32(atomic.LoadUint64(&ei[Crc]))
		files = append(files, file)
	}
	tinyExtentSnapshot = s.getTinyExtentInfo()
	for _, ei := range tinyExtentSnapshot {
		file := GetSnapShotFileFromPool()
		file.Name = strconv.FormatUint(ei[FileID], 10)
		file.Size = uint32(ei[Size])
		file.Modified = int64(ei[ModifyTime])
		file.Crc = 0
		files = append(files, file)
	}

	return
}

// Create creates an extent.
func (s *ExtentStore) Create(extentID uint64, putCache bool) (err error) {
	var e *Extent
	name := path.Join(s.dataPath, strconv.Itoa(int(extentID)))
	if s.HasExtent(extentID) {
		err = ExtentExistsError
		return err
	}
	e = NewExtent(name, extentID, s.ioInterceptor)
	e.header = make([]byte, unit.BlockHeaderSize)
	err = e.InitToFS()
	if err != nil {
		return err
	}
	if putCache {
		s.cache.Put(e)
	} else {
		defer func() {
			_ = e.Close(false)
		}()
	}
	s.infoStore.Create(extentID)
	s.infoStore.Update(extentID, 0, uint64(time.Now().Unix()), 0)
	atomic.AddInt64(&s.extentCnt, 1)
	_ = s.UpdateBaseExtentID(extentID)
	return
}

const (
	BaseExtentAddNumOnInitExtentStore = 1000
)

func (s *ExtentStore) initBaseFileID() (err error) {
	var (
		baseFileID uint64
	)
	baseFileID, _ = s.GetPersistenceBaseExtentID()
	dirFd, err := os.Open(s.dataPath)
	if err != nil {
		return
	}
	defer func() {
		dirFd.Close()
	}()
	names, err := dirFd.Readdirnames(-1)
	var (
		extentID uint64
		isExtent bool
	)
	for _, name := range names {
		if extentID, isExtent = s.ExtentID(name); !isExtent {
			continue
		}
		s.infoStore.Create(extentID)
		if proto.IsTinyExtent(extentID) {
			name := path.Join(s.dataPath, strconv.FormatUint(extentID, 10))
			info, statErr := os.Stat(name)
			if statErr != nil {
				statErr = fmt.Errorf("restore from file %v system: %v", name, statErr)
				log.LogWarnf(statErr.Error())
				continue
			}
			watermark := info.Size()
			if watermark%PageSize != 0 {
				watermark = watermark + (PageSize - watermark%PageSize)
			}
			s.infoStore.Update(extentID, uint64(watermark), uint64(info.ModTime().Unix()), 0)
		}
		atomic.AddInt64(&s.extentCnt, 1)
		if !proto.IsTinyExtent(extentID) && extentID > baseFileID {
			baseFileID = extentID
		}
	}
	if baseFileID < MinExtentID {
		baseFileID = MinExtentID
	}
	baseFileID += BaseExtentAddNumOnInitExtentStore
	atomic.StoreUint64(&s.baseExtentID, baseFileID)
	if err = s.PersistenceBaseExtentID(baseFileID); err != nil {
		return
	}
	log.LogInfof("datadir(%v) maxBaseId(%v)", s.dataPath, baseFileID)
	return nil
}

// Load 加载存储引擎剩余未加载的必要信息.
func (s *ExtentStore) Load() {

	s.loadMux.Lock()
	defer s.loadMux.Unlock()

	if atomic.LoadInt32(&s.loadStatus) == LoadFinish {
		return
	}

	s.infoStore.Range(func(extentID uint64, ei *ExtentInfoBlock) {
		if proto.IsTinyExtent(extentID) {
			return
		}
		extentAbsPath := path.Join(s.dataPath, strconv.FormatUint(extentID, 10))
		info, err := os.Stat(extentAbsPath)
		if err != nil {
			log.LogWarnf("asyncLoadExtentSize extentPath(%v) error(%v)", extentAbsPath, err)
			return
		}
		s.infoStore.Update(extentID, uint64(info.Size()), uint64(info.ModTime().Unix()), 0)
	})

	// Mark the load progress of this extent store has been finished.
	atomic.StoreInt32(&s.loadStatus, LoadFinish)
}

func (s *ExtentStore) IsFinishLoad() bool {
	return atomic.LoadInt32(&s.loadStatus) == LoadFinish
}

func (s *ExtentStore) getExtentInfoByExtentID(eid uint64) (ei *ExtentInfoBlock, ok bool) {
	ei, ok = s.infoStore.Load(eid)
	return
}

// Write writes the given extent to the disk.
func (s *ExtentStore) Write(ctx context.Context, extentID uint64, offset, size int64, data []byte, crc uint32, writeType int, isSync bool) (err error) {
	var (
		e *Extent
	)

	ei, ok := s.getExtentInfoByExtentID(extentID)
	if !ok {
		err = proto.ExtentNotFoundError
		return
	}
	e, err = s.ExtentWithHeader(ei)
	if err != nil {
		return err
	}
	if err = s.checkOffsetAndSize(extentID, offset, size); err != nil {
		return err
	}
	err = e.Write(data, offset, size, crc, writeType, isSync, s.PersistenceBlockCrc, ei)
	if err != nil {
		return err
	}
	s.infoStore.UpdateInfoFromExtent(e, 0)
	return nil
}

func (s *ExtentStore) checkOffsetAndSize(extentID uint64, offset, size int64) error {
	if proto.IsTinyExtent(extentID) {
		return nil
	}
	if offset+size > unit.BlockSize*unit.BlockCount {
		return NewParameterMismatchErr(fmt.Sprintf("offset=%v size=%v", offset, size))
	}
	if offset >= unit.BlockCount*unit.BlockSize || size == 0 {
		return NewParameterMismatchErr(fmt.Sprintf("offset=%v size=%v", offset, size))
	}

	//if size > unit.BlockSize {
	//	return NewParameterMismatchErr(fmt.Sprintf("offset=%v size=%v", offset, size))
	//}
	return nil
}

// Read reads the extent based on the given id.
func (s *ExtentStore) Read(extentID uint64, offset, size int64, nbuf []byte, isRepairRead bool) (crc uint32, err error) {
	var e *Extent
	ei, _ := s.getExtentInfoByExtentID(extentID)
	if e, err = s.ExtentWithHeader(ei); err != nil {
		return
	}
	if err = s.checkOffsetAndSize(extentID, offset, size); err != nil {
		return
	}
	crc, err = e.Read(nbuf, offset, size, isRepairRead)

	return
}

func (s *ExtentStore) tinyDelete(e *Extent, offset, size int64) (err error) {
	if offset+size > e.dataSize {
		return
	}
	var (
		hasDelete bool
	)
	if hasDelete, err = e.DeleteTiny(offset, size); err != nil {
		return
	}
	if hasDelete {
		return
	}
	if err = s.RecordTinyDelete(e.extentID, offset, size); err != nil {
		return
	}
	return
}

// MarkDelete marks the given extent as deleted.
func (s *ExtentStore) MarkDelete(extentID uint64, offset, size int64) (err error) {
	ei, ok := s.getExtentInfoByExtentID(extentID)
	if !ok {
		err = proto.ExtentNotFoundError
		return
	}
	if proto.IsTinyExtent(extentID) {
		var e *Extent
		if e, err = s.ExtentWithHeader(ei); err != nil {
			return
		}
		return s.tinyDelete(e, offset, size)
	}
	s.cache.Del(extentID)
	extentFilePath := path.Join(s.dataPath, strconv.FormatUint(extentID, 10))
	if err = os.Remove(extentFilePath); err != nil {
		return
	}
	s.PersistenceHasDeleteExtent(extentID)
	s.infoStore.Delete(extentID)
	s.DeleteBlockCrc(extentID)
	s.normalExtentDeleteMap.Store(extentID, time.Now().Unix())
	atomic.AddInt64(&s.extentCnt, -1)
	return
}

// Close closes the extent store.
func (s *ExtentStore) Close() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.closed {
		return
	}

	// Release cache
	s.cache.Flush()
	s.cache.Close()
	s.tinyExtentDeleteFp.Sync()
	s.tinyExtentDeleteFp.Close()
	s.normalExtentDeleteFp.Sync()
	s.normalExtentDeleteFp.Close()
	s.verifyExtentFp.Sync()
	s.verifyExtentFp.Close()
	s.closed = true
	close(s.closeC)
}

// Watermark returns the extent info of the given extent on the record.
func (s *ExtentStore) Watermark(extentID uint64) (ei *ExtentInfoBlock, err error) {
	if !proto.IsTinyExtent(extentID) && !s.IsFinishLoad() {
		err = PartitionIsLoaddingErr
		return
	}
	ei, ok := s.getExtentInfoByExtentID(extentID)
	if !ok || ei == nil {
		err = fmt.Errorf("e %v not exist", s.getExtentKey(extentID))
		return
	}
	return
}

// Watermark returns the extent info of the given extent on the record.
func (s *ExtentStore) ForceWatermark(extentID uint64) (ei *ExtentInfoBlock, err error) {
	ei, ok := s.getExtentInfoByExtentID(extentID)
	if !ok || ei == nil {
		err = fmt.Errorf("e %v not exist", s.getExtentKey(extentID))
		return
	}
	return
}

// GetTinyExtentOffset returns the offset of the given extent.
func (s *ExtentStore) GetTinyExtentOffset(extentID uint64) (watermark int64, err error) {
	einfo, err := s.Watermark(extentID)
	if err != nil {
		return
	}
	watermark = int64(einfo[Size])
	if watermark%PageSize != 0 {
		watermark = watermark + (PageSize - watermark%PageSize)
	}

	return
}

// GetAllWatermarks returns all the watermarks.
func (s *ExtentStore) GetAllWatermarks(extentType uint8, filter ExtentFilter) (extents []ExtentInfoBlock, err error) {
	extents = make([]ExtentInfoBlock, 0)
	s.infoStore.RangeDist(extentType, func(extentID uint64, ei *ExtentInfoBlock) {
		if filter != nil && !filter(ei) {
			return
		}
		extents = append(extents, *ei)
	})
	return
}

func (s *ExtentStore) GetAllWatermarksWithByteArr(extentType uint8, filter ExtentFilter) (tinyDeleteFileSize int64, data []byte, err error) {
	needSize := 0
	extents := make([]uint64, 0)
	s.infoStore.RangeDist(extentType, func(extentID uint64, ei *ExtentInfoBlock) {
		if filter != nil && !filter(ei) {
			return
		}
		needSize += 16

		extents = append(extents, ei[FileID])
		return
	})
	data = make([]byte, needSize)
	index := 0

	for _, eid := range extents {
		ei, ok := s.infoStore.Load(eid)
		if !ok {
			continue
		}
		binary.BigEndian.PutUint64(data[index:index+8], ei[FileID])
		index += 8
		binary.BigEndian.PutUint64(data[index:index+8], ei[Size])
		index += 8
	}
	data = data[:index]
	tinyDeleteFileSize, err = s.LoadTinyDeleteFileOffset()

	return
}

func (s *ExtentStore) GetAllExtentInfoWithByteArr(filter ExtentFilter) (data []byte, err error) {
	needSize := 0
	extents := make([]uint64, 0)
	s.infoStore.RangeDist(proto.AllExtentType, func(extentID uint64, ei *ExtentInfoBlock) {
		if filter != nil && !filter(ei) {
			return
		}
		needSize += 20
		extents = append(extents, ei[FileID])
		return
	})

	data = make([]byte, needSize)
	index := 0
	for _, eid := range extents {
		ei, ok := s.infoStore.Load(eid)
		if !ok {
			continue
		}
		binary.BigEndian.PutUint64(data[index:index+8], ei[FileID])
		index += 8
		binary.BigEndian.PutUint64(data[index:index+8], ei[Size])
		index += 8
		binary.BigEndian.PutUint32(data[index:index+4], uint32(ei[Crc]))
		index += 4
	}
	data = data[:index]
	return
}

func (s *ExtentStore) getTinyExtentInfo() (extents []ExtentInfoBlock) {
	extents = make([]ExtentInfoBlock, 0)
	s.infoStore.RangeTinyExtent(func(_ uint64, ei *ExtentInfoBlock) {
		extents = append(extents, *ei)
	})
	return
}

// ExtentID return the extent ID.
func (s *ExtentStore) ExtentID(filename string) (extentID uint64, isExtent bool) {
	if isExtent = RegexpExtentFile.MatchString(filename); !isExtent {
		return
	}
	var (
		err error
	)
	if extentID, err = strconv.ParseUint(filename, 10, 64); err != nil {
		isExtent = false
		return
	}
	isExtent = true
	return
}

func (s *ExtentStore) initTinyExtent() (err error) {
	s.availableTinyExtentC = make(chan uint64, proto.TinyExtentCount)
	s.brokenTinyExtentC = make(chan uint64, proto.TinyExtentCount)
	var extentID uint64

	for extentID = proto.TinyExtentStartID; extentID < proto.TinyExtentStartID+proto.TinyExtentCount; extentID++ {
		err = s.Create(extentID, false)
		if err == nil || strings.Contains(err.Error(), syscall.EEXIST.Error()) || err == ExtentExistsError {
			err = nil
			s.brokenTinyExtentC <- extentID
			s.brokenTinyExtentMap.Store(extentID, true)
			continue
		}
		return err
	}

	return
}

// GetAvailableTinyExtent returns the available tiny extent from the channel.
func (s *ExtentStore) GetAvailableTinyExtent() (extentID uint64, err error) {
	s.availableTinyExtentMutex.Lock()
	defer s.availableTinyExtentMutex.Unlock()
	select {
	case extentID = <-s.availableTinyExtentC:
		s.availableTinyExtentMap.Delete(extentID)
		return
	default:
		return 0, NoAvailableExtentError

	}
}

// SendToAvailableTinyExtentC sends the extent to the channel that stores the available tiny extents.
func (s *ExtentStore) SendToAvailableTinyExtentC(extentID uint64) {
	s.availableTinyExtentMutex.Lock()
	defer s.availableTinyExtentMutex.Unlock()
	if _, ok := s.availableTinyExtentMap.Load(extentID); !ok {
		s.availableTinyExtentC <- extentID
		s.availableTinyExtentMap.Store(extentID, true)
	}
}

// AvailableTinyExtentCnt returns the count of the available tiny extents.
func (s *ExtentStore) AvailableTinyExtentCnt() int {
	return len(s.availableTinyExtentC)
}

// BrokenTinyExtentCnt returns the count of the broken tiny extents.
func (s *ExtentStore) BrokenTinyExtentCnt() int {
	return len(s.brokenTinyExtentC)
}

// MoveAllToBrokenTinyExtentC moves all the tiny extents to the channel stores the broken extents.
func (s *ExtentStore) MoveAllToBrokenTinyExtentC(cnt int) {
	for i := 0; i < cnt; i++ {
		extentID, err := s.GetAvailableTinyExtent()
		if err != nil {
			return
		}
		s.SendToBrokenTinyExtentC(extentID)
	}
}

// SendToBrokenTinyExtentC sends the given extent id to the channel.
func (s *ExtentStore) SendToBrokenTinyExtentC(extentID uint64) {
	s.brokenTinyExtentMutex.Lock()
	defer s.brokenTinyExtentMutex.Unlock()
	if _, ok := s.brokenTinyExtentMap.Load(extentID); !ok {
		s.brokenTinyExtentC <- extentID
		s.brokenTinyExtentMap.Store(extentID, true)
	}
}

// SendAllToBrokenTinyExtentC sends all the extents to the channel that stores the broken extents.
func (s *ExtentStore) SendAllToBrokenTinyExtentC(extentIds []uint64) {
	s.brokenTinyExtentMutex.Lock()
	defer s.brokenTinyExtentMutex.Unlock()
	for _, extentID := range extentIds {
		if _, ok := s.brokenTinyExtentMap.Load(extentID); !ok {
			s.brokenTinyExtentC <- extentID
			s.brokenTinyExtentMap.Store(extentID, true)
		}
	}
}

// GetBrokenTinyExtent returns the first broken extent in the channel.
func (s *ExtentStore) GetBrokenTinyExtent() (extentID uint64, err error) {
	s.brokenTinyExtentMutex.Lock()
	defer s.brokenTinyExtentMutex.Unlock()
	select {
	case extentID = <-s.brokenTinyExtentC:
		s.brokenTinyExtentMap.Delete(extentID)
		return
	default:
		return 0, NoBrokenExtentError

	}
}

// StoreSizeExtentID returns the size of the extent store
func (s *ExtentStore) StoreSizeExtentID(maxExtentID uint64) (totalSize uint64) {
	s.infoStore.Range(func(extentID uint64, ei *ExtentInfoBlock) {
		if extentID <= maxExtentID {
			totalSize += ei[Size]
		}
		return
	})

	return totalSize
}

// StoreSizeExtentID returns the size of the extent store
func (s *ExtentStore) GetMaxExtentIDAndPartitionSize() (maxExtentID, totalSize uint64) {
	s.infoStore.Range(func(extentID uint64, ei *ExtentInfoBlock) {
		if ei[FileID] > maxExtentID {
			maxExtentID = ei[FileID]
		}
		totalSize += ei[Size]
		return
	})
	return maxExtentID, totalSize
}

func MarshalTinyExtent(extentID uint64, offset, size int64) (data []byte) {
	data = make([]byte, DeleteTinyRecordSize)
	binary.BigEndian.PutUint64(data[0:8], extentID)
	binary.BigEndian.PutUint64(data[8:16], uint64(offset))
	binary.BigEndian.PutUint64(data[16:DeleteTinyRecordSize], uint64(size))
	return data
}

func UnMarshalTinyExtent(data []byte) (extentID, offset, size uint64) {
	extentID = binary.BigEndian.Uint64(data[0:8])
	offset = binary.BigEndian.Uint64(data[8:16])
	size = binary.BigEndian.Uint64(data[16:DeleteTinyRecordSize])
	return
}

func (s *ExtentStore) RecordTinyDelete(extentID uint64, offset, size int64) (err error) {
	record := MarshalTinyExtent(extentID, offset, size)
	stat, err := s.tinyExtentDeleteFp.Stat()
	if err != nil {
		return
	}
	if stat.Size()%DeleteTinyRecordSize != 0 {
		needWriteEmpty := DeleteTinyRecordSize - (stat.Size() % DeleteTinyRecordSize)
		data := make([]byte, needWriteEmpty)
		s.tinyExtentDeleteFp.Write(data)
	}
	_, err = s.tinyExtentDeleteFp.Write(record)
	if err != nil {
		return
	}

	return
}

func (s *ExtentStore) ReadTinyDeleteRecords(offset, size int64, data []byte) (crc uint32, err error) {
	_, err = s.tinyExtentDeleteFp.ReadAt(data[:size], offset)
	if err == nil || err == io.EOF {
		err = nil
		crc = crc32.ChecksumIEEE(data[:size])
	}
	return
}

func (s *ExtentStore) CheckIsAvaliRandomWrite(extentID uint64, offset, size int64) (err error) {
	var e *Extent
	e, err = s.extentWithHeaderByExtentID(extentID)
	if err != nil {
		return
	}
	err = e.checkWriteParameter(offset, size, RandomWriteType)
	return
}

func (s *ExtentStore) LoadExtentWaterMark(extentID uint64) (size int64, err error) {
	var e *Extent
	e, err = s.extentWithHeaderByExtentID(extentID)
	if err != nil {
		return
	}
	return e.Size(), nil
}

// NextExtentID returns the next extentID. When the client sends the request to create an extent,
// this function generates an unique extentID within the current partition.
// This function can only be called by the leader.
func (s *ExtentStore) NextExtentID() (extentID uint64, err error) {
	extentID = atomic.AddUint64(&s.baseExtentID, 1)
	err = s.PersistenceBaseExtentID(extentID)
	return
}

func (s *ExtentStore) LoadTinyDeleteFileOffset() (offset int64, err error) {
	stat, err := s.tinyExtentDeleteFp.Stat()
	if err == nil {
		offset = stat.Size()
	}
	return
}

func (s *ExtentStore) getExtentKey(extent uint64) string {
	return fmt.Sprintf("extent %v_%v", s.partitionID, extent)
}

// UpdateBaseExtentID updates the base extent ID.
func (s *ExtentStore) UpdateBaseExtentID(id uint64) (err error) {
	if proto.IsTinyExtent(id) {
		return
	}
	if id > atomic.LoadUint64(&s.baseExtentID) {
		atomic.StoreUint64(&s.baseExtentID, id)
		err = s.PersistenceBaseExtentID(atomic.LoadUint64(&s.baseExtentID))
	}
	s.PreAllocSpaceOnVerfiyFile(atomic.LoadUint64(&s.baseExtentID))

	return
}

func (s *ExtentStore) extent(extentID uint64) (e *Extent, err error) {
	if e, err = s.loadExtentFromDisk(extentID, false); err != nil {
		err = fmt.Errorf("load extent from disk: %v", err)
		return nil, err
	}
	return
}

func (s *ExtentStore) ExtentWithHeader(ei *ExtentInfoBlock) (e *Extent, err error) {
	var ok bool
	if ei == nil {
		err = proto.ExtentNotFoundError
		return
	}
	if e, ok = s.cache.Get(ei[FileID]); !ok {
		if e, err = s.loadExtentFromDisk(ei[FileID], true); err != nil {
			err = fmt.Errorf("load  %v from disk: %v", s.getExtentKey(ei[FileID]), err)
			return nil, err
		}
	}
	return
}

func (s *ExtentStore) extentWithHeaderByExtentID(extentID uint64) (e *Extent, err error) {
	var ok bool
	if e, ok = s.cache.Get(extentID); !ok {
		if e, err = s.loadExtentFromDisk(extentID, true); err != nil {
			err = fmt.Errorf("load  %v from disk: %v", s.getExtentKey(extentID), err)
			return nil, err
		}
	}
	return
}

// HasExtent tells if the extent store has the extent with the given ID
func (s *ExtentStore) HasExtent(extentID uint64) (exist bool) {
	_, ok := s.getExtentInfoByExtentID(extentID)
	return ok
}

// IsRecentDelete tells if the normal extent is deleted recently, true-it must has been deleted, false-it may not be deleted
func (s *ExtentStore) IsRecentDelete(extentID uint64) (deleted bool) {
	_, ok := s.normalExtentDeleteMap.Load(extentID)
	return ok
}

// GetExtentCount returns the number of extents in the extentInfoMap
func (s *ExtentStore) GetExtentCount() (count int) {
	return int(atomic.LoadInt64(&s.extentCnt))
}

func (s *ExtentStore) loadExtentFromDisk(extentID uint64, putCache bool) (e *Extent, err error) {
	name := path.Join(s.dataPath, strconv.Itoa(int(extentID)))
	e = NewExtent(name, extentID, s.ioInterceptor)
	if err = e.RestoreFromFS(); err != nil {
		err = fmt.Errorf("restore from file %v putCache %v system: %v", name, putCache, err)
		return
	}
	if !putCache {
		return
	}
	if !proto.IsTinyExtent(extentID) {
		e.header = make([]byte, unit.BlockHeaderSize)
		if _, err = s.verifyExtentFp.ReadAt(e.header, int64(extentID*unit.BlockHeaderSize)); err != nil && err != io.EOF {
			return
		}
	}
	err = nil
	s.cache.Put(e)

	return
}

func (s *ExtentStore) ScanBlocks(extentID uint64) (bcs []*BlockCrc, err error) {
	var blockCnt int
	bcs = make([]*BlockCrc, 0)
	ei, _ := s.getExtentInfoByExtentID(extentID)
	e, err := s.ExtentWithHeader(ei)
	if err != nil {
		return bcs, err
	}
	blockCnt = int(e.Size() / unit.BlockSize)
	if e.Size()%unit.BlockSize != 0 {
		blockCnt += 1
	}
	for blockNo := 0; blockNo < blockCnt; blockNo++ {
		blockCrc := binary.BigEndian.Uint32(e.header[blockNo*unit.PerBlockCrcSize : (blockNo+1)*unit.PerBlockCrcSize])
		bcs = append(bcs, &BlockCrc{BlockNo: blockNo, Crc: blockCrc})
	}
	sort.Sort(BlockCrcArr(bcs))

	return
}

type ExtentInfoArr []*ExtentInfoBlock

func (arr ExtentInfoArr) Len() int           { return len(arr) }
func (arr ExtentInfoArr) Less(i, j int) bool { return arr[i][FileID] < arr[j][FileID] }
func (arr ExtentInfoArr) Swap(i, j int)      { arr[i], arr[j] = arr[j], arr[i] }

func (s *ExtentStore) AutoComputeExtentCrc() {
	defer func() {
		if r := recover(); r != nil {
			return
		}
	}()

	needUpdateCrcExtents := make([]*ExtentInfoBlock, 0)
	s.infoStore.Range(func(extentID uint64, ei *ExtentInfoBlock) {
		if !proto.IsTinyExtent(ei[FileID]) && time.Now().Unix()-int64(ei[ModifyTime]) > UpdateCrcInterval &&
			ei[Size] > 0 && ei[Crc] == 0 {
			needUpdateCrcExtents = append(needUpdateCrcExtents, ei)
		}
		return
	})
	sort.Sort(ExtentInfoArr(needUpdateCrcExtents))
	for _, ei := range needUpdateCrcExtents {
		e, err := s.ExtentWithHeader(ei)
		extentCrc, err := e.autoComputeExtentCrc(s.PersistenceBlockCrc)
		if err != nil {
			continue
		}
		s.infoStore.UpdateInfoFromExtent(e, extentCrc)
		time.Sleep(time.Millisecond * 100)
	}
}

func (s *ExtentStore) TinyExtentRecover(extentID uint64, offset, size int64, data []byte, crc uint32, isEmptyPacket bool) (err error) {
	if !proto.IsTinyExtent(extentID) {
		return fmt.Errorf("extent %v not tinyExtent", extentID)
	}

	var (
		e  *Extent
		ei *ExtentInfoBlock
	)
	ei, _ = s.getExtentInfoByExtentID(extentID)
	if e, err = s.ExtentWithHeader(ei); err != nil {
		return nil
	}

	if err = e.TinyExtentRecover(data, offset, size, crc, isEmptyPacket); err != nil {
		return err
	}
	s.infoStore.UpdateInfoFromExtent(e, 0)

	return nil
}

func (s *ExtentStore) TinyExtentGetFinfoSize(extentID uint64) (size uint64, err error) {
	var (
		e *Extent
	)
	if !proto.IsTinyExtent(extentID) {
		return 0, fmt.Errorf("unavali extent id (%v)", extentID)
	}
	ei, _ := s.getExtentInfoByExtentID(extentID)
	if e, err = s.ExtentWithHeader(ei); err != nil {
		return
	}

	finfo, err := e.file.Stat()
	if err != nil {
		return 0, err
	}
	size = uint64(finfo.Size())

	return
}

func (s *ExtentStore) ComputeMd5Sum(extentID, offset, size uint64) (md5Sum string, err error) {
	extentIDAbsPath := path.Join(s.dataPath, strconv.FormatUint(extentID, 10))
	fp, err := os.Open(extentIDAbsPath)
	if err != nil {
		err = fmt.Errorf("open %v error %v", extentIDAbsPath, err)
		return
	}
	defer func() {
		fp.Close()
	}()
	md5Writer := md5.New()
	stat, err := fp.Stat()
	if err != nil {
		err = fmt.Errorf("stat %v error %v", extentIDAbsPath, err)
		return
	}
	if size == 0 {
		size = uint64(stat.Size())
	}
	if offset != 0 {
		_, err = fp.Seek(int64(offset), 0)
		if err != nil {
			err = fmt.Errorf("seek %v error %v", extentIDAbsPath, err)
			return
		}
	}
	_, err = io.CopyN(md5Writer, fp, int64(size))
	if err != nil {
		err = fmt.Errorf("ioCopy %v error %v", extentIDAbsPath, err)
		return
	}
	md5Sum = hex.EncodeToString(md5Writer.Sum(nil))

	return
}

func (s *ExtentStore) TinyExtentAvaliOffset(extentID uint64, offset int64) (newOffset, newEnd int64, err error) {
	var e *Extent
	if !proto.IsTinyExtent(extentID) {
		return 0, 0, fmt.Errorf("unavali extent(%v)", extentID)
	}
	ei, _ := s.getExtentInfoByExtentID(extentID)
	if e, err = s.ExtentWithHeader(ei); err != nil {
		return
	}

	defer func() {
		if err != nil && strings.Contains(err.Error(), syscall.ENXIO.Error()) {
			newOffset = e.dataSize
			newEnd = e.dataSize
			err = nil
		}

	}()
	newOffset, newEnd, err = e.tinyExtentAvaliOffset(offset)

	return
}

const (
	DiskSectorSize = 512
)

func (s *ExtentStore) GetStoreUsedSize() (used int64) {
	used = int64(s.infoStore.NormalUsed())
	for eid := proto.TinyExtentStartID; eid < proto.TinyExtentStartID+proto.TinyExtentCount; eid++ {
		stat := new(syscall.Stat_t)
		err := syscall.Stat(fmt.Sprintf("%v/%v", s.dataPath, eid), stat)
		if err != nil {
			continue
		}
		used += stat.Blocks * DiskSectorSize
	}
	return
}

func (s *ExtentStore) EvictExpiredCache() {
	s.cache.EvictExpired()
}

func (s *ExtentStore) ForceEvictCache(ratio Ratio) {
	s.cache.ForceEvict(ratio)
}

// Flush 强制下刷存储引擎当前所有打开的FD，保证这些FD的数据在内核PageCache里的脏页全部回写.
func (s *ExtentStore) Flush() (cnt int) {
	return s.cache.Flush()
}

func (s *ExtentStore) EvictExpiredNormalExtentDeleteCache(expireTime int64) {
	var count int
	s.normalExtentDeleteMap.Range(func(key, value interface{}) bool {
		timeDelete := value.(int64)
		if timeDelete < time.Now().Unix()-expireTime {
			s.normalExtentDeleteMap.Delete(key)
			count++
		}
		return true
	})
	log.LogDebugf("action[EvictExpiredNormalExtentDeleteCache] Partition(%d) (%d) extent delete cache has been evicted.", s.partitionID, count)
}

func (s *ExtentStore) PlaybackTinyDelete() (err error) {
	var (
		recordFileInfo os.FileInfo
		recordData           = make([]byte, DeleteTinyRecordSize)
		readOff        int64 = 0
		readN                = 0
	)
	if recordFileInfo, err = s.tinyExtentDeleteFp.Stat(); err != nil {
		return
	}
	for readOff = 0; readOff < recordFileInfo.Size(); readOff += DeleteTinyRecordSize {
		readN, err = s.tinyExtentDeleteFp.ReadAt(recordData, readOff)
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}
		if readN != DeleteTinyRecordSize {
			return
		}
		extentID, offset, size := UnMarshalTinyExtent(recordData[:readN])
		ei, ok := s.getExtentInfoByExtentID(extentID)
		if !ok {
			continue
		}
		var e *Extent
		if e, err = s.ExtentWithHeader(ei); err != nil {
			return
		}
		if _, err = e.DeleteTiny(int64(offset), int64(size)); err != nil {
			return
		}
	}
	return
}

func (s *ExtentStore) GetRealBlockCnt(extentID uint64) (block int64, err error) {
	ei, _ := s.getExtentInfoByExtentID(extentID)
	e, err := s.ExtentWithHeader(ei)
	if err != nil {
		return
	}
	block = e.getRealBlockCnt()
	return
}

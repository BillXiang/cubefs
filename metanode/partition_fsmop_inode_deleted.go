// Copyright 2018 The Chubao Authors.
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
	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/util/log"
	"io"
)

type FSMDeletedINode struct {
	inode uint64
}

func NewFSMDeletedINode(ino uint64) *FSMDeletedINode {
	fi := new(FSMDeletedINode)
	fi.inode = ino
	return fi
}

func (i *FSMDeletedINode) Marshal() (res []byte, err error) {
	res = make([]byte, 8)
	binary.BigEndian.PutUint64(res, i.inode)
	return
}

func (i *FSMDeletedINode) Unmarshal(data []byte) (err error) {
	i.inode = binary.BigEndian.Uint64(data)
	return
}

type FSMDeletedINodeBatch []*FSMDeletedINode

func (db FSMDeletedINodeBatch) Marshal() (data []byte, err error) {
	buff := bytes.NewBuffer(make([]byte, 0))
	err = binary.Write(buff, binary.BigEndian, uint32(len(db)))
	if err != nil {
		return
	}

	for _, di := range db {
		var bs []byte
		bs, err = di.Marshal()
		if err != nil {
			return
		}
		err = binary.Write(buff, binary.BigEndian, uint32(len(bs)))
		if err != nil {
			return
		}
		_, err = buff.Write(bs)
		if err != nil {
			return
		}
	}
	data = buff.Bytes()
	return
}

func FSMDeletedINodeBatchUnmarshal(raw []byte) (FSMDeletedINodeBatch, error) {
	buff := bytes.NewBuffer(raw)
	var batchLen uint32
	if err := binary.Read(buff, binary.BigEndian, &batchLen); err != nil {
		return nil, err
	}

	result := make(FSMDeletedINodeBatch, 0, int(batchLen))

	var dataLen uint32
	for j := 0; j < int(batchLen); j++ {
		if err := binary.Read(buff, binary.BigEndian, &dataLen); err != nil {
			return nil, err
		}
		data := make([]byte, int(dataLen))
		if _, err := buff.Read(data); err != nil {
			return nil, err
		}
		ino := new(FSMDeletedINode)
		if err := ino.Unmarshal(data); err != nil {
			return nil, err
		}
		result = append(result, ino)
	}

	return result, nil
}

type fsmOpDeletedInodeResponse struct {
	Status uint8  `json:"st"`
	Inode  uint64 `json:"ino"`
}

func (mp *metaPartition) mvToDeletedInodeTree(inode *Inode, timestamp int64) (status uint8, err error) {
	defer func() {
		if err == existsError || err == notExistsError {
			err = nil
		}
	}()
	status = proto.OpOk
	dino := NewDeletedInode(inode, timestamp)

	var resp *fsmOpDeletedInodeResponse
	resp, err = mp.fsmCreateDeletedInode(dino)
	if err == rocksdbError {
		log.LogErrorf("[mvToDeletedInodeTree], inode: %v, status: %v, err: %v", inode, status, err)
		return
	}
	status = resp.Status

	if _, err = mp.inodeTree.Delete(inode.Inode); err == rocksdbError {
		log.LogErrorf("[mvToDeletedInodeTree], inode(%v) deleted failed(%v)", inode, err)
		return
	}
	return
}

func (mp *metaPartition) fsmCreateDeletedInode(dino *DeletedINode) (rsp *fsmOpDeletedInodeResponse, err error) {
	rsp = new(fsmOpDeletedInodeResponse)
	rsp.Inode = dino.Inode.Inode
	rsp.Status = proto.OpOk
	if err = mp.inodeDeletedTree.Create(dino, false); err != nil {
		if err == existsError {
			rsp.Status = proto.OpExistErr
		} else {
			rsp.Status = proto.OpErr
		}
	}
	return
}

func (mp *metaPartition) fsmBatchRecoverDeletedInode(inos FSMDeletedINodeBatch) (rsp []*fsmOpDeletedInodeResponse, err error) {
	var wrongIndex = len(inos)
	defer func() {
		for index := wrongIndex; index < len(inos); index++ {
			rsp = append(rsp, &fsmOpDeletedInodeResponse{Status: proto.OpErr, Inode: inos[index].inode})
		}
	}()
	for index, ino := range inos {
		var resp *fsmOpDeletedInodeResponse
		resp, err = mp.recoverDeletedInode(ino.inode)
		if err == rocksdbError {
			wrongIndex = index
			break
		}
		if resp.Status != proto.OpOk {
			rsp = append(rsp, resp)
		}
	}
	return
}

func (mp *metaPartition) fsmRecoverDeletedInode(ino *FSMDeletedINode) (
	resp *fsmOpDeletedInodeResponse, err error) {
	return mp.recoverDeletedInode(ino.inode)
}

func (mp *metaPartition) recoverDeletedInode(inode uint64) (
	resp *fsmOpDeletedInodeResponse, err error) {
	resp = new(fsmOpDeletedInodeResponse)
	resp.Inode = inode
	resp.Status = proto.OpOk

	var (
		currInode    *Inode
		deletedInode *DeletedINode
	)

	ino := NewInode(inode, 0)
	defer func() {
		if resp.Status != proto.OpOk {
			log.LogDebugf("[recoverDeletedInode], partitionID(%v), inode(%v), status: %v",
				mp.config.PartitionId, ino.Inode, resp.Status)
		}
	}()

	dino := NewDeletedInodeByID(inode)
	currInode, err = mp.inodeTree.Get(ino.Inode)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}
	deletedInode, err = mp.inodeDeletedTree.Get(ino.Inode)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}
	if currInode != nil {
		if deletedInode != nil {
			_, err = mp.inodeDeletedTree.Delete(inode)
			if err == rocksdbError {
				resp.Status = proto.OpErr
				return
			}
			err = nil
			return
		}

		defer func() {
			if err = mp.inodeTree.Put(currInode); err != nil {
				resp.Status = proto.OpErr
				return
			}
		}()

		if currInode.ShouldDelete() {
			log.LogDebugf("[recoverDeletedInode], the inode[%v] 's deleted flag is invalid", ino)
			currInode.CancelDeleteMark()
		}
		if !proto.IsDir(currInode.Type) {
			currInode.IncNLink() // TODO: How to handle idempotent?
		}
		log.LogDebugf("[recoverDeletedInode], success to increase the link of inode[%v]", inode)
		return
	}

	if deletedInode == nil {
		log.LogErrorf("[recoverDeletedInode], not found the inode[%v] from deletedTree", dino)
		resp.Status = proto.OpNotExistErr
		return
	}

	if deletedInode.IsExpired {
		log.LogWarnf("[recoverDeletedInode], inode: [%v] is expired", deletedInode)
		resp.Status = proto.OpNotExistErr
		return
	}

	inoPtr := deletedInode.buildInode()
	inoPtr.CancelDeleteMark()
	if inoPtr.IsEmptyDir() {
		inoPtr.NLink = 2
	} else {
		inoPtr.IncNLink()
	}
	err = mp.inodeTree.Create(inoPtr, false)
	if err != nil && err != existsError {
		log.LogErrorf("[recoverDeletedInode], failed to add inode to inodeTree, inode: (%v), error: (%v)", inoPtr, err)
		resp.Status = proto.OpErr
		return
	}
	if _, err = mp.inodeDeletedTree.Delete(dino.Inode.Inode); err == rocksdbError {
		log.LogErrorf("[recoverDeletedInode], failed to delete deletedInode, delInode: (%v), error: (%v)", dino, err)
		resp.Status = proto.OpErr
	}
	return
}

func (mp *metaPartition) fsmBatchCleanDeletedInode(inos FSMDeletedINodeBatch) (rsp []*fsmOpDeletedInodeResponse, err error) {
	rsp = make([]*fsmOpDeletedInodeResponse, 0)
	var wrongIndex = len(inos)
	defer func() {
		for index := wrongIndex; index < len(inos); index++ {
			rsp = append(rsp, &fsmOpDeletedInodeResponse{Status: proto.OpErr, Inode: inos[index].inode})
		}
	}()

	for index, ino := range inos {
		var resp *fsmOpDeletedInodeResponse
		resp, err = mp.cleanDeletedInode(ino.inode)
		if err == rocksdbError {
			wrongIndex = index
			break
		}
		if resp.Status != proto.OpOk {
			rsp = append(rsp, resp)
		}
	}
	return
}

func (mp *metaPartition) fsmCleanDeletedInode(ino *FSMDeletedINode) (
	resp *fsmOpDeletedInodeResponse, err error) {
	return mp.cleanDeletedInode(ino.inode)
}

func (mp *metaPartition) cleanDeletedInode(inode uint64) (
	resp *fsmOpDeletedInodeResponse, err error) {
	resp = new(fsmOpDeletedInodeResponse)
	resp.Inode = inode
	resp.Status = proto.OpOk
	defer func() {
		log.LogDebugf("[cleanDeletedInode], inode: (%v), status:[%v]", inode, resp.Status)
	}()

	var dino *DeletedINode
	dino, err = mp.inodeDeletedTree.Get(inode)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}

	if dino == nil {
		resp.Status = proto.OpNotExistErr
		return
	}

	begDen := newPrimaryDeletedDentry(dino.Inode.Inode, "", 0, 0)
	endDen := newPrimaryDeletedDentry(dino.Inode.Inode+1, "", 0, 0)
	var children int
	err = mp.dentryDeletedTree.Range(begDen, endDen, func(v []byte) (bool, error) {
		children++
		if children > 0 {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		resp.Status = proto.OpErr
		return
	}

	if children > 0 {
		resp.Status = proto.OpExistErr
		return
	}

	if dino.IsEmptyDir() {
		_, err = mp.inodeDeletedTree.Delete(dino.Inode.Inode)
		if err == rocksdbError {
			resp.Status = proto.OpErr
		}
		return
	}

	if dino.IsTempFile() {
		dino.setExpired()
		mp.freeList.Push(dino.Inode.Inode)
		return
	}
	resp.Status = proto.OpErr
	return
}

func (mp *metaPartition) fsmCleanExpiredInode(inos FSMDeletedINodeBatch) (rsp []*fsmOpDeletedInodeResponse, err error) {
	var wrongIndex = len(inos)
	defer func() {
		for index := wrongIndex; index < len(inos); index++ {
			rsp = append(rsp, &fsmOpDeletedInodeResponse{Status: proto.OpErr, Inode: inos[index].inode})
		}
	}()
	for index, ino := range inos {
		var resp *fsmOpDeletedInodeResponse
		resp, err = mp.cleanExpiredInode(ino.inode)
		if err == rocksdbError {
			wrongIndex = index
			break
		}
		if resp.Status != proto.OpOk {
			rsp = append(rsp, resp)
		}
	}
	return
}

func (mp *metaPartition) cleanExpiredInode(ino uint64) (
	resp *fsmOpDeletedInodeResponse, err error) {
	resp = new(fsmOpDeletedInodeResponse)
	resp.Inode = ino
	resp.Status = proto.OpOk
	defer func() {
		log.LogDebugf("[cleanExpiredInode], inode: %v, status: %v", ino, resp.Status)
	}()

	var di *DeletedINode
	di, err = mp.inodeDeletedTree.Get(ino)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}
	if di == nil {
		return
	}

	if di.IsEmptyDir() {
		if _, err = mp.inodeDeletedTree.Delete(di.Inode.Inode); err == rocksdbError {
			resp.Status = proto.OpErr
		}
		return
	}

	if di.IsTempFile() {
		di.setExpired()
		mp.freeList.Push(di.Inode.Inode)
		return
	}

	resp.Status = proto.OpErr
	return
}

func (mp *metaPartition) internalClean(val []byte) (err error) {
	if len(val) == 0 {
		return
	}
	buf := bytes.NewBuffer(val)
	ino := NewInode(0, 0)
	for {
		err = binary.Read(buf, binary.BigEndian, &ino.Inode)
		if err != nil {
			if err == io.EOF {
				err = nil
				return
			}
			return
		}
		log.LogDebugf("internalClean: received internal delete: partitionID(%v) inode(%v)",
			mp.config.PartitionId, ino.Inode)
		if err = mp.internalCleanDeletedInode(ino); err == rocksdbError {
			return
		}
	}
}

func (mp *metaPartition) internalCleanDeletedInode(ino *Inode) (err error) {
	_, err = mp.inodeDeletedTree.Delete(ino.Inode)
	if err != nil {
		log.LogDebugf("[internalCleanDeletedInode], ino: %v", ino)
		if err == rocksdbError {
			log.LogErrorf("[internalCleanDeletedInode] delete error:%v", err)
			return
		}
		if _, err = mp.inodeTree.Delete(ino.Inode); err == rocksdbError {
			log.LogErrorf("[internalCleanDeletedInode] delete error:%v", err)
			return
		}
		log.LogDebugf("[internalCleanDeletedInode], delete inode:%v result:%v", ino, err)
	} else {
		log.LogDebugf("[internalCleanDeletedInode], dino: %v", ino)
	}
	mp.freeList.Remove(ino.Inode)
	if _, err = mp.extendTree.Delete(ino.Inode); err == rocksdbError { // Also delete extend attribute.
		log.LogErrorf("[internalCleanDeletedInode], deleted extend failed, ino:%v, error:%v", ino.Inode, err)
		return
	}
	log.LogDebugf("[internalCleanDeletedInode], delete extend:%v result:%v", ino, err)
	return
}

func (mp *metaPartition) checkExpiredAndInsertFreeList(di *DeletedINode) {
	if proto.IsDir(di.Type) {
		return
	}
	if di.IsExpired {
		mp.freeList.Push(di.Inode.Inode)
	}
}

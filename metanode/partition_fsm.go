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

package metanode

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/cubefs/cubefs/depends/tiglabs/raft"
	raftproto "github.com/cubefs/cubefs/depends/tiglabs/raft/proto"
	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/exporter"
	"github.com/cubefs/cubefs/util/log"
)

// Apply applies the given operational commands.
func (mp *metaPartition) Apply(command []byte, index uint64) (resp interface{}, err error) {
	msg := &MetaItem{}
	defer func() {
		if err == nil {
			mp.uploadApplyID(index)
		}
	}()
	if err = msg.UnmarshalJson(command); err != nil {
		return
	}

	switch msg.Op {
	case opFSMCreateInode:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		if mp.config.Cursor < ino.Inode {
			mp.config.Cursor = ino.Inode
		}
		resp = mp.fsmCreateInode(ino)
	case opFSMCreateInodeQuota:
		qinode := &MetaQuotaInode{}
		if err = qinode.Unmarshal(msg.V); err != nil {
			return
		}
		ino := qinode.inode
		if mp.config.Cursor < ino.Inode {
			mp.config.Cursor = ino.Inode
		}
		if len(qinode.quotaIds) > 0 {
			mp.setInodeQuota(qinode.quotaIds, ino.Inode)
		}
		resp = mp.fsmCreateInode(ino)
		if resp == proto.OpOk {
			for _, quotaId := range qinode.quotaIds {
				mp.mqMgr.updateUsedInfo(0, 1, quotaId)
			}
		}
	case opFSMUnlinkInode:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmUnlinkInode(ino)
	case opFSMUnlinkInodeBatch:
		inodes, err := InodeBatchUnmarshal(msg.V)
		if err != nil {
			return nil, err
		}
		resp = mp.fsmUnlinkInodeBatch(inodes)
	case opFSMExtentTruncate:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmExtentsTruncate(ino)
	case opFSMCreateLinkInode:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmCreateLinkInode(ino)
	case opFSMEvictInode:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmEvictInode(ino)
	case opFSMEvictInodeBatch:
		inodes, err := InodeBatchUnmarshal(msg.V)
		if err != nil {
			return nil, err
		}
		resp = mp.fsmBatchEvictInode(inodes)
	case opFSMSetAttr:
		req := &SetattrRequest{}
		err = json.Unmarshal(msg.V, req)
		if err != nil {
			return
		}
		err = mp.fsmSetAttr(req)
	case opFSMCreateDentry:
		den := &Dentry{}
		if err = den.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmCreateDentry(den, false)
	case opCreateDentryEx:
		den := &DentryEx{}
		if err = json.Unmarshal(msg.V, den); err != nil {
			return
		}
		resp = mp.fsmCreateDentryEx(den)
	case opFSMDeleteDentry:
		den := &Dentry{}
		if err = den.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmDeleteDentry(den, false)
	case opFSMDeleteDentryBatch:
		db, err := DentryBatchUnmarshal(msg.V)
		if err != nil {
			return nil, err
		}
		resp = mp.fsmBatchDeleteDentry(db)
	case opFSMUpdateDentry:
		den := &Dentry{}
		if err = den.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmUpdateDentry(den)
	case opFSMUpdatePartition:
		req := &UpdatePartitionReq{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp, err = mp.fsmUpdatePartition(req.End)
	case opFSMExtentsAdd:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmAppendExtents(ino)
	case opFSMExtentsAddWithCheck:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmAppendExtentsWithCheck(ino, false)
	case opFSMExtentSplit:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmAppendExtentsWithCheck(ino, true)
	case opFSMObjExtentsAdd:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmAppendObjExtents(ino)
	case opFSMExtentsEmpty:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmExtentsEmpty(ino)
	// case opFSMExtentsDel:
	// 	ino := NewInode(0, 0)
	// 	if err = ino.Unmarshal(msg.V); err != nil {
	// 		return
	// 	}
	// 	resp = mp.fsmDelExtents(ino)
	case opFSMClearInodeCache:
		ino := NewInode(0, 0)
		if err = ino.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmClearInodeCache(ino)
	case opFSMSentToChan:
		resp = mp.fsmSendToChan(msg.V, true)
	case opFSMStoreTick:
		inodeTree := mp.inodeTree.GetTree()
		dentryTree := mp.dentryTree.GetTree()
		extendTree := mp.extendTree.GetTree()
		multipartTree := mp.multipartTree.GetTree()
		txTree := mp.txProcessor.txManager.txTree.GetTree()
		txRbInodeTree := mp.txProcessor.txResource.txRbInodeTree.GetTree()
		txRbDentryTree := mp.txProcessor.txResource.txRbDentryTree.GetTree()
		txId := mp.txProcessor.txManager.txIdAlloc.getTransactionID()

		msg := &storeMsg{
			command:        opFSMStoreTick,
			applyIndex:     index,
			txId:           txId,
			inodeTree:      inodeTree,
			dentryTree:     dentryTree,
			extendTree:     extendTree,
			multipartTree:  multipartTree,
			txTree:         txTree,
			txRbInodeTree:  txRbInodeTree,
			txRbDentryTree: txRbDentryTree,
			multiVerList:   mp.getVerList(),
		}
		mp.storeChan <- msg
	case opFSMInternalDeleteInode:
		err = mp.internalDelete(msg.V)
	case opFSMInternalDeleteInodeBatch:
		err = mp.internalDeleteBatch(msg.V)
	case opFSMInternalDelExtentFile:
		err = mp.delOldExtentFile(msg.V)
	case opFSMInternalDelExtentCursor:
		err = mp.setExtentDeleteFileCursor(msg.V)
	case opFSMSetXAttr:
		var extend *Extend
		if extend, err = NewExtendFromBytes(msg.V); err != nil {
			return
		}
		err = mp.fsmSetXAttr(extend)
	case opSetInodeLock:
		req := new(proto.InodeLockReq)
		err = json.Unmarshal(msg.V, req)
		if err != nil {
			return
		}
		resp = mp.fsmSetInodeLock(req)
	case opFSMRemoveXAttr:
		var extend *Extend
		if extend, err = NewExtendFromBytes(msg.V); err != nil {
			return
		}
		err = mp.fsmRemoveXAttr(extend)
	case opFSMUpdateXAttr:
		var extend *Extend
		if extend, err = NewExtendFromBytes(msg.V); err != nil {
			return
		}
		err = mp.fsmSetXAttr(extend)
	case opFSMCreateMultipart:
		var multipart *Multipart
		multipart = MultipartFromBytes(msg.V)
		resp = mp.fsmCreateMultipart(multipart)
	case opFSMRemoveMultipart:
		var multipart *Multipart
		multipart = MultipartFromBytes(msg.V)
		resp = mp.fsmRemoveMultipart(multipart)
	case opFSMAppendMultipart:
		var multipart *Multipart
		multipart = MultipartFromBytes(msg.V)
		resp = mp.fsmAppendMultipart(multipart)
	case opFSMSyncCursor:
		var cursor uint64
		cursor = binary.BigEndian.Uint64(msg.V)
		if cursor > mp.config.Cursor {
			mp.config.Cursor = cursor
		}
	case opFSMSyncTxID:
		var txID uint64
		txID = binary.BigEndian.Uint64(msg.V)
		if txID > mp.txProcessor.txManager.txIdAlloc.getTransactionID() {
			mp.txProcessor.txManager.txIdAlloc.setTransactionID(txID)
		}
	case opFSMTxCreateInode:
		txIno := NewTxInode("", 0, 0, 0, nil)
		if err = txIno.Unmarshal(msg.V); err != nil {
			return
		}
		if mp.config.Cursor < txIno.Inode.Inode {
			mp.config.Cursor = txIno.Inode.Inode
		}
		resp = mp.fsmTxCreateInode(txIno, []uint32{})
	case opFSMTxCreateInodeQuota:
		qinode := &TxMetaQuotaInode{}
		if err = qinode.Unmarshal(msg.V); err != nil {
			return
		}
		txIno := qinode.txinode
		if mp.config.Cursor < txIno.Inode.Inode {
			mp.config.Cursor = txIno.Inode.Inode
		}
		if len(qinode.quotaIds) > 0 {
			mp.setInodeQuota(qinode.quotaIds, txIno.Inode.Inode)
		}
		resp = mp.fsmTxCreateInode(txIno, qinode.quotaIds)
		if resp == proto.OpOk {
			for _, quotaId := range qinode.quotaIds {
				mp.mqMgr.updateUsedInfo(0, 1, quotaId)
			}
		}
	case opFSMTxCreateDentry:
		txDen := NewTxDentry(0, "", 0, 0, nil, nil)
		if err = txDen.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmTxCreateDentry(txDen, false)
	case opFSMTxSetState:
		req := &proto.TxSetStateRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxSetState(req)
	case opFSMTxCommit:
		req := &proto.TxApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxCommit(req.TxID)
	case opFSMTxInodeCommit:
		req := &proto.TxInodeApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxInodeCommit(req.TxID, req.Inode)
	case opFSMTxDentryCommit:
		req := &proto.TxDentryApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxDentryCommit(req.TxID, req.Pid, req.Name)
	case opFSMTxRollback:
		req := &proto.TxApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxRollback(req.TxID)
	case opFSMTxInodeRollback:
		req := &proto.TxInodeApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxInodeRollback(req)
	case opFSMTxDentryRollback:
		req := &proto.TxDentryApplyRequest{}
		if err = json.Unmarshal(msg.V, req); err != nil {
			return
		}
		resp = mp.fsmTxDentryRollback(req)
	case opFSMTxDeleteDentry:
		txDen := NewTxDentry(0, "", 0, 0, nil, nil)
		if err = txDen.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmTxDeleteDentry(txDen, false)
	case opFSMTxUnlinkInode:
		txIno := NewTxInode("", 0, 0, 0, nil)
		if err = txIno.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmTxUnlinkInode(txIno)
	case opFSMTxUpdateDentry:
		//txDen := NewTxDentry(0, "", 0, 0, nil)
		txUpdateDen := NewTxUpdateDentry(nil, nil, nil)
		if err = txUpdateDen.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmTxUpdateDentry(txUpdateDen)
	case opFSMTxCreateLinkInode:
		txIno := NewTxInode("", 0, 0, 0, nil)
		if err = txIno.Unmarshal(msg.V); err != nil {
			return
		}
		resp = mp.fsmTxCreateLinkInode(txIno)
	case opFSMVersionOp:
		resp = mp.fsmVersionOp(msg.V)
	}

	return
}

func (mp *metaPartition) fsmVersionOp(reqData []byte) (err error) {
	mp.multiVersionList.Lock()
	defer mp.multiVersionList.Unlock()

	var opData VerOpData
	if err = json.Unmarshal(reqData, &opData); err != nil {
		log.LogErrorf("action[fsmVersionOp] unmarshal error %v", err)
		return
	}

	log.LogInfof("action[fsmVersionOp] mp[%v] seq %v, op %v", mp.config.PartitionId, opData.VerSeq, opData.Op)

	if opData.Op == proto.CreateVersionCommit {
		cnt := len(mp.multiVersionList.VerList)
		if cnt > 0 && mp.multiVersionList.VerList[cnt-1].Ver >= opData.VerSeq {
			log.LogErrorf("action[MultiVersionOp] reqeust seq %v lessOrEqual last exist snapshot seq %v",
				mp.multiVersionList.VerList[cnt-1].Ver, opData.VerSeq)
			mp.verSeq = opData.VerSeq
			return
		}
		newVer := &proto.VolVersionInfo{
			Status: proto.VersionNormal,
			Ctime:  time.Now(),
			Ver:    opData.VerSeq,
		}
		mp.verSeq = opData.VerSeq
		mp.multiVersionList.VerList = append(mp.multiVersionList.VerList, newVer)

		log.LogInfof("action[fsmVersionOp] mp[%v] seq %v, op %v, seqArray size %v", mp.config.PartitionId, opData.VerSeq, opData.Op, len(mp.multiVersionList.VerList))
	} else if opData.Op == proto.DeleteVersion {
		for i, ver := range mp.multiVersionList.VerList {
			if ver.Ver == opData.VerSeq {
				log.LogInfof("action[fsmVersionOp] mp[%v] seq %v, op %v, seqArray size %v", mp.config.PartitionId, opData.VerSeq, opData.Op, len(mp.multiVersionList.VerList))
				// mp.multiVersionList = append(mp.multiVersionList[:i], mp.multiVersionList[i+1:]...)
				mp.multiVersionList.VerList = append(mp.multiVersionList.VerList[:i], mp.multiVersionList.VerList[i+1:]...)
				break
			}
		}
	} else {
		log.LogErrorf("action[fsmVersionOp] mp %v with seq %v process op type %v seq %v not found",
			mp.config.PartitionId, mp.verSeq, opData.Op, opData.VerSeq)
	}
	return
}

// ApplyMemberChange  apply changes to the raft member.
func (mp *metaPartition) ApplyMemberChange(confChange *raftproto.ConfChange, index uint64) (resp interface{}, err error) {
	defer func() {
		if err == nil {
			mp.uploadApplyID(index)
		}
	}()
	// change memory status
	var (
		updated bool
	)
	switch confChange.Type {
	case raftproto.ConfAddNode:
		req := &proto.AddMetaPartitionRaftMemberRequest{}
		if err = json.Unmarshal(confChange.Context, req); err != nil {
			return
		}
		updated, err = mp.confAddNode(req, index)
	case raftproto.ConfRemoveNode:
		req := &proto.RemoveMetaPartitionRaftMemberRequest{}
		if err = json.Unmarshal(confChange.Context, req); err != nil {
			return
		}
		updated, err = mp.confRemoveNode(req, index)
	case raftproto.ConfUpdateNode:
		//updated, err = mp.confUpdateNode(req, index)
	}
	if err != nil {
		return
	}
	if updated {
		mp.config.sortPeers()
		if err = mp.persistMetadata(); err != nil {
			log.LogErrorf("action[ApplyMemberChange] err[%v].", err)
			return
		}
	}
	return
}

// Snapshot returns the snapshot of the current meta partition.
func (mp *metaPartition) Snapshot() (snap raftproto.Snapshot, err error) {
	snap, err = newMetaItemIterator(mp)
	return
}

// ApplySnapshot applies the given multiSnap.multiVersions.
func (mp *metaPartition) ApplySnapshot(peers []raftproto.Peer, iter raftproto.SnapIterator) (err error) {
	var (
		data          []byte
		index         int
		appIndexID    uint64
		txID          uint64
		cursor        uint64
		inodeTree     = NewBtree()
		dentryTree    = NewBtree()
		extendTree    = NewBtree()
		multipartTree = NewBtree()
		txTree        = NewBtree()
		//transactions       = make(map[string]*proto.TransactionInfo)
		txRbInodeTree = NewBtree()
		//txRollbackInodes   = make(map[uint64]*TxRollbackInode)
		txRbDentryTree = NewBtree()
		//txRollbackDentries = make(map[string]*TxRollbackDentry)

	)
	defer func() {
		if err == io.EOF {
			mp.applyID = appIndexID
			mp.txProcessor.txManager.txIdAlloc.setTransactionID(txID)
			mp.inodeTree = inodeTree
			mp.dentryTree = dentryTree
			mp.extendTree = extendTree
			mp.multipartTree = multipartTree
			mp.config.Cursor = cursor
			mp.txProcessor.txManager.txTree = txTree
			//mp.txProcessor.txManager.transactions = transactions
			mp.txProcessor.txResource.txRbInodeTree = txRbInodeTree
			//mp.txProcessor.txResource.txRollbackInodes = txRollbackInodes
			mp.txProcessor.txResource.txRbDentryTree = txRbDentryTree
			//mp.txProcessor.txResource.txRollbackDentries = txRollbackDentries

			err = nil
			// store message
			mp.storeChan <- &storeMsg{
				command:       opFSMStoreTick,
				applyIndex:    mp.applyID,
				txId:          mp.txProcessor.txManager.txIdAlloc.getTransactionID(),
				inodeTree:     mp.inodeTree,
				dentryTree:    mp.dentryTree,
				extendTree:    mp.extendTree,
				multipartTree: mp.multipartTree,
				txTree:        mp.txProcessor.txManager.txTree,
				//transactions:       mp.txProcessor.txManager.transactions,
				txRbInodeTree: mp.txProcessor.txResource.txRbInodeTree,
				//txRollbackInodes:   mp.txProcessor.txResource.txRollbackInodes,
				txRbDentryTree: mp.txProcessor.txResource.txRbDentryTree,
				//txRollbackDentries: mp.txProcessor.txResource.txRollbackDentries,
				multiVerList: mp.getVerList(),
			}
			/*if mp.txProcessor.txManager.txTree.Len() > 0 {
				log.LogDebugf("ApplySnapshot: notify transaction expiration")
				mp.txProcessor.txManager.notifyNewTransaction()
			}*/

			select {
			case mp.extReset <- struct{}{}:
				log.LogDebugf("ApplySnapshot: finish with EOF: partitionID(%v) applyID(%v)", mp.config.PartitionId, mp.applyID)
				return
			case <-mp.stopC:
				log.LogWarnf("ApplySnapshot: revice stop signal, exit now, partition(%d), applyId(%d)", mp.config.PartitionId, mp.applyID)
				err = errors.New("server has been shutdown")
				return
			}
		}
		log.LogErrorf("ApplySnapshot: stop with error: partitionID(%v) err(%v)", mp.config.PartitionId, err)
	}()
	for {
		data, err = iter.Next()
		if err != nil {
			return
		}
		if index == 0 {
			appIndexID = binary.BigEndian.Uint64(data)
			index++
			continue
		}
		if index == 1 {
			strTxID := string(data)
			txID, _ = strconv.ParseUint(strTxID, 10, 64)
			index++
			continue
		}
		snap := NewMetaItem(0, nil, nil)
		if err = snap.UnmarshalBinary(data); err != nil {
			return
		}
		index++
		switch snap.Op {
		case opFSMCreateInode:
			ino := NewInode(0, 0)

			// TODO Unhandled errors
			ino.UnmarshalKey(snap.K)
			ino.UnmarshalValue(snap.V)
			if cursor < ino.Inode {
				cursor = ino.Inode
			}
			inodeTree.ReplaceOrInsert(ino, true)
			log.LogDebugf("ApplySnapshot: create inode: partitonID(%v) inode(%v).", mp.config.PartitionId, ino)
		case opFSMCreateDentry:
			dentry := &Dentry{}
			if err = dentry.UnmarshalKey(snap.K); err != nil {
				return
			}
			if err = dentry.UnmarshalValue(snap.V); err != nil {
				return
			}
			dentryTree.ReplaceOrInsert(dentry, true)
			log.LogDebugf("ApplySnapshot: create dentry: partitionID(%v) dentry(%v)", mp.config.PartitionId, dentry)
		case opFSMSetXAttr:
			var extend *Extend
			if extend, err = NewExtendFromBytes(snap.V); err != nil {
				return
			}
			extendTree.ReplaceOrInsert(extend, true)
			log.LogDebugf("ApplySnapshot: set extend attributes: partitionID(%v) extend(%v)",
				mp.config.PartitionId, extend)
		case opFSMCreateMultipart:
			var multipart = MultipartFromBytes(snap.V)
			multipartTree.ReplaceOrInsert(multipart, true)
			log.LogDebugf("ApplySnapshot: create multipart: partitionID(%v) multipart(%v)", mp.config.PartitionId, multipart)
		case opFSMTxSnapshot:
			txInfo := proto.NewTransactionInfo(0, proto.TxTypeUndefined)
			txInfo.Unmarshal(snap.V)
			//transactions[txInfo.TxID] = txInfo
			txTree.ReplaceOrInsert(txInfo, true)
			log.LogDebugf("ApplySnapshot: create transaction: partitionID(%v) txInfo(%v)", mp.config.PartitionId, txInfo)
		case opFSMTxRbInodeSnapshot:
			txRbInode := NewTxRollbackInode(nil, []uint32{}, nil, 0)
			txRbInode.Unmarshal(snap.V)
			//txRollbackInodes[txRbInode.inode.Inode] = txRbInode
			txRbInodeTree.ReplaceOrInsert(txRbInode, true)
			log.LogDebugf("ApplySnapshot: create txRbInode: partitionID(%v) txRbInode(%v)", mp.config.PartitionId, txRbInode)
		case opFSMTxRbDentrySnapshot:
			txRbDentry := NewTxRollbackDentry(nil, nil, 0)
			txRbDentry.Unmarshal(snap.V)
			//txRollbackDentries[txRbDentry.txDentryInfo.GetKey()] = txRbDentry
			txRbDentryTree.ReplaceOrInsert(txRbDentry, true)
			log.LogDebugf("ApplySnapshot: create txRbDentry: partitionID(%v) txRbDentry(%v)", mp.config.PartitionId, txRbDentry)
		case opExtentFileSnapshot:
			fileName := string(snap.K)
			fileName = path.Join(mp.config.RootDir, fileName)
			if err = ioutil.WriteFile(fileName, snap.V, 0644); err != nil {
				log.LogErrorf("ApplySnapshot: write snap extent delete file fail: partitionID(%v) err(%v)",
					mp.config.PartitionId, err)
			}
			log.LogDebugf("ApplySnapshot: write snap extent delete file: partitonID(%v) filename(%v).",
				mp.config.PartitionId, fileName)
		default:
			err = fmt.Errorf("unknown Op=%d", snap.Op)
			return
		}
	}
}

// HandleFatalEvent handles the fatal errors.
func (mp *metaPartition) HandleFatalEvent(err *raft.FatalError) {
	// Panic while fatal event happen.
	exporter.Warning(fmt.Sprintf("action[HandleFatalEvent] err[%v].", err))
	log.LogFatalf("action[HandleFatalEvent] err[%v].", err)
	panic(err.Err)
}

// HandleLeaderChange handles the leader changes.
func (mp *metaPartition) HandleLeaderChange(leader uint64) {
	exporter.Warning(fmt.Sprintf("metaPartition(%v) changeLeader to (%v)", mp.config.PartitionId, leader))
	if mp.config.NodeId == leader {
		localIp := mp.manager.metaNode.localAddr
		if localIp == "" {
			localIp = "127.0.0.1"
		}

		conn, err := net.DialTimeout("tcp", net.JoinHostPort(localIp, serverPort), time.Second)
		if err != nil {
			log.LogErrorf(fmt.Sprintf("HandleLeaderChange serverPort not exsit ,error %v", err))
			exporter.Warning(fmt.Sprintf("mp [%v] HandleLeaderChange serverPort not exsit ,error %v", mp.config.PartitionId, err))
			go mp.raftPartition.TryToLeader(mp.config.PartitionId)
			return
		}
		log.LogDebugf("[metaPartition] HandleLeaderChange close conn %v, nodeId: %v, leader: %v", serverPort, mp.config.NodeId, leader)
		exporter.Warning(fmt.Sprintf("[metaPartition]mp [%v] HandleLeaderChange close conn %v, nodeId: %v, leader: %v", mp.config.PartitionId, serverPort, mp.config.NodeId, leader))
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}
	if mp.config.NodeId != leader {
		log.LogDebugf("[metaPartition] pid: %v HandleLeaderChange become unleader nodeId: %v, leader: %v", mp.config.PartitionId, mp.config.NodeId, leader)
		exporter.Warning(fmt.Sprintf("[metaPartition] pid: %v HandleLeaderChange become unleader nodeId: %v, leader: %v", mp.config.PartitionId, mp.config.NodeId, leader))
		mp.txProcessor.txManager.Stop()
		mp.storeChan <- &storeMsg{
			command: stopStoreTick,
		}
		return
	}
	mp.storeChan <- &storeMsg{
		command: startStoreTick,
	}

	mp.txProcessor.txManager.Start()

	log.LogDebugf("[metaPartition] pid: %v HandleLeaderChange become leader conn %v, nodeId: %v, leader: %v", mp.config.PartitionId, serverPort, mp.config.NodeId, leader)
	exporter.Warning(fmt.Sprintf("[metaPartition] pid: %v HandleLeaderChange become leader conn %v, nodeId: %v, leader: %v", mp.config.PartitionId, serverPort, mp.config.NodeId, leader))
	if mp.config.Start == 0 && mp.config.Cursor == 0 {
		id, err := mp.nextInodeID()
		if err != nil {
			log.LogFatalf("[HandleLeaderChange] init root inode id: %s.", err.Error())
			exporter.Warning(fmt.Sprintf("[HandleLeaderChange] pid %v init root inode id: %s.", mp.config.PartitionId, err.Error()))
		}
		ino := NewInode(id, proto.Mode(os.ModePerm|os.ModeDir))
		go mp.initInode(ino)
	}
}

// Put puts the given key-value pair (operation key and operation request) into the raft store.
func (mp *metaPartition) submit(op uint32, data []byte) (resp interface{}, err error) {
	log.LogDebugf("submit. op %v", op)
	snap := NewMetaItem(0, nil, nil)
	snap.Op = op
	if data != nil {
		snap.V = data
	}
	cmd, err := snap.MarshalJson()
	if err != nil {
		return
	}

	// submit to the raft store
	resp, err = mp.raftPartition.Submit(cmd)
	log.LogDebugf("submit. op %v done", op)
	return
}

func (mp *metaPartition) uploadApplyID(applyId uint64) {
	atomic.StoreUint64(&mp.applyID, applyId)
}

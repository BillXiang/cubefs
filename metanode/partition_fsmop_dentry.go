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
	"fmt"
	"github.com/cubefs/cubefs/util/exporter"
	"strings"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/log"
)

type DentryResponse struct {
	Status uint8
	Msg    *Dentry
}

func NewDentryResponse() *DentryResponse {
	return &DentryResponse{
		Msg: &Dentry{},
	}
}

func (mp *metaPartition) fsmTxCreateDentry(dbHandle interface{}, txDentry *TxDentry) (status uint8, err error) {

	done := mp.txProcessor.txManager.txInRMDone(txDentry.TxInfo.TxID)
	if done {
		log.LogWarnf("fsmTxCreateDentry: tx is already finish. txId %s", txDentry.TxInfo.TxID)
		status = proto.OpTxInfoNotExistErr
		return
	}

	txDI := proto.NewTxDentryInfo("", txDentry.Dentry.ParentId, txDentry.Dentry.Name, 0)
	txDenInfo, ok := txDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		status = proto.OpTxDentryInfoNotExistErr
		return
	}

	rbDentry := NewTxRollbackDentry(txDentry.Dentry, txDenInfo, TxDelete)
	status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry)
	if status == proto.OpExistErr {
		return proto.OpOk, nil
	}

	if status != proto.OpOk {
		return
	}

	defer func() {
		if status != proto.OpOk {
			mp.txProcessor.txResource.deleteTxRollbackDentry(txDenInfo.ParentId, txDenInfo.Name, txDenInfo.TxID)
		}
	}()

	return mp.fsmCreateDentry(dbHandle, txDentry.Dentry, false)
}

// Insert a dentry into the dentry tree.
func (mp *metaPartition) fsmCreateDentry(dbHandle interface{}, dentry *Dentry, forceUpdate bool) (status uint8, err error) {
	status = proto.OpOk
	var (
		parIno *Inode
		d      *Dentry
		ok     bool
	)
	parIno, err = mp.inodeTree.Get(dentry.ParentId)
	if err != nil {
		status = proto.OpErr
		return
	}
	if !forceUpdate {
		if parIno == nil || parIno.ShouldDelete() {
			log.LogErrorf("action[fsmCreateDentry] mp %v ParentId [%v] get [%v] but should del, dentry name [%v], inode [%v]", mp.config.PartitionId, dentry.ParentId, parIno, dentry.Name, dentry.Inode)
			status = proto.OpNotExistErr
			return
		}
		if !proto.IsDir(parIno.Type) {
			log.LogErrorf("action[fsmCreateDentry] mp %v ParentId [%v] get [%v] but should del, dentry name [%v], inode [%v]", mp.config.PartitionId, dentry.ParentId, parIno, dentry.Name, dentry.Inode)
			status = proto.OpArgMismatchErr
			return
		}
	}
	if d, ok, err = mp.dentryTree.Create(dbHandle, dentry, false); err != nil {
		status = proto.OpErr
		return
	}

	if !ok {
		//do not allow directories and files to overwrite each
		// other when renaming
		if d.isDeleted() {
			log.LogDebugf("action[fsmCreateDentry] mp %v newest dentry %v be set deleted flag", mp.config.PartitionId, d)
			d.Inode = dentry.Inode
			if d.getVerSeq() == dentry.getVerSeq() {
			d.setVerSeq(dentry.getSeqFiled())
		} else {
			d.addVersion(dentry.getSeqFiled())
		}
			d.Type = dentry.Type
			d.ParentId = dentry.ParentId
			log.LogDebugf("action[fsmCreateDentry.ver] mp %v latest dentry already deleted.Now create new one [%v]", mp.config.PartitionId, dentry)

			if !forceUpdate {
			parIno.IncNLink(mp.verSeq)
			parIno.SetMtime()
			if err = mp.inodeTree.Update(dbHandle, parIno); err != nil {
				log.LogErrorf("action[fsmCreateDentry] update parent inode err:%v", err)
				status = proto.OpErr
			}
		}
			return
		} else if proto.OsModeType(dentry.Type) != proto.OsModeType(d.Type) && !proto.IsSymlink(dentry.Type) && !proto.IsSymlink(d.Type) {
			log.LogErrorf("action[fsmCreateDentry] ParentId [%v] get [%v] but should del, dentry name [%v], inode [%v], type[%v,%v],dir[%v,%v]",
			dentry.ParentId, parIno, dentry.Name, dentry.Inode, dentry.Type, d.Type, proto.IsSymlink(dentry.Type), proto.IsSymlink(d.Type))
			status = proto.OpArgMismatchErr
			return
		} else if dentry.ParentId == d.ParentId && strings.Compare(dentry.Name, d.Name) == 0 && dentry.Inode == d.Inode {
			log.LogDebugf("action[fsmCreateDentry.ver] mp %v no need repeat create new one [%v]", mp.config.PartitionId, dentry)
			return
		}
		log.LogErrorf("action[fsmCreateDentry.ver] mp %v dentry already exist [%v] and diff with the request [%v]", mp.config.PartitionId, d, dentry)
		status = proto.OpExistErr
		return
	}

	if !forceUpdate {
		parIno.IncNLink(mp.verSeq)
		parIno.SetMtime()
		if err = mp.inodeTree.Update(dbHandle, parIno); err != nil {
			log.LogErrorf("action[fsmCreateDentry] update parent inode err:%v", err)
			status = proto.OpErr
		}
	}
	return
}

func (mp *metaPartition) getDentryList(dentry *Dentry) (denList []proto.DetryInfo) {
	item, err := mp.dentryTree.RefGet(dentry.ParentId, dentry.Name)
	if err != nil {
		if err == rocksDBError {
			exporter.WarningRocksdbError(fmt.Sprintf("action[getDentry] clusterID[%s] volumeName[%s] partitionID[%v]" +
				" get dentry failed witch rocksdb error[dentry:%v]", mp.manager.metaNode.clusterId, mp.config.VolName,
				mp.config.PartitionId, dentry))
		}
		return
	}
	if item != nil {
		if item.getSnapListLen() == 0 {
			return
		}
		for _, den := range item.multiSnap.dentryList {
			denList = append(denList, proto.DetryInfo{
				Inode:  den.Inode,
				Mode:   den.Type,
				IsDel:  den.isDeleted(),
				VerSeq: den.getVerSeq(),
			})
		}
	}
	return
}

// Query a dentry from the dentry tree with specified dentry info.
func (mp *metaPartition) getDentry(dentry *Dentry) (*Dentry, uint8, error) {
	status := proto.OpOk
	d, err := mp.dentryTree.RefGet(dentry.ParentId, dentry.Name)
	if err != nil {
		if err == rocksDBError {
			exporter.WarningRocksdbError(fmt.Sprintf("action[getDentry] clusterID[%s] volumeName[%s] partitionID[%v]" +
				" get dentry failed witch rocksdb error[dentry:%v]", mp.manager.metaNode.clusterId, mp.config.VolName,
				mp.config.PartitionId, dentry))
		}
		status = proto.OpErr
		return nil, status, err
	}
	if d == nil {
		status = proto.OpNotExistErr
		return nil, status, nil
	}
	log.LogDebugf("action[getDentry] get dentry[%v] by req dentry %v", d, dentry)

	den := mp.getDentryByVerSeq(d, dentry.getSeqFiled())
	if den != nil {
		return den, proto.OpOk, nil
	}
	return den, proto.OpNotExistErr, nil
}

func (mp *metaPartition) fsmTxDeleteDentry(dbHandle interface{}, txDentry *TxDentry) (resp *DentryResponse, err error) {
	resp = NewDentryResponse()
	var (
		item      *Dentry
	)
	resp.Status = proto.OpOk
	if mp.txProcessor.txManager.txInRMDone(txDentry.TxInfo.TxID) {
		log.LogWarnf("fsmTxDeleteDentry: tx is already finish. txId %s", txDentry.TxInfo.TxID)
		resp.Status = proto.OpTxInfoNotExistErr
		return
	}

	tmpDen := txDentry.Dentry
	txDI := proto.NewTxDentryInfo("", tmpDen.ParentId, tmpDen.Name, 0)
	txDenInfo, ok := txDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		resp.Status = proto.OpTxDentryInfoNotExistErr
		return
	}

	rbDentry := NewTxRollbackDentry(tmpDen, txDenInfo, TxAdd)
	resp.Status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry)
	if resp.Status == proto.OpExistErr {
		resp.Status = proto.OpOk
		return
	}

	if resp.Status != proto.OpOk {
		return
	}

	defer func() {
		if resp.Status != proto.OpOk {
			mp.txProcessor.txResource.deleteTxRollbackDentry(txDenInfo.ParentId, txDenInfo.Name, txDenInfo.TxID)
		}
	}()

	item, err = mp.dentryTree.Get(tmpDen.ParentId, tmpDen.Name)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}
	if item == nil || item.Inode != tmpDen.Inode {
		log.LogWarnf("fsmTxDeleteDentry: got wrong dentry, want %v, got %v", tmpDen, item)
		resp.Status = proto.OpNotExistErr
		return
	}

	if ok, err = mp.dentryTree.Delete(dbHandle, tmpDen.ParentId, tmpDen.Name); err != nil {
		resp.Status = proto.OpErr
		return
	}
	// parent link count not change
	resp.Msg = item
	return
}

// Delete dentry from the dentry tree.
func (mp *metaPartition) fsmDeleteDentry(dbHandle interface{}, denParm *Dentry, checkInode bool) (resp *DentryResponse, err error) {
	log.LogDebugf("action[fsmDeleteDentry] mp [%v] delete param (%v) seq %v", mp.config.PartitionId, denParm, denParm.getSeqFiled())
	resp = NewDentryResponse()
	resp.Status = proto.OpOk
	var (
		denFound *Dentry
		item     *Dentry
		doMore   = true
		clean    bool
		d      *Dentry
		parIno *Inode
	)

	d, err = mp.dentryTree.Get(denParm.ParentId, denParm.Name)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}
	if d == nil {
		resp.Status = proto.OpNotExistErr
		return
	}
	if checkInode {
		log.LogDebugf("action[fsmDeleteDentry] mp %v delete param %v", mp.config.PartitionId, denParm)
		den := d
		if den.Inode != denParm.Inode {
			item = nil
		} else {
			if mp.verSeq == 0 {
				log.LogDebugf("action[fsmDeleteDentry] mp %v volume snapshot not enabled,delete directly", mp.config.PartitionId)
				denFound = den
			} else {
				denFound, doMore, clean = den.deleteVerSnapshot(denParm.getSeqFiled(), mp.verSeq, mp.GetVerList())
			}
		}

		item = den
	} else {
		log.LogDebugf("action[fsmDeleteDentry] mp %v denParm dentry %v", mp.config.PartitionId, denParm)
		if mp.verSeq == 0 {
			item = d
			if item != nil {
				denFound = item
			}
		} else {
			item = d
			if item != nil {
				denFound, doMore, clean = item.deleteVerSnapshot(denParm.getSeqFiled(), mp.verSeq, mp.GetVerList())
			}
		}
	}

	if item != nil && (clean == true || (item.getSnapListLen() == 0 && item.isDeleted())) {
		log.LogDebugf("action[fsmDeleteDentry] mp %v dnetry %v really be deleted", mp.config.PartitionId, item)
		mp.dentryTree.Delete(dbHandle, item.ParentId, item.Name)
	}

	if !doMore { // not the top layer,do nothing to parent inode
		if denFound != nil {
			resp.Msg = denFound
		}
		log.LogDebugf("action[fsmDeleteDentry] mp %v there's nothing to do more denParm %v", mp.config.PartitionId, denParm)
		return
	}
	if denFound == nil {
		resp.Status = proto.OpNotExistErr
		log.LogErrorf("action[fsmDeleteDentry] mp %v not found dentry %v", mp.config.PartitionId, denParm)
		return
	} else {
		if parIno, err = mp.inodeTree.Get(denFound.ParentId); err != nil {
			log.LogErrorf("action[fsmDeleteDentry] get parent inode(%v) failed:%v", denFound.ParentId, err)
		}
		if parIno != nil {
			if !parIno.ShouldDelete() {
				parIno.DecNLink()
				if err = mp.inodeTree.Update(dbHandle, parIno); err != nil {
					log.LogErrorf("action[fsmDeleteDentry] update parent inode(%v) info failed:%v", denFound.ParentId, err)
					resp.Status = proto.OpErr
					return
				}
			}
		}
	}
	resp.Msg = denFound
	return
}

// batch Delete dentry from the dentry tree.
func (mp *metaPartition) fsmBatchDeleteDentry(db DentryBatch) (result []*DentryResponse, err error) {
	result = make([]*DentryResponse, 0, len(db))
	var wrongIndex int
	defer func() {
		if err != nil {
			for index := wrongIndex; index < len(db); index++ {
				result = append(result, &DentryResponse{Status: proto.OpErr, Msg: db[index]})
			}
		}
	}()

	for index, dentry := range db {
		var (
			rsp           *DentryResponse
			dbWriteHandle interface{}
		)
		status := mp.dentryInTx(dentry.ParentId, dentry.Name)
		if status != proto.OpOk {
			result = append(result, &DentryResponse{Status: status})
			continue
		}
		dbWriteHandle, err = mp.dentryTree.CreateBatchWriteHandle()
		if err != nil {
			wrongIndex = index
			break
		}
		rsp, err = mp.fsmDeleteDentry(dbWriteHandle, dentry, false)
		if err != nil {
			_ = mp.dentryTree.ReleaseBatchWriteHandle(dbWriteHandle)
			wrongIndex = index
			break
		}
		err = mp.dentryTree.CommitAndReleaseBatchWriteHandle(dbWriteHandle, false)
		if err != nil {
			wrongIndex = index
			break
		}
		result = append(result, rsp)
	}
	return
}

func (mp *metaPartition) fsmTxUpdateDentry(dbHandle interface{}, txUpDateDentry *TxUpdateDentry) (resp *DentryResponse, err error) {
	resp = NewDentryResponse()
	resp.Status = proto.OpOk
	var item *Dentry

	if mp.txProcessor.txManager.txInRMDone(txUpDateDentry.TxInfo.TxID) {
		log.LogWarnf("fsmTxUpdateDentry: tx is already finish. txId %s", txUpDateDentry.TxInfo.TxID)
		resp.Status = proto.OpTxInfoNotExistErr
		return
	}

	newDen := txUpDateDentry.NewDentry
	oldDen := txUpDateDentry.OldDentry

	txDI := proto.NewTxDentryInfo("", oldDen.ParentId, oldDen.Name, 0)
	txDenInfo, ok := txUpDateDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		resp.Status = proto.OpTxDentryInfoNotExistErr
		return
	}

	item, err = mp.dentryTree.Get(oldDen.ParentId, oldDen.Name)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}

	if item == nil || item.Inode != oldDen.Inode {
		resp.Status = proto.OpNotExistErr
		log.LogWarnf("fsmTxUpdateDentry: find dentry is not right, want %v, got %v", oldDen, item)
		return
	}

	rbDentry := NewTxRollbackDentry(txUpDateDentry.OldDentry, txDenInfo, TxUpdate)
	resp.Status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry)
	if resp.Status == proto.OpExistErr {
		resp.Status = proto.OpOk
		return
	}

	if resp.Status != proto.OpOk {
		return
	}

	d := item
	d.Inode, newDen.Inode = newDen.Inode, d.Inode
	if err = mp.dentryTree.Update(dbHandle, d); err != nil {
		resp.Status = proto.OpErr
		return
	}
	resp.Msg = newDen
	return
}

func (mp *metaPartition) fsmUpdateDentry(dbHandle interface{}, dentry *Dentry) (
	resp *DentryResponse, err error) {
	resp = NewDentryResponse()
	resp.Status = proto.OpOk
	var item *Dentry
	item, err = mp.dentryTree.Get(dentry.ParentId, dentry.Name)
	if err != nil {
		resp.Status = proto.OpErr
		return
	}

	if item == nil || item.Inode != dentry.Inode {
		resp.Status = proto.OpNotExistErr
		log.LogWarnf("fsmTxUpdateDentry: find dentry is not right, want %v, got %v", dentry, item)
		return
	}

	d := item
	if d.getVerSeq() < mp.GetVerSeq() {
		dn := d.CopyDirectly()
		dn.(*Dentry).setVerSeq(d.getVerSeq())
		d.setVerSeq(mp.GetVerSeq())
		d.multiSnap.dentryList = append([]*Dentry{dn.(*Dentry)}, d.multiSnap.dentryList...)
	}
	d.Inode, dentry.Inode = dentry.Inode, d.Inode
	resp.Msg = dentry
	if err = mp.dentryTree.Update(dbHandle, d); err != nil {
		resp.Status = proto.OpErr
		return
	}
	return
}

func (mp *metaPartition) getDentryByVerSeq(dy *Dentry, verSeq uint64) (d *Dentry) {
	d, _ = dy.getDentryFromVerList(verSeq)
	return
}

func (mp *metaPartition) readDirOnly(req *ReadDirOnlyReq) (resp *ReadDirOnlyResp, err error) {
	resp = &ReadDirOnlyResp{}
	begDentry := &Dentry{
		ParentId: req.ParentID,
	}
	endDentry := &Dentry{
		ParentId: req.ParentID + 1,
	}

	err = mp.dentryTree.RangeWithPrefix(&Dentry{ParentId: req.ParentID}, begDentry, endDentry, func(den *Dentry) (bool, error) {
		if proto.IsDir(den.Type) {
			d := mp.getDentryByVerSeq(den, req.VerSeq)
			if d == nil {
				return true, nil
			}
			resp.Children = append(resp.Children, proto.Dentry{
				Inode: d.Inode,
				Type:  d.Type,
				Name:  d.Name,
			})
		}
		return true, nil
	})
	if err != nil {
		log.LogErrorf("readDir failed:[%s]", err.Error())
		return
	}
	return
}

func (mp *metaPartition) readDir(req *ReadDirReq) (resp *ReadDirResp, err error) {
	resp = &ReadDirResp{}
	begDentry := &Dentry{
		ParentId: req.ParentID,
	}
	endDentry := &Dentry{
		ParentId: req.ParentID + 1,
	}

	err = mp.dentryTree.RangeWithPrefix(&Dentry{ParentId: req.ParentID}, begDentry, endDentry, func(den *Dentry) (bool, error) {
		d := mp.getDentryByVerSeq(den, req.VerSeq)
		if d == nil {
			return true, nil
		}
		resp.Children = append(resp.Children, proto.Dentry{
			Inode: d.Inode,
			Type:  d.Type,
			Name:  d.Name,
		})
		return true, nil
	})
	if err != nil {
		log.LogErrorf("readDir failed:[%s]", err.Error())
		return
	}
	return
}

// Read dentry from btree by limit count
// if req.Marker == "" and req.Limit == 0, it becomes readDir
// else if req.Marker != "" and req.Limit == 0, return dentries from pid:name to pid+1
// else if req.Marker == "" and req.Limit != 0, return dentries from pid with limit count
// else if req.Marker != "" and req.Limit != 0, return dentries from pid:marker to pid:xxxx with limit count
//
func (mp *metaPartition) readDirLimit(req *ReadDirLimitReq) (resp *ReadDirLimitResp, err error) {
	log.LogDebugf("action[readDirLimit] mp %v req %v", mp.config.PartitionId, req)
	resp = &ReadDirLimitResp{}
	startDentry := &Dentry{
		ParentId: req.ParentID,
	}
	if len(req.Marker) > 0 {
		startDentry.Name = req.Marker
	}
	endDentry := &Dentry{
		ParentId: req.ParentID + 1,
	}

	err = mp.dentryTree.RangeWithPrefix(&Dentry{ParentId: req.ParentID}, startDentry, endDentry, func(den *Dentry) (bool, error) {
		if !proto.IsDir(den.Type) && (req.VerOpt&uint8(proto.FlagsSnapshotDel) > 0) {
			if req.VerOpt&uint8(proto.FlagsSnapshotDelDir) > 0 {
				return true, nil
			}
			if !den.isEffective(req.VerSeq) {
				return true, nil
			}
		}
		d := mp.getDentryByVerSeq(den, req.VerSeq)
		if d == nil {
			return true, nil
		}
		resp.Children = append(resp.Children, proto.Dentry{
			Inode: d.Inode,
			Type:  d.Type,
			Name:  d.Name,
		})
		// Limit == 0 means no limit.
		if req.Limit > 0 && uint64(len(resp.Children)) >= req.Limit {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		log.LogErrorf("readDir failed:[%s]", err.Error())
		return
	}
	log.LogDebugf("action[readDirLimit] mp %v resp %v", mp.config.PartitionId, resp)
	return
}

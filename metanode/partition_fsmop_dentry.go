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
	"strings"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/btree"
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

func (mp *metaPartition) fsmTxCreateDentry(txDentry *TxDentry, forceUpdate bool) (status uint8) {
	//1.if mpID == -1, register transaction in transaction manager
	//if txDentry.TxInfo.TxID != "" && txDentry.TxInfo.TmID == -1 {
	_ = mp.txProcessor.txManager.registerTransaction(txDentry.TxInfo)
	//}
	//2.register rollback item
	txDI := proto.NewTxDentryInfo("", txDentry.Dentry.ParentId, txDentry.Dentry.Name, 0)

	//txDenInfo := mp.txProcessor.txManager.getTxDentryInfo(txDentry.TxInfo.TxID, txDI.GetKey())
	txDenInfo, ok := txDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		status = proto.OpTxDentryInfoNotExistErr
		return
	}
	rbDentry := NewTxRollbackDentry(txDentry.Dentry, txDenInfo, TxDelete)
	if status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry); status != proto.OpOk {
		//status = proto.OpTxConflictErr
		return
	}

	//3. register rollback parent inode item
	parInodeInfo, ok := txDentry.TxInfo.TxInodeInfos[txDentry.ParInode.Inode]
	if !ok {
		status = proto.OpTxInodeInfoNotExistErr
		return
	}
	quotaIds, _ := mp.isExistQuota(txDentry.ParInode.Inode)

	rbParInode := NewTxRollbackInode(txDentry.ParInode, quotaIds, parInodeInfo, TxUpdate)
	if status = mp.txProcessor.txResource.addTxRollbackInode(rbParInode); status != proto.OpOk {
		//status = proto.OpTxConflictErr
		return
	}

	return mp.fsmCreateDentry(txDentry.Dentry, forceUpdate)
}

// Insert a dentry into the dentry tree.
func (mp *metaPartition) fsmCreateDentry(dentry *Dentry,
	forceUpdate bool) (status uint8) {
	status = proto.OpOk

	log.LogDebugf("action[fsmCreateDentry] ParentId [%v], dentry name [%v], inode [%v], verseq [%v]", dentry.ParentId, dentry.Name, dentry.Inode, dentry.VerSeq)
	var parIno *Inode
	if !forceUpdate {
		item := mp.inodeTree.CopyGet(NewInode(dentry.ParentId, 0))
		if item == nil {
			log.LogErrorf("action[fsmCreateDentry] ParentId [%v] get nil, dentry name [%v], inode [%v]", dentry.ParentId, dentry.Name, dentry.Inode)
			status = proto.OpNotExistErr
			return
		}
		parIno = item.(*Inode)
		if parIno.ShouldDelete() {
			log.LogErrorf("action[fsmCreateDentry] ParentId [%v] get [%v] but should del, dentry name [%v], inode [%v]", dentry.ParentId, parIno, dentry.Name, dentry.Inode)
			status = proto.OpNotExistErr
			return
		}
		if !proto.IsDir(parIno.Type) {
			log.LogErrorf("action[fsmCreateDentry] ParentId [%v] get [%v] but should del, dentry name [%v], inode [%v]", dentry.ParentId, parIno, dentry.Name, dentry.Inode)
			status = proto.OpArgMismatchErr
			return
		}
	}

	if item, ok := mp.dentryTree.ReplaceOrInsert(dentry, false); !ok {
		//do not allow directories and files to overwrite each
		// other when renaming
		d := item.(*Dentry)
		if d.isDeleted() {
			log.LogDebugf("action[fsmCreateDentry] newest dentry %v be set deleted flag", d)
			d.Inode = dentry.Inode
			d.VerSeq = dentry.VerSeq
			d.Type = dentry.Type
			d.ParentId = dentry.ParentId
			log.LogDebugf("action[fsmCreateDentry.ver] latest dentry already deleted.Now create new one [%v]", dentry)

			if !forceUpdate {
				parIno.IncNLink(mp.verSeq)
				parIno.SetMtime()
			}
			return
		} else if proto.OsModeType(dentry.Type) != proto.OsModeType(d.Type) {
			log.LogDebugf("action[fsmCreateDentry.ver] OpArgMismatchErr [%v] [%v]", proto.OsModeType(dentry.Type), proto.OsModeType(d.Type))
			status = proto.OpArgMismatchErr
			return
		} else if dentry.ParentId == d.ParentId && strings.Compare(dentry.Name, d.Name) == 0 && dentry.Inode == d.Inode {
			log.LogDebugf("action[fsmCreateDentry.ver] no need repeat create new one [%v]", dentry)
			return
		}
		log.LogErrorf("action[fsmCreateDentry.ver] dentry already exist [%v] and diff with the request [%v]", d, dentry)
		status = proto.OpExistErr
	} else {
		if !forceUpdate {
			parIno.IncNLink(mp.verSeq)
			parIno.SetMtime()
		}
	}

	return
}

func (mp *metaPartition) getDentryList(dentry *Dentry) (denList []proto.DetryInfo) {
	item := mp.dentryTree.Get(dentry)
	if item != nil {
		for _, den := range item.(*Dentry).dentryList {
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
func (mp *metaPartition) getDentry(dentry *Dentry) (*Dentry, uint8) {
	status := proto.OpOk
	item := mp.dentryTree.Get(dentry)
	if item == nil {
		status = proto.OpNotExistErr
		return nil, status
	}
	log.LogDebugf("action[getDentry] get dentry[%v] by req dentry %v", item.(*Dentry), dentry)

	den := mp.getDentryByVerSeq(item.(*Dentry), dentry.VerSeq)
	if den != nil {
		return den, proto.OpOk
	}
	return den, proto.OpNotExistErr
}

func (mp *metaPartition) fsmTxDeleteDentry(txDentry *TxDentry, checkInode bool) (resp *DentryResponse) {
	resp = NewDentryResponse()
	resp.Status = proto.OpOk

	//1.if mpID == -1, register transaction in transaction manager
	//if txDentry.TxInfo.TxID != "" && txDentry.TxInfo.TmID == -1 {
	_ = mp.txProcessor.txManager.registerTransaction(txDentry.TxInfo)
	//}
	//2.register rollback dentry item
	txDI := proto.NewTxDentryInfo("", txDentry.Dentry.ParentId, txDentry.Dentry.Name, 0)
	txDenInfo, ok := txDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		resp.Status = proto.OpTxDentryInfoNotExistErr
		return
	}
	rbDentry := NewTxRollbackDentry(txDentry.Dentry, txDenInfo, TxAdd)
	if resp.Status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry); resp.Status != proto.OpOk {
		//resp.Status = proto.OpTxConflictErr
		return
	}

	//3. register rollback parent inode item
	parInodeInfo, ok := txDentry.TxInfo.TxInodeInfos[txDentry.ParInode.Inode]
	if !ok {
		resp.Status = proto.OpTxInodeInfoNotExistErr
		return
	}
	quotaIds, _ := mp.isExistQuota(txDentry.ParInode.Inode)

	rbParInode := NewTxRollbackInode(txDentry.ParInode, quotaIds, parInodeInfo, TxUpdate)
	if resp.Status = mp.txProcessor.txResource.addTxRollbackInode(rbParInode); resp.Status != proto.OpOk {
		//resp.Status = proto.OpTxConflictErr
		return
	}

	return mp.fsmDeleteDentry(txDentry.Dentry, checkInode)
}

// Delete dentry from the dentry tree.
func (mp *metaPartition) fsmDeleteDentry(denParm *Dentry, checkInode bool) (resp *DentryResponse) {

	log.LogDebugf("action[fsmDeleteDentry] delete param (%v) seq %v", denParm, denParm.VerSeq)
	resp = NewDentryResponse()
	resp.Status = proto.OpOk

	if denParm.VerSeq != 0 {
		if err := mp.checkAndUpdateVerList(denParm.VerSeq); err != nil {
			resp.Status = proto.OpNotExistErr
			log.LogErrorf("action[fsmDeleteDentry] dentry %v err %v", denParm, err)
			return
		}
	}

	var (
		item   interface{}
		doMore = true
		clean  bool
	)
	if checkInode {
		log.LogDebugf("action[fsmDeleteDentry] delete param %v", denParm)
		item = mp.dentryTree.Execute(func(tree *btree.BTree) interface{} {
			d := tree.CopyGet(denParm)
			if d == nil {
				return nil
			}
			den := d.(*Dentry)
			if den.Inode != denParm.Inode {
				return nil
			}
			if mp.verSeq == 0 {
				log.LogDebugf("action[fsmDeleteDentry] volume snapshot not enabled,delete directly")
				return mp.dentryTree.tree.Delete(den)
			}
			_, doMore, clean = den.deleteVerSnapshot(denParm.VerSeq, mp.verSeq, mp.getVerList())
			return den
		})
	} else {
		log.LogDebugf("action[fsmDeleteDentry] denParm dentry %v", denParm)

		if mp.verSeq == 0 {
			item = mp.dentryTree.Delete(denParm)
		} else {
			item = mp.dentryTree.Get(denParm)
			if item != nil {
				_, doMore, clean = item.(*Dentry).deleteVerSnapshot(denParm.VerSeq, mp.verSeq, mp.getVerList())
			}
		}
	}

	if item != nil && (clean == true || (len(item.(*Dentry).dentryList) == 0 && item.(*Dentry).isDeleted())) {
		log.LogDebugf("action[fsmDeleteDentry] dnetry %v really be deleted", item.(*Dentry))
		item = mp.dentryTree.Delete(item.(*Dentry))
	}

	if !doMore { // not the top layer,do nothing to parent inode
		if item != nil {
			resp.Msg = item.(*Dentry)
		}
		log.LogDebugf("action[fsmDeleteDentry] there's nothing to do more denParm %v", denParm)
		return
	}
	if item == nil {
		resp.Status = proto.OpNotExistErr
		log.LogErrorf("action[fsmDeleteDentry] not found dentry %v", denParm)
		return
	} else {
		mp.inodeTree.CopyFind(NewInode(denParm.ParentId, 0),
			func(item BtreeItem) {
				if item != nil { // no matter
					ino := item.(*Inode)
					if !ino.ShouldDelete() {
						log.LogDebugf("action[fsmDeleteDentry] den  %v delete parent's link", denParm)
						if denParm.VerSeq == 0 {
							item.(*Inode).DecNLink()
						}
						log.LogDebugf("action[fsmDeleteDentry] inode %v be unlinked by child name %v", item.(*Inode).Inode, denParm.Name)
						item.(*Inode).SetMtime()
					}
				}
			})
	}
	resp.Msg = item.(*Dentry)
	return
}

// batch Delete dentry from the dentry tree.
func (mp *metaPartition) fsmBatchDeleteDentry(db DentryBatch) []*DentryResponse {
	result := make([]*DentryResponse, 0, len(db))
	for _, dentry := range db {
		result = append(result, mp.fsmDeleteDentry(dentry, true))
	}
	return result
}

func (mp *metaPartition) fsmTxUpdateDentry(txUpDateDentry *TxUpdateDentry) (resp *DentryResponse) {
	resp = NewDentryResponse()
	resp.Status = proto.OpOk
	//1.if mpID == -1, register transaction in transaction manager
	//if txDentry.TxInfo.TxID != "" && txDentry.TxInfo.TmID == -1 {
	_ = mp.txProcessor.txManager.registerTransaction(txUpDateDentry.TxInfo)
	//}

	//2.register rollback item
	txDI := proto.NewTxDentryInfo("", txUpDateDentry.OldDentry.ParentId, txUpDateDentry.OldDentry.Name, 0)

	//txDenInfo := mp.txProcessor.txManager.getTxDentryInfo(txDentry.TxInfo.TxID, txDI.GetKey())
	txDenInfo, ok := txUpDateDentry.TxInfo.TxDentryInfos[txDI.GetKey()]
	if !ok {
		resp.Status = proto.OpTxDentryInfoNotExistErr
		return
	}
	rbDentry := NewTxRollbackDentry(txUpDateDentry.OldDentry, txDenInfo, TxUpdate)
	if resp.Status = mp.txProcessor.txResource.addTxRollbackDentry(rbDentry); resp.Status != proto.OpOk {
		//resp.Status = proto.OpTxConflictErr
		return
	}

	return mp.fsmUpdateDentry(txUpDateDentry.NewDentry)
}

func (mp *metaPartition) fsmUpdateDentry(dentry *Dentry) (
	resp *DentryResponse) {
	resp = NewDentryResponse()
	resp.Status = proto.OpOk
	mp.dentryTree.CopyFind(dentry, func(item BtreeItem) {
		if item == nil {
			resp.Status = proto.OpNotExistErr
			return
		}
		d := item.(*Dentry)
		d.Inode, dentry.Inode = dentry.Inode, d.Inode
		resp.Msg = dentry
	})
	return
}

func (mp *metaPartition) getDentryTree() *BTree {
	return mp.dentryTree.GetTree()
}

func (mp *metaPartition) getDentryByVerSeq(dy *Dentry, verSeq uint64) (d *Dentry) {
	log.LogInfof("action[getDentryFromVerList] verseq %v, tmp dentry %v, inode id %v, name %v", verSeq, dy.VerSeq, dy.Inode, dy.Name)
	d, _ = dy.getDentryFromVerList(verSeq)
	return
}

func (mp *metaPartition) readDirOnly(req *ReadDirOnlyReq) (resp *ReadDirOnlyResp) {
	resp = &ReadDirOnlyResp{}
	begDentry := &Dentry{
		ParentId: req.ParentID,
	}
	endDentry := &Dentry{
		ParentId: req.ParentID + 1,
	}
	mp.dentryTree.AscendRange(begDentry, endDentry, func(i BtreeItem) bool {
		if proto.IsDir(i.(*Dentry).Type) {
			d := mp.getDentryByVerSeq(i.(*Dentry), req.VerSeq)
			if d == nil {
				return true
			}
			resp.Children = append(resp.Children, proto.Dentry{
				Inode: d.Inode,
				Type:  d.Type,
				Name:  d.Name,
			})
		}
		return true
	})
	return
}

func (mp *metaPartition) readDir(req *ReadDirReq) (resp *ReadDirResp) {
	resp = &ReadDirResp{}
	begDentry := &Dentry{
		ParentId: req.ParentID,
	}
	endDentry := &Dentry{
		ParentId: req.ParentID + 1,
	}
	mp.dentryTree.AscendRange(begDentry, endDentry, func(i BtreeItem) bool {
		d := mp.getDentryByVerSeq(i.(*Dentry), req.VerSeq)
		if d == nil {
			return true
		}
		resp.Children = append(resp.Children, proto.Dentry{
			Inode: d.Inode,
			Type:  d.Type,
			Name:  d.Name,
		})
		return true
	})
	return
}

// Read dentry from btree by limit count
// if req.Marker == "" and req.Limit == 0, it becomes readDir
// else if req.Marker != "" and req.Limit == 0, return dentries from pid:name to pid+1
// else if req.Marker == "" and req.Limit != 0, return dentries from pid with limit count
// else if req.Marker != "" and req.Limit != 0, return dentries from pid:marker to pid:xxxx with limit count
//
func (mp *metaPartition) readDirLimit(req *ReadDirLimitReq) (resp *ReadDirLimitResp) {
	log.LogDebugf("action[readDirLimit] req %v", req)
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
	mp.dentryTree.AscendRange(startDentry, endDentry, func(i BtreeItem) bool {
		if !proto.IsDir(i.(*Dentry).Type) && req.VerDel && !i.(*Dentry).isEffective(req.VerSeq) {
			return true
		}
		d := mp.getDentryByVerSeq(i.(*Dentry), req.VerSeq)
		if d == nil {
			return true
		}
		resp.Children = append(resp.Children, proto.Dentry{
			Inode: d.Inode,
			Type:  d.Type,
			Name:  d.Name,
		})
		// Limit == 0 means no limit.
		if req.Limit > 0 && uint64(len(resp.Children)) >= req.Limit {
			return false
		}
		return true
	})
	log.LogDebugf("action[readDirLimit] resp %v", resp)
	return
}

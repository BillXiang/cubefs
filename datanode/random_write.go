// Copyright 2018 The ChuBao Authors.
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

package datanode

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"

	"github.com/tiglabs/containerfs/proto"
	"github.com/tiglabs/containerfs/repl"
	"github.com/tiglabs/containerfs/storage"
	"github.com/tiglabs/containerfs/util/log"
	"strings"
	"fmt"
)

type RndWrtCmdItem struct {
	Op uint32 `json:"op"`
	K  []byte `json:"k"`
	V  []byte `json:"v"`
}

type rndWrtOpItem struct {
	extentID uint64
	offset   int64
	size     int64
	data     []byte
	crc      uint32
}

// Marshal random write value to binary data.
// Binary frame structure:
//  +------+----+------+------+------+------+------+
//  | Item | extentID | offset | size | crc | data |
//  +------+----+------+------+------+------+------+
//  | byte |     8    |    8   |  8   |  4  | size |
//  +------+----+------+------+------+------+------+
func rndWrtDataMarshal(extentID uint64, offset, size int64, data []byte, crc uint32) (result []byte, err error) {
	buff := bytes.NewBuffer(make([]byte, 0))
	buff.Grow(8 + 8*2 + 4 + int(size))
	if err = binary.Write(buff, binary.BigEndian, extentID); err != nil {
		return
	}
	if err = binary.Write(buff, binary.BigEndian, offset); err != nil {
		return
	}
	if err = binary.Write(buff, binary.BigEndian, size); err != nil {
		return
	}
	if err = binary.Write(buff, binary.BigEndian, crc); err != nil {
		return
	}
	if _, err = buff.Write(data); err != nil {
		return
	}
	result = buff.Bytes()
	return
}

func rndWrtDataUnmarshal(raw []byte) (result *rndWrtOpItem, err error) {
	var opItem rndWrtOpItem

	buff := bytes.NewBuffer(raw)
	if err = binary.Read(buff, binary.BigEndian, &opItem.extentID); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &opItem.offset); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &opItem.size); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &opItem.crc); err != nil {
		return
	}
	opItem.data = make([]byte, opItem.size)
	if _, err = buff.Read(opItem.data); err != nil {
		return
	}

	result = &opItem
	return
}

func (rndWrtItem *RndWrtCmdItem) rndWrtCmdMarshalJSON() (cmd []byte, err error) {
	return json.Marshal(rndWrtItem)
}

func (rndWrtItem *RndWrtCmdItem) rndWrtCmdUnmarshal(cmd []byte) (err error) {
	return json.Unmarshal(cmd, rndWrtItem)
}

// RandomWriteSubmit Submit the propose to raft.
func (dp *DataPartition) RandomWriteSubmit(pkg *repl.Packet) (err error) {
	val, err := rndWrtDataMarshal(pkg.ExtentID, pkg.ExtentOffset, int64(pkg.Size), pkg.Data, pkg.CRC)
	if err != nil {
		return
	}
	resp, err := dp.Put(opRandomWrite, val)
	if err != nil {
		return
	}

	pkg.ResultCode = resp.(uint8)

	log.LogDebugf("[randomWrite] SubmitRaft: req_%v_%v_%v_%v_%v response = %v.",
		dp.partitionID, pkg.ExtentID, pkg.ExtentOffset, pkg.Size, pkg.CRC, pkg.GetResultMesg())
	return
}

func (dp *DataPartition) checkWriteErrs(errMsg string) (ignore bool) {
	// file has deleted when raft log apply
	if strings.Contains(errMsg, storage.ErrorExtentHasDelete.Error()) {
		return true
	}
	return false
}

func (dp *DataPartition) addDiskErrs(err error, flag uint8) {
	if err == nil {
		return
	}

	d := dp.Disk()
	if d == nil {
		return
	}
	if !IsDiskErr(err.Error()) {
		return
	}
	if flag == WriteFlag {
		d.addWriteErr()
	}
}

func (dp *DataPartition) RandomPartitionReadCheck(request *repl.Packet, connect net.Conn) (err error) {
	if !dp.config.RandomWrite || request.Opcode == proto.OpExtentRepairRead {
		return
	}
	_, ok := dp.IsRaftLeader()
	if !ok {
		err = storage.ErrNotLeader
		logContent := fmt.Sprintf("action[ReadCheck] %v.", request.LogMessage(request.GetOpMsg(), connect.RemoteAddr().String(), request.StartT, err))
		log.LogInfof(logContent)
		return
	}

	if dp.applyID < dp.maxAppliedID {
		err = storage.ErrorAgain
		logContent := fmt.Sprintf("action[ReadCheck] %v localID=%v maxID=%v.",
			request.LogMessage(request.GetOpMsg(), connect.RemoteAddr().String(), request.StartT, nil), dp.applyID, dp.maxAppliedID)
		log.LogErrorf(logContent)
		return
	}

	return
}

type ItemIterator struct {
	applyID uint64
}

func NewItemIterator(applyID uint64) *ItemIterator {
	si := new(ItemIterator)
	si.applyID = applyID
	return si
}

func (si *ItemIterator) ApplyIndex() uint64 {
	return si.applyID
}

func (si *ItemIterator) Close() {
	return
}

func (si *ItemIterator) Next() (data []byte, err error) {
	appIDBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(appIDBuf, si.applyID)
	data = appIDBuf[:]
	return
}

package metanode

import (
	"context"
	"github.com/chubaofs/chubaofs/proto"
	"os"
	"testing"
	"time"
)

func TestDeletedINode_Copy(t *testing.T) {
}

func mockINode(id uint64) *Inode {
	ino := new(Inode)
	ino.Inode = id
	ino.Type = proto.Mode(os.ModeDir)
	ino.Uid = 500
	ino.Gid = 501
	ino.Size = 1024
	ino.Generation = 2
	ino.CreateTime = time.Now().Unix()
	ino.ModifyTime = time.Now().Unix() + 1
	ino.AccessTime = time.Now().Unix() + 2
	ino.LinkTarget = []byte("link")
	ino.NLink = 2
	ino.Flag = 1
	ino.Reserved = 1024 * 1024
	ino.Extents = NewSortedExtents()
	var i uint64
	for i = 1; i < 5; i++ {
		var ek proto.ExtentKey
		ek.Size = uint32(1024 * i)
		ek.FileOffset = uint64(1024 * (i - 1))
		ek.ExtentOffset = i
		ek.ExtentId = i
		ek.CRC = uint32(10 * i)
		ek.PartitionId = i
		ino.Extents.eks = append(ino.Extents.eks, ek)
	}
	return ino
}

func TestNewDeletedInode(t *testing.T) {
	ino := mockINode(2)
	di := NewDeletedInode(ino, time.Now().UnixNano()/1000)
	compareTestInode2(ino, di.buildInode(), t)
}

func TestDeletedINode_Less(t *testing.T) {
	ino1 := NewInode(10, 1)
	dino1 := NewDeletedInode(ino1, ts)

	ino2 := NewInode(11, 1)
	dino2 := NewDeletedInode(ino2, ts)

	if dino1.Less(dino2) == false {
		t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
		t.FailNow()
	}

	ino2 = NewInode(10, 1)
	dino2 = NewDeletedInode(ino2, ts+100)
	if dino1.Less(dino2) == true {
		t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
		t.FailNow()
	}

	dino2 = NewDeletedInodeByID(11)
	if dino1.Less(dino2) == false {
		t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
		t.FailNow()
	}

	dino2 = NewDeletedInodeByID(9)
	if dino1.Less(dino2) == true {
		t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
		t.FailNow()
	}

	/*
		dino2 = NewDeletedInodeByTimestamp(ts-100)
		if dino1.Less(dino2) == true {
			t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
			t.FailNow()
		}

		dino2 = NewDeletedInodeByTimestamp(ts+100)
		if dino1.Less(dino2) == false {
			t.Errorf("dino1: %v, dino2: %v", dino1, dino2)
			t.FailNow()
		}

	*/

	mp := new(metaPartition)
	mp.inodeTree = mockInodeTree()
	mp.inodeDeletedTree = mockDeletedInodeTree()

	ino := NewInode(9, 0)
	item := mp.inodeTree.Get(ino)
	if item != nil {
		t.Errorf("found inode: %v", ino)
		t.FailNow()
	}

	ino = NewInode(10, 0)
	item = mp.inodeTree.Get(ino)
	if item == nil {
		t.Errorf("not found inode: %v", ino)
		t.FailNow()
	}

	ino = NewInode(30, 0)
	item = mp.inodeTree.Get(ino)
	if item != nil {
		t.Errorf("found inode: %v", ino)
		t.FailNow()
	}

	dino := NewDeletedInodeByID(9)
	item = mp.inodeDeletedTree.CopyGet(dino)
	if item != nil {
		t.Errorf("found inode: %v", ino)
		t.FailNow()
	}

	dino = NewDeletedInodeByID(10)
	item = mp.inodeDeletedTree.CopyGet(dino)
	if item == nil {
		t.Errorf("not found inode: %v", dino)
		t.FailNow()
	}

	dino = NewDeletedInodeByID(30)
	item = mp.inodeDeletedTree.CopyGet(dino)
	if item != nil {
		t.Errorf("found inode: %v", ino)
		t.FailNow()
	}

}

func TestDeletedINode_Marshal(t *testing.T) {
	ino := mockINode(1)
	dino := NewDeletedInode(ino, ts)

	data, err := dino.Marshal()
	if err != nil {
		t.Error(err.Error())
		t.FailNow()
	}

	newdino := new(DeletedINode)
	err = newdino.Unmarshal(context.Background(), data)
	if err != nil {
		t.Error(err.Error())
		t.FailNow()
	}

	compareTestInode(dino, newdino, t)
}

func compareTestInode(ino1 *DeletedINode, ino2 *DeletedINode, t *testing.T) {
	if ino1.NLink != ino2.NLink {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Inode.Inode != ino2.Inode.Inode {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Size != ino2.Size {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Generation != ino2.Generation {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Reserved != ino2.Reserved {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Type != ino2.Type {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.CreateTime != ino2.CreateTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.AccessTime != ino2.AccessTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.ModifyTime != ino2.ModifyTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Flag != ino2.Flag {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Gid != ino2.Gid {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Uid != ino2.Uid {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Extents.Len() != ino2.Extents.Len() {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	for index, ek1 := range ino1.Extents.eks {
		ek2 := ino2.Extents.eks[index]
		if ek1.Size != ek2.Size {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.CRC != ek2.CRC {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.CRC != ek2.CRC {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.PartitionId != ek2.PartitionId {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.ExtentOffset != ek2.ExtentOffset {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.ExtentId != ek2.ExtentId {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.FileOffset != ek2.FileOffset {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
	}
}

func compareTestInode2(ino1 *Inode, ino2 *Inode, t *testing.T) {
	if ino1.NLink != ino2.NLink {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Inode != ino2.Inode {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Size != ino2.Size {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Generation != ino2.Generation {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Reserved != ino2.Reserved {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Type != ino2.Type {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.CreateTime != ino2.CreateTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.AccessTime != ino2.AccessTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.ModifyTime != ino2.ModifyTime {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Flag != ino2.Flag {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Gid != ino2.Gid {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Uid != ino2.Uid {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	if ino1.Extents.Len() != ino2.Extents.Len() {
		t.Errorf("ino1: %v, ino2: %v", ino1, ino2)
		t.FailNow()
	}

	for index, ek1 := range ino1.Extents.eks {
		ek2 := ino2.Extents.eks[index]
		if ek1.Size != ek2.Size {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.CRC != ek2.CRC {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.CRC != ek2.CRC {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.PartitionId != ek2.PartitionId {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.ExtentOffset != ek2.ExtentOffset {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.ExtentId != ek2.ExtentId {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
		if ek1.FileOffset != ek2.FileOffset {
			t.Errorf("ino1: %v, ek1: %v, ino2: %v, ek2: %v", ino1, ek1, ino2, ek2)
			t.FailNow()
		}
	}
}

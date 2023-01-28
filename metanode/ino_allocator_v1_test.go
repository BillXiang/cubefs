package metanode

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestInoAllocatorV1_MaxCost(t *testing.T) {
	allocator := NewInoAllocatorV1(0, 1<<24 + uint64(rand.Int()) % 64)
	allocator.SetStatus(allocatorStatusInit)
	allocator.SetStatus(allocatorStatusAvailable)
	//set all 1
	for i := 0; i < len(allocator.Bits); i++ {
		allocator.Bits[i] = math.MaxUint64
	}

	allocator.ClearId(3)
	allocator.BitCursor = 4
	start := time.Now()
	id, err := allocator.AllocateId()
	if err != nil {
		t.Fatalf("allocate id failed")
		return
	}
	cost := time.Since(start)
	t.Logf("allocate id:%d, max cost:%v", id, cost)

	allocator.BitCursor = 256
	id, err = allocator.AllocateId()
	if err == nil {
		t.Fatalf("allocate id failed, expect err, but now success.id:%d", id)
		return
	}
	allocator.ClearId(allocator.Cnt - 1)
	id, err = allocator.AllocateId()
	if err != nil {
		t.Logf(allocator.Bits.GetU64BitInfo(len(allocator.Bits) - 1))
		t.Fatalf("allocate id failed")
		return
	}
	t.Logf(allocator.Bits.GetU64BitInfo(len(allocator.Bits) - 1))
	t.Logf("allocate max id:%d, cnt:%d, end:%d ", id, allocator.Cnt, allocator.End)
}

func TestInoAllocatorV1_NotU64Len(t *testing.T) {
	allocator := NewInoAllocatorV1(0, 1<<24 + 1)
	allocator.SetStatus(allocatorStatusInit)
	//set all 1

	allocator.SetId(3)

	t.Logf(allocator.Bits.GetU64BitInfo(0))
	t.Logf(allocator.Bits.GetU64BitInfo(len(allocator.Bits) - 1))
}

func TestInoAllocatorV1_U64Len(t *testing.T) {
	allocator := NewInoAllocatorV1(0, 1<<24)
	allocator.SetStatus(allocatorStatusInit)
	//set all 1

	allocator.SetId(3)
	t.Logf(allocator.Bits.GetU64BitInfo(0))
	t.Logf(allocator.Bits.GetU64BitInfo(len(allocator.Bits) - 1))
}

func InoAlloterv1UsedCnt(t *testing.T, allocator *inoAllocatorV1) {
	for i := uint64(0) ; i < 100; i++ {
		allocator.SetId(i * 100 + i)
	}
	if allocator.GetUsed() != 100 {
		t.Fatalf("allocate 100, but record:%d, cap:%d", allocator.GetUsed(), allocator.Cnt)
	}

	for i := uint64(0); i < 100; i++ {
		if allocator.Bits.IsBitFree(int(i * 100 + i - allocator.Start)) {
			t.Fatalf("id allocator:%d but now free, cap:%d", i * 100 + i, allocator.Cnt)
		}
	}

	for i := uint64(0) ; i < 100; i++ {
		allocator.ClearId(i * 100 + i)
	}
	if allocator.GetUsed() != 0 {
		t.Fatalf("allocate 0, but record:%d, cap:%d", allocator.GetUsed(), allocator.Cnt)
	}
	for i := uint64(0); i < 100; i++ {
		if !allocator.Bits.IsBitFree(int(i * 100 + i - allocator.Start)) {
			t.Fatalf("id allocator:%d but now free, cap:%d", i * 100 + i, allocator.Cnt)
		}
	}
}

func TestInoAllocatorV1_UsedCnt(t *testing.T) {
	allocator  := NewInoAllocatorV1(0, 1<<24 + 1)
	//set all 1
	allocator1 := NewInoAllocatorV1(0, 1<<24)
	allocator.SetStatus(allocatorStatusInit)
	allocator1.SetStatus(allocatorStatusInit)
	InoAlloterv1UsedCnt(t, allocator)
	InoAlloterv1UsedCnt(t, allocator1)
}

func InoAlloterv1Allocate(t *testing.T, allocator *inoAllocatorV1, start uint64) {
	allocator.SetId(start)
	for i := uint64(0) ; i < 100; i++ {
		allocator.AllocateId()
	}
	t.Logf(allocator.Bits.GetU64BitInfo(int(start / 64)))
	t.Logf(allocator.Bits.GetU64BitInfo(int(start / 64 + 1)))
	for i := uint64(0); i < 100; i++ {
		if allocator.Bits.IsBitFree(int(i + 1 + start)) {
			t.Fatalf("id allocator:%d but now free, cap:%d", i, allocator.Cnt)
		}
	}

	for i := uint64(0) ; i < 100; i++ {
		allocator.ClearId(i + 1 + start)
	}

	t.Logf(allocator.Bits.GetU64BitInfo(int(start / 64)))
	t.Logf(allocator.Bits.GetU64BitInfo(int(start / 64 + 1)))
	for i := uint64(0); i < 100; i++ {
		if !allocator.Bits.IsBitFree(int(i + 1 + start)) {
			t.Fatalf("id allocator:%d but now free, cap:%d", i, allocator.Cnt)
		}
	}
	return
}

func TestInoAllocatorV1_Allocate(t *testing.T) {
	allocator  := NewInoAllocatorV1(0, 1<<24 + 1)
	//set all
	allocator1 := NewInoAllocatorV1(0, 1<<24)
	allocator.SetStatus(allocatorStatusInit)
	allocator.SetStatus(allocatorStatusAvailable)
	allocator1.SetStatus(allocatorStatusInit)
	allocator1.SetStatus(allocatorStatusAvailable)
	InoAlloterv1Allocate(t, allocator, uint64(rand.Int()) % allocator.Cnt)
	InoAlloterv1Allocate(t, allocator1, uint64(rand.Int()) % allocator1.Cnt)
}

func TestInoAllocatorV1_StTest(t *testing.T) {
	var err error
	allocator := NewInoAllocatorV1(0, 1<<24)
	//stopped
	err = allocator.SetStatus(allocatorStatusAvailable)
	if err == nil {
		t.Fatalf("expect err, but now nil")
	}
	t.Logf("stat stopped-->started :%v", err)

	err = allocator.SetStatus(allocatorStatusUnavailable)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat stopped-->stopped")

	err = allocator.SetStatus(allocatorStatusInit)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat stopped-->init")

	//init
	err = allocator.SetStatus(allocatorStatusUnavailable)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat init-->stopped")
	allocator.SetStatus(allocatorStatusInit)

	err = allocator.SetStatus(allocatorStatusAvailable)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat init-->start")

	//start
	err = allocator.SetStatus(allocatorStatusAvailable)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat start-->start")

	err = allocator.SetStatus(allocatorStatusInit)
	if err == nil {
		t.Fatalf("expect err, but now nil")
	}
	t.Logf("stat started-->init :%v", err)

	err = allocator.SetStatus(allocatorStatusUnavailable)
	if err != nil {
		t.Fatalf("expect nil, but err:%v", err.Error())
	}
	t.Logf("stat start-->stopped")
}

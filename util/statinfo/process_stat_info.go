package statinfo

import (
	"github.com/cubefs/cubefs/util/cpu"
	"github.com/cubefs/cubefs/util/exporter"
	"github.com/cubefs/cubefs/util/log"
	"github.com/cubefs/cubefs/util/memory"
	"github.com/cubefs/cubefs/util/unit"
	"os"
	"sync"
	"time"
)

const (
	CollectPointNumber = 60
)

type ProcessStatInfo struct {
	ProcessStartTime string
	MaxCPUUsage      float64
	MaxMemUsage      float64
	MaxMemUsedGB     float64
	CPUUsageList     []float64
	MemUsedGBList    []float64
	memUsed          uint64
	RWMutex          sync.RWMutex
}

func NewProcessStatInfo() *ProcessStatInfo {
	return &ProcessStatInfo{
		CPUUsageList:  make([]float64, 0, CollectPointNumber),
		MemUsedGBList: make([]float64, 0, CollectPointNumber),
	}
}

func (s *ProcessStatInfo) UpdateStatInfoSchedule() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	s.DoUpdateStatInfo()
	for {
		select {
		case <-ticker.C:
			s.DoUpdateStatInfo()
		}
	}
}

func (s *ProcessStatInfo) DoUpdateStatInfo() {
	defer func() {
		if r := recover(); r != nil {
			log.LogErrorf("update sys info, recover: %v", r)
			exporter.WarningAppendKey("RecoverPanic", "update sys info panic")
			return
		}
	}()

	pid := os.Getpid()
	s.UpdateMemoryInfo(pid)
	s.UpdateCPUUsageInfo(pid)
}

func (s *ProcessStatInfo) UpdateMemoryInfo(pid int) {
	var (
		memoryUsedGB float64
		memoryUsage  float64
	)
	memoryUsed, err := memory.GetProcessMemory(pid)
	if err != nil {
		return
	}
	memoryTotal, _, err := memory.GetMemInfo()
	if err != nil {
		return
	}
	//reserves a decimal fraction
	memoryUsage = unit.FixedPoint(float64(memoryUsed)/float64(memoryTotal)*100, 1)
	memoryUsedGB = unit.FixedPoint(float64(memoryUsed)/unit.GB, 1)
	s.RWMutex.Lock()
	defer s.RWMutex.Unlock()
	s.memUsed = memoryUsed
	if len(s.MemUsedGBList) < CollectPointNumber {
		s.MemUsedGBList = append(s.MemUsedGBList, memoryUsedGB)
	} else {
		s.MemUsedGBList = append(s.MemUsedGBList[1:], memoryUsedGB)
	}
	if memoryUsedGB > s.MaxMemUsedGB {
		s.MaxMemUsedGB = memoryUsedGB
	}
	if memoryUsage > s.MaxMemUsage {
		s.MaxMemUsage = memoryUsage
	}
	return
}

func (s *ProcessStatInfo) UpdateCPUUsageInfo(pid int) {
	cpuUsage, err := cpu.GetProcessCPUUsage(pid)
	if err != nil {
		return
	}
	//reserves a decimal fraction
	cpuUsage = unit.FixedPoint(cpuUsage, 1)
	s.RWMutex.Lock()
	defer s.RWMutex.Unlock()
	if len(s.CPUUsageList) < CollectPointNumber {
		s.CPUUsageList = append(s.CPUUsageList, cpuUsage)
	} else {
		s.CPUUsageList = append(s.CPUUsageList[1:], cpuUsage)
	}
	if cpuUsage > s.MaxCPUUsage {
		s.MaxCPUUsage = cpuUsage
	}
	return
}

func (s *ProcessStatInfo) GetProcessCPUStatInfo() (cpuUsageList []float64, maxCPUUsage float64) {
	s.RWMutex.RLock()
	defer s.RWMutex.RUnlock()
	cpuUsageList = s.CPUUsageList
	maxCPUUsage = s.MaxCPUUsage
	return
}

func (s *ProcessStatInfo) GetProcessMemoryStatInfo() (memUsed uint64, memoryUsedGBList []float64, maxMemoryUsedGB float64, maxMemoryUsagePercent float64) {
	s.RWMutex.RLock()
	defer s.RWMutex.RUnlock()
	memoryUsedGBList = s.MemUsedGBList
	maxMemoryUsedGB = s.MaxMemUsedGB
	maxMemoryUsagePercent = s.MaxMemUsage
	memUsed = s.memUsed
	return
}

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

package master

import (
	"fmt"
	cfsProto "github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/util/errors"
	"github.com/cubefs/cubefs/util/log"
	"github.com/tiglabs/raft/proto"
	"strings"
)

// LeaderInfo represents the leader's information
type LeaderInfo struct {
	addr string //host:port
}

func (m *Server) scheduleProcessLeaderChange() {
	go func() {
		for {
			select {
			case leader := <-m.leaderChangeChan:
				log.LogWarnf("action[handleLeaderChange] change leader to [%v],%v ", leader, m.leaderVersion.Load())
				m.doLeaderChange(leader)
			}
		}
	}()
}

func (m *Server) doLeaderChange(leader uint64) {
	if leader == 0 {
		log.LogWarnf("action[handleLeaderChange] but no leader")
		return
	}
	m.metaReady.Store(false)
	m.cluster.isLeader.Store(false)
	m.leaderVersion.Add(1)
	oldLeaderAddr := m.leaderInfo.addr
	m.leaderInfo.addr = AddrDatabase[leader]
	log.LogWarnf("action[handleLeaderChange] change leader to [%v] ", m.leaderInfo.addr)
	m.reverseProxy = m.newReverseProxy()

	if m.id == leader {
		msg := fmt.Sprintf("clusterID[%v] leader is changed to %v",
			m.clusterName, m.leaderInfo.addr)
		WarnBySpecialKey(gAlarmKeyMap[alarmKeyLeaderChanged], msg)
		if oldLeaderAddr != m.leaderInfo.addr {
			m.loadMetadata()
			log.LogInfo("action[refreshUser] begin")
			if err := m.refreshUser(); err != nil {
				log.LogErrorf("action[refreshUser] failed,err:%v", err)
			}
			log.LogInfo("action[refreshUser] end")
			m.cluster.updateMetaLoadedTime()
			msg = fmt.Sprintf("clusterID[%v] leader[%v] load metadata finished.",
				m.clusterName, m.leaderInfo.addr)
			WarnBySpecialKey(gAlarmKeyMap[alarmKeyLeaderChanged], msg)
		}
		m.cluster.checkDataNodeHeartbeat()
		m.cluster.checkMetaNodeHeartbeat()
		m.cluster.isLeader.Store(true)
		m.metaReady.Store(true)
	} else {
		msg := fmt.Sprintf("clusterID[%v] leader is changed to %v",
			m.clusterName, m.leaderInfo.addr)
		WarnBySpecialKey(gAlarmKeyMap[alarmKeyLeaderChanged], msg)
		m.clearMetadata()
		m.cluster.resetMetaLoadedTime()
		msg = fmt.Sprintf("clusterID[%v] follower[%v] clear metadata has finished.",
			m.clusterName, m.ip)
		WarnBySpecialKey(gAlarmKeyMap[alarmKeyLeaderChanged], msg)
	}
}

func (m *Server) handleLeaderChange(leader uint64) {
	m.leaderChangeChan <- leader
}

func (m *Server) handlePeerChange(confChange *proto.ConfChange) (err error) {
	var msg string
	addr := string(confChange.Context)
	switch confChange.Type {
	case proto.ConfAddNode:
		var arr []string
		if arr = strings.Split(addr, colonSplit); len(arr) < 2 {
			msg = fmt.Sprintf("action[handlePeerChange] clusterID[%v] nodeAddr[%v] is invalid", m.clusterName, addr)
			break
		}
		m.raftStore.AddNodeWithPort(confChange.Peer.ID, arr[0], int(m.config.heartbeatPort), int(m.config.replicaPort))
		AddrDatabase[confChange.Peer.ID] = string(confChange.Context)
		msg = fmt.Sprintf("clusterID[%v] peerID:%v,nodeAddr[%v] has been add", m.clusterName, confChange.Peer.ID, addr)
	case proto.ConfRemoveNode:
		m.raftStore.DeleteNode(confChange.Peer.ID)
		msg = fmt.Sprintf("clusterID[%v] peerID:%v,nodeAddr[%v] has been removed", m.clusterName, confChange.Peer.ID, addr)
	}
	WarnBySpecialKey(gAlarmKeyMap[alarmKeyPeerChanged], msg)
	return
}

func (m *Server) handleApplySnapshot() {
	if err := m.checkClusterName(); err != nil {
		log.LogErrorf(errors.Stack(err))
		log.LogFlush()
		panic("cfg cluster name failed")
		return
	}
	m.fsm.restore()
	m.restoreIDAlloc()
	return
}

func (m *Server) restoreIDAlloc() {
	m.cluster.idAlloc.restore()
}

// Load stored metadata into the memory
func (m *Server) loadMetadata() {
	log.LogInfo("action[loadMetadata] begin")
	m.clearMetadata()
	m.restoreIDAlloc()
	m.cluster.fsm.restore()
	var err error
	if err = m.cluster.loadClusterValue(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadNodeSets(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadRegions(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadIDCs(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadDataNodes(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadMetaNodes(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadEcNodes(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadCodecNodes(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadVols(); err != nil {
		panic(err)
	}

	if err = m.cluster.loadTokens(); err != nil {
		panic(err)
	}

	//rand.Seed(time.Now().UnixNano())
	//v := 15 + rand.Intn(10)
	//time.Sleep(time.Second * time.Duration(v))
	if err = m.cluster.loadMetaPartitions(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadDataPartitions(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadEcPartitions(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadMigrateTask(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadFlashGroups(); err != nil {
		panic(err)
	}
	if err = m.cluster.loadFlashNodes(); err != nil {
		panic(err)
	}
	log.LogInfo("action[loadMetadata] end")

	log.LogInfo("action[loadUserInfo] begin")
	if err = m.user.loadUserStore(); err != nil {
		panic(err)
	}
	if err = m.user.loadAKStore(); err != nil {
		panic(err)
	}
	if err = m.user.loadVolUsers(); err != nil {
		panic(err)
	}
	log.LogInfo("action[loadUserInfo] end")
}

func (m *Server) clearMetadata() {
	m.cluster.clearTopology()
	m.cluster.clearDataNodes()
	m.cluster.clearMetaNodes()
	m.cluster.clearCodecNodes()
	m.cluster.clearEcNodes()
	m.cluster.clearMigrateTask()
	m.cluster.clearVols()
	m.cluster.clearClusterViewResponseCache()
	m.user.clearUserStore()
	m.user.clearAKStore()
	m.user.clearVolUsers()
	m.cluster.flashNodeTopo.clear()
	m.cluster.clearFlashGroupResponseCache()

	m.cluster.t = newTopology()
	m.cluster.flashNodeTopo = newFlashNodeTopology()
}

func (m *Server) refreshUser() (err error) {
	/* todo create user automatically
	var userInfo *cfsProto.UserInfo
	for volName, vol := range m.cluster.allVols() {
		if _, err = m.user.getUserInfo(vol.Owner); err == cfsProto.ErrUserNotExists {
			if len(vol.OSSAccessKey) > 0 && len(vol.OSSSecretKey) > 0 {
				var param = cfsProto.UserCreateParam{
					ID:        vol.Owner,
					Password:  DefaultUserPassword,
					AccessKey: vol.OSSAccessKey,
					SecretKey: vol.OSSSecretKey,
					Type:      cfsProto.UserTypeNormal,
				}
				userInfo, err = m.user.createKey(&param)
				if err != nil && err != cfsProto.ErrDuplicateUserID && err != cfsProto.ErrDuplicateAccessKey {
					return err
				}
			} else {
				var param = cfsProto.UserCreateParam{
					ID:       vol.Owner,
					Password: DefaultUserPassword,
					Type:     cfsProto.UserTypeNormal,
				}
				userInfo, err = m.user.createKey(&param)
				if err != nil && err != cfsProto.ErrDuplicateUserID {
					return err
				}
			}
			if err == nil && userInfo != nil {
				if _, err = m.user.addOwnVol(userInfo.UserID, volName); err != nil {
					return err
				}
			}
		}
	}*/
	if _, err = m.user.getUserInfo(RootUserID); err != nil {
		var param = cfsProto.UserCreateParam{
			ID:       RootUserID,
			Password: DefaultRootPasswd,
			Type:     cfsProto.UserTypeRoot,
		}
		if _, err = m.user.createKey(&param); err != nil {
			return err
		}
	}
	return nil
}

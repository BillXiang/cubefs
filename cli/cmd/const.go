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

package cmd

const (
	//List of operation name for cli
	CliOpGet               = "get"
	CliOpList              = "list"
	CliOpStatus            = "stat"
	CliOpCreate            = "create"
	CliOpDelete            = "delete"
	CliOpInfo              = "info"
	CliOpAdd               = "add"
	CliOpSet               = "set"
	CliOpDecommission      = "decommission"
	CliOpDecommissionDisk  = "decommissionDisk"
	CliOpDownloadZip       = "load"
	CliOpMetaCompatibility = "meta"
	CliOpFreeze            = "freeze"
	CliOpSetThreshold      = "threshold"
	CliOpCheck             = "check"
	CliOpReset             = "reset"
	CliOpReplicate         = "add-replica"
	CliOpDelReplica        = "del-replica"
	CliOpAddLearner        = "add-learner"
	CliOpPromoteLearner    = "promote-learner"

	//Shorthand format of operation name
	CliOpDecommissionShortHand = "dec"

	//resource name
	CliResourceDataNode      = "datanode [COMMAND]"
	CliResourceMetaNode      = "metanode"
	CliResourceDataPartition = "datapartition"
	CliResourceMetaPartition = "metapartition"
	CliResourceTopology      = "topology"
	CliResourceRaftNode      = "raftnode"
	CliResourceDisk          = "disk"
	CliResourceConfig        = "config"

	//Flags
	CliFlagName                = "name"
	CliFlagOnwer               = "user"
	CliFlagDataPartitionSize   = "dp-size"
	CliFlagDataPartitionCount  = "dp-count"
	CliFlagMetaPartitionCount  = "mp-count"
	CliFlagReplicas            = "replicas"
	CliFlagEnable              = "enable"
	CliFlagEnableFollowerRead  = "follower-read"
	CliFlagAutoRepair          = "auto-repair"
	CliFlagVolWriteMutexEnable = "vol-write-mutex"
	CliFlagAuthenticate        = "authenticate"
	CliFlagEnableToken         = "enable-token"
	CliFlagEnableAutoFill      = "auto-fill"
	CliFlagCapacity            = "capacity"
	CliFlagThreshold           = "threshold"
	CliFlagAutoPromote         = "auto-promote"
	CliFlagAddress             = "addr"
	CliFlagDiskPath            = "path"
	CliFlagAuthKey             = "authkey"
	CliFlagINodeStartID        = "inode-start"
	CliFlagId                  = "id"
	CliFlagZoneName            = "zonename"

	//CliFlagSetDataPartitionCount	= "count" use dp-count instead

	//Shorthand format of resource name
	ResourceDataNodeShortHand      = "dn"
	ResourceMetaNodeShortHand      = "mn"
	ResourceDataPartitionShortHand = "dp"
	ResourceMetaPartitionShortHand = "mp"
)

const (
	UsedSizeNotEqualErr         = "used size not equal"
	RaftNoLeader                = "no raft leader"
	ReplicaNotConsistent        = "replica number not consistent"
	PartitionNotHealthyInMaster = "partition not healthy in master"
)

// for parse raft log
const (
	opFSMCreateInode uint32 = iota
	opFSMUnlinkInode
	opFSMCreateDentry
	opFSMDeleteDentry
	opFSMDeletePartition
	opFSMUpdatePartition
	opFSMDecommissionPartition
	opFSMExtentsAdd
	opFSMStoreTick
	startStoreTick
	stopStoreTick
	opFSMUpdateDentry
	opFSMExtentTruncate
	opFSMCreateLinkInode
	opFSMEvictInode
	opFSMInternalDeleteInode
	opFSMSetAttr
	opFSMInternalDelExtentFile
	opFSMInternalDelExtentCursor
	opExtentFileSnapshot
	opFSMSetXAttr
	opFSMRemoveXAttr
	opFSMCreateMultipart
	opFSMRemoveMultipart
	opFSMAppendMultipart
	opFSMSyncCursor

	//supplement action
	opFSMInternalDeleteInodeBatch
	opFSMDeleteDentryBatch
	opFSMUnlinkInodeBatch
	opFSMEvictInodeBatch

	opFSMCursorReset

	opFSMExtentsInsert
)

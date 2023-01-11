package proto

const (
	AdminGetClusterUmpKey                       = "admin_getCluster"
	AdminGetDataPartitionUmpKey                 = "dataPartition_get"
	AdminLoadDataPartitionUmpKey                = "dataPartition_load"
	AdminCreateDataPartitionUmpKey              = "dataPartition_create"
	AdminFreezeDataPartitionUmpKey              = "dataPartition_freeze"
	AdminUnfreezeDataPartitionUmpKey            = "dataPartition_unfreeze"
	AdminDecommissionDataPartitionUmpKey        = "dataPartition_decommission"
	AdminDiagnoseDataPartitionUmpKey            = "dataPartition_diagnose"
	AdminResetDataPartitionUmpKey               = "dataPartition_reset"
	AdminTransferDataPartitionUmpKey            = "dataPartition_transfer"
	AdminManualResetDataPartitionUmpKey         = "dataPartition_manualReset"
	AdminDataPartitionUpdateUmpKey              = "dataPartition_update"
	AdminDataPartitionSetIsRecoverUmpKey        = "dataPartition_setIsRecover"
	AdminCanDelDataPartitionsUmpKey             = "dataPartition_candel"
	AdminCanMigrateDataPartitionsUmpKey         = "dataPartition_canmigrate"
	AdminDelDpAlreadyEcUmpKey                   = "dataPartition_deldpalreadyec"
	AdminDpMigrateEcUmpKey                      = "dataPartition_ecmigreate"
	AdminDpStopMigratingUmpKey                  = "dataPartition_stopMigrating"
	AdminDNStopMigratingUmpKey                  = "dataNode_stopMigrating"
	AdminResetCorruptDataNodeUmpKey             = "dataNode_reset"
	AdminDeleteDataReplicaUmpKey                = "dataReplica_delete"
	AdminAddDataReplicaUmpKey                   = "dataReplica_add"
	AdminAddDataReplicaLearnerUmpKey            = "dataLearner_add"
	AdminPromoteDataReplicaLearnerUmpKey        = "dataLearner_promote"
	AdminDeleteVolUmpKey                        = "vol_delete"
	AdminUpdateVolUmpKey                        = "vol_update"
	AdminUpdateVolEcInfoUmpKey                  = "vol_updateEcInfo"
	AdminSetVolConvertStUmpKey                  = "vol_setConvertSate"
	AdminVolBatchUpdateDpsUmpKey                = "vol_batchUpdateDataPartitions"
	AdminCreateVolUmpKey                        = "admin_createVol"
	AdminGetVolUmpKey                           = "admin_getVol"
	AdminClusterFreezeUmpKey                    = "cluster_freeze"
	AdminClusterStatUmpKey                      = "cluster_stat"
	AdminGetIPUmpKey                            = "admin_getIp"
	AdminGetLimitInfoUmpKey                     = "admin_getLimitInfo"
	AdminCreateMetaPartitionUmpKey              = "metaPartition_create"
	AdminSetMetaNodeThresholdUmpKey             = "threshold_set"
	AdminClusterEcSetUmpKey                     = "cluster_ecSet"
	AdminClusterGetScrubUmpKey                  = "scrub_get"
	AdminListVolsUmpKey                         = "vol_list"
	AdminSetNodeInfoUmpKey                      = "admin_setNodeInfo"
	AdminGetNodeInfoUmpKey                      = "admin_getNodeInfo"
	AdminSetNodeStateUmpKey                     = "admin_setNodeState"
	AdminMergeNodeSetUmpKey                     = "admin_mergeNodeSet"
	AdminClusterAutoMergeNodeSetUmpKey          = "cluster_autoMergeNodeSet"
	AdminApplyVolMutexUmpKey                    = "vol_writeMutex_apply"
	AdminReleaseVolMutexUmpKey                  = "vol_writeMutex_release"
	AdminGetVolMutexUmpKey                      = "vol_writeMutex_get"
	AdminSetVolConvertModeUmpKey                = "vol_setConvertMode"
	AdminSetVolMinRWPartitionUmpKey             = "vol_setMinRWPartition"
	AdminSetClientPkgAddrUmpKey                 = "clientPkgAddr_set"
	AdminGetClientPkgAddrUmpKey                 = "clientPkgAddr_get"
	AdminSmartVolListUmpKey                     = "admin_smartVol_list"
	AdminSetMNRocksDBDiskThresholdUmpKey        = "rocksdbDiskThreshold_set"
	AdminSetMNMemModeRocksDBDiskThresholdUmpKey = "memModeRocksdbDiskThreshold_set"
	AdminCompactVolListUmpKey                   = "admin_compactVol_list"
	AdminCompactVolSetUmpKey                    = "admin_compactVol_set"
	ClientDataPartitionsUmpKey                  = "client_partitions"
	ClientVolUmpKey                             = "client_vol"
	ClientMetaPartitionUmpKey                   = "metaPartition_get"
	ClientVolStatUmpKey                         = "client_volStat"
	ClientMetaPartitionsUmpKey                  = "client_metaPartitions"
	AddRaftNodeUmpKey                           = "raftNode_add"
	RemoveRaftNodeUmpKey                        = "raftNode_remove"
	AddDataNodeUmpKey                           = "dataNode_add"
	DecommissionDataNodeUmpKey                  = "dataNode_decommission"
	DecommissionDiskUmpKey                      = "disk_decommission"
	GetDataNodeUmpKey                           = "dataNode_get"
	AddMetaNodeUmpKey                           = "metaNode_add"
	DecommissionMetaNodeUmpKey                  = "metaNode_decommission"
	GetMetaNodeUmpKey                           = "metaNode_get"
	AdminUpdateMetaNodeUmpKey                   = "metaNode_update"
	AdminLoadMetaPartitionUmpKey                = "metaPartition_load"
	AdminDiagnoseMetaPartitionUmpKey            = "metaPartition_diagnose"
	AdminResetMetaPartitionUmpKey               = "metaPartition_reset"
	AdminManualResetMetaPartitionUmpKey         = "metaPartition_manualReset"
	AdminResetCorruptMetaNodeUmpKey             = "metaNode_reset"
	AdminDecommissionMetaPartitionUmpKey        = "metaPartition_decommission"
	AdminAddMetaReplicaUmpKey                   = "metaReplica_add"
	AdminDeleteMetaReplicaUmpKey                = "metaReplica_delete"
	AdminSelectMetaReplicaNodeUmpKey            = "metaReplica_selectNode"
	AdminAddMetaReplicaLearnerUmpKey            = "metaLearner_add"
	AdminPromoteMetaReplicaLearnerUmpKey        = "metaLearner_promote"
	AdminMetaPartitionSetIsRecoverUmpKey        = "metaPartition_setIsRecover"
	GetMetaNodeTaskResponseUmpKey               = "metaNode_response"
	GetDataNodeTaskResponseUmpKey               = "dataNode_response"
	DataNodeValidateCRCReportUmpKey             = "dataNode_validateCRCReport"
	GetCodecNodeTaskResponseUmpKey              = "codecNode_response"
	GetEcNodeTaskResponseUmpKey                 = "ecNode_response"
	GetTopologyViewUmpKey                       = "topo_get"
	UpdateZoneUmpKey                            = "zone_update"
	GetAllZonesUmpKey                           = "zone_list"
	SetZoneRegionUmpKey                         = "zone_setRegion"
	UpdateRegionUmpKey                          = "region_update"
	GetRegionViewUmpKey                         = "region_get"
	RegionListUmpKey                            = "region_list"
	CreateRegionUmpKey                          = "region_create"
	SetZoneIDCUmpKey                            = "zone_setIDC"
	GetIDCViewUmpKey                            = "idc_get"
	IDCListUmpKey                               = "idc_list"
	CreateIDCUmpKey                             = "idc_create"
	DeleteDCUmpKey                              = "idc_delete"
	TokenGetURIUmpKey                           = "token_get"
	TokenAddURIUmpKey                           = "token_add"
	TokenDelURIUmpKey                           = "token_delete"
	TokenUpdateURIUmpKey                        = "token_update"
	UserCreateUmpKey                            = "user_create"
	UserDeleteUmpKey                            = "user_delete"
	UserUpdateUmpKey                            = "user_update"
	UserUpdatePolicyUmpKey                      = "user_updatePolicy"
	UserRemovePolicyUmpKey                      = "user_removePolicy"
	UserDeleteVolPolicyUmpKey                   = "user_deleteVolPolicy"
	UserGetInfoUmpKey                           = "user_info"
	UserGetAKInfoUmpKey                         = "user_akInfo"
	UserTransferVolUmpKey                       = "user_transferVol"
	UserListUmpKey                              = "user_list"
	UsersOfVolUmpKey                            = "vol_users"
	GetAllCodecNodesUmpKey                      = "codecNode_getAllNodes"
	GetCodecNodeUmpKey                          = "codecNode_get"
	AddCodecNodeUmpKey                          = "codecNode_add"
	DecommissionCodecNodeUmpKey                 = "codecNode_decommission"
	AddEcNodeUmpKey                             = "ecNode_add"
	GetEcNodeUmpKey                             = "ecNode_get"
	DecommissionEcNodeUmpKey                    = "ecNode_decommission"
	DecommissionEcDiskUmpKey                    = "ecNode_diskDecommission"
	AdminGetEcPartitionUmpKey                   = "ecPartition_get"
	AdminDecommissionEcPartitionUmpKey          = "ecPartition_decommission"
	AdminDiagnoseEcPartitionUmpKey              = "ecPartition_diagnose"
	AdminEcPartitionRollBackUmpKey              = "ecPartition_rollback"
	AdminGetAllTaskStatusUmpKey                 = "ecPartition_gettaskstatus"
	AdminDeleteEcReplicaUmpKey                  = "ecReplica_delete"
	AdminAddEcReplicaUmpKey                     = "ecReplica_add"
	ClientEcPartitionsUmpKey                    = "client_ecPartitions"
	AdminMetaPartitionSetEnableReuseStateUmpKey = "metaPartition_setEnableReuseState"
)

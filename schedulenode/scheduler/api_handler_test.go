package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/sdk/mysql"
	"github.com/chubaofs/chubaofs/util/config"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
)

var ListenAddr string

func init() {
	cfgJSON := `{
		"role": "schedulenode",
		"localIP": "11.97.57.231",
		"prof": "17330",
		"workerTaskPeriod": 5,
		"logDir": "/export/Logs/chubaofs/scheduleNode1/",
		"logLevel": "debug",
		"mysql": {
			"url": "11.97.57.230",
			"userName": "root",
			"password": "cfsTest123@",
			"database": "smart",
			"port": 3306
		},
		"hBaseUrl": "api.storage.hbase.jd.local/",
		"clusterAddr": {
			"smart_vol_test": [
				"172.20.81.30:17010",
				"172.20.81.31:17010",
				"11.7.139.135:17010"
			]
		}
    }`
	cfg := config.LoadConfigString(cfgJSON)
	scheduleNode := NewScheduleNode()
	profPort := cfg.GetString(config.ConfigKeyProfPort)
	if profPort != "" {
		go func() {
			err := http.ListenAndServe(fmt.Sprintf(":%v", profPort), nil)
			if err != nil {
				panic(fmt.Sprintf("cannot listen pprof %v err %v", profPort, err.Error()))
			}
		}()
	}
	err := scheduleNode.Start(cfg)
	if err != nil {
		fmt.Printf("start schedule node handler failed cause: %s\n", err.Error())
	}
	ListenAddr = fmt.Sprintf("http://127.0.0.1:%s", profPort)
	time.Sleep(5 * time.Second)
}

func TestGetScheduleNodeLeader(t *testing.T) {
	reqURL := fmt.Sprintf("%v%v", ListenAddr, ScheduleNodeAPIGetLeader)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("Got leader failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}
	le := &proto.LeaderElect{}
	err = json.Unmarshal(reply.Data, le)
	if err != nil {
		t.Errorf("unmarshall response data to leader elect failed, errorInfo: %s", err.Error())
	}
	fmt.Printf("leader: %s, term: %v\n", le.LeaderAddr, le.Term)
}

func TestListTasks(t *testing.T) {
	// init tasks
	tasks, err := addTasks()
	if err != nil {
		t.Errorf("add new tasks failed. cause: %v", err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%s&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIListTasks,
		ParamKeyCluster, tasks[0].Cluster,
		ParamKeyVolume, tasks[0].VolName,
		ParamKeyDPId, tasks[0].DpId,
		ParamKeyWorkerType, proto.WorkerTypeSmartVolume)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("List tasks failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}
	tasksQueried := make([]*proto.Task, 0)
	err = json.Unmarshal(reply.Data, &tasksQueried)
	if err != nil {
		t.Errorf("unmarshall response data to tasks failed, errorInfo: %s", err.Error())
	}
	for _, task := range tasksQueried {
		fmt.Printf("taskId(%v), cluster(%v), volume(%v), dpId(%v), taskType(%v)\n", task.TaskId, task.Cluster, task.VolName, task.DpId, task.TaskType)
	}
	err = deleteTasks(tasks)
	if err != nil {
		t.Errorf("delete tasks failed. cause: %v", err.Error())
	}
}

func TestListTaskHistory(t *testing.T) {
	// init tasks
	tasks, err := addHistoryTasks()
	if err != nil {
		t.Errorf("add new tasks failed. cause: %v", err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%s&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIListHisTasks,
		ParamKeyCluster, tasks[0].Cluster,
		ParamKeyVolume, tasks[0].VolName,
		ParamKeyDPId, tasks[0].DpId,
		ParamKeyWorkerType, proto.WorkerTypeSmartVolume)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("Got leader failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}
	historyTasksQueried := make([]*proto.TaskHistory, 0)
	err = json.Unmarshal(reply.Data, &historyTasksQueried)
	if err != nil {
		t.Errorf("unmarshall response data to leader elect failed, errorInfo: %s", err.Error())
	}
	for _, ht := range historyTasksQueried {
		fmt.Printf("taskId(%v), cluster(%v), volume(%v), dpId(%v), taskType(%v)\n", ht.TaskId, ht.Cluster, ht.VolName, ht.DpId, ht.TaskType)
	}
	// delete history tasks
	err = mysql.DeleteTaskHistories(historyTasksQueried)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

func TestAddFlowControl(t *testing.T) {
	flow, _ := proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "spark", 1000)
	err := mysql.DeleteFlowControl(flow)
	if err != nil {
		t.Fatalf(err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIFlowAdd,
		ParamKeyTaskType, flow.WorkerType,
		ParamKeyFlowType, flow.FlowType,
		ParamKeyFlowValue, flow.FlowValue,
		ParamKeyMaxNum, flow.MaxNums)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("add flow control failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	var existed bool
	flows, err := mysql.SelectFlowControls()
	if err != nil {
		t.Fatalf(err.Error())
	}
	for _, f := range flows {
		if f.WorkerType == flow.WorkerType && f.FlowType == flow.FlowType && f.FlowValue == flow.FlowValue && f.MaxNums == flow.MaxNums {
			existed = true
			break
		}
	}
	if !existed {
		t.Fatalf("add failed")
	}

	// clean flow controls
	err = mysql.DeleteFlowControl(flow)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

func TestModifyFlowControl(t *testing.T) {
	// first add new flow control
	flow, _ := proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "spark", 1000)
	err := mysql.DeleteFlowControl(flow)
	if err != nil {
		t.Fatalf(err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIFlowAdd,
		ParamKeyTaskType, flow.WorkerType,
		ParamKeyFlowType, flow.FlowType,
		ParamKeyFlowValue, flow.FlowValue,
		ParamKeyMaxNum, flow.MaxNums)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("add flow control failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	// change flow control and modify to database
	flow, _ = proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "spark", 199)
	reqURL = fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIFlowModify,
		ParamKeyTaskType, flow.WorkerType,
		ParamKeyFlowType, flow.FlowType,
		ParamKeyFlowValue, flow.FlowValue,
		ParamKeyMaxNum, flow.MaxNums)
	fmt.Println(reqURL)
	reply, err = process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("modify flow control failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	var existed bool
	flows, err := mysql.SelectFlowControls()
	if err != nil {
		t.Fatalf(err.Error())
	}
	for _, f := range flows {
		if f.WorkerType == flow.WorkerType && f.FlowType == flow.FlowType && f.FlowValue == flow.FlowValue && f.MaxNums == flow.MaxNums {
			existed = true
			break
		}
	}
	if !existed {
		t.Fatalf("modify failed")
	}

	// clean flow control
	err = mysql.DeleteFlowControl(flow)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

func TestDeleteFlowControl(t *testing.T) {
	// first add new flow control
	flow, _ := proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "spark", 1000)
	err := mysql.DeleteFlowControl(flow)
	if err != nil {
		t.Fatalf(err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v&%s=%v", ListenAddr, ScheduleNodeAPIFlowAdd,
		ParamKeyTaskType, flow.WorkerType,
		ParamKeyFlowType, flow.FlowType,
		ParamKeyFlowValue, flow.FlowValue,
		ParamKeyMaxNum, flow.MaxNums)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("add flow control failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	reqURL = fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%s", ListenAddr, ScheduleNodeAPIFlowDelete,
		ParamKeyTaskType, flow.WorkerType,
		ParamKeyFlowType, flow.FlowType,
		ParamKeyFlowValue, flow.FlowValue)
	fmt.Println(reqURL)
	reply, err = process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("delete flow control failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	var existed bool
	flows, err := mysql.SelectFlowControls()
	if err != nil {
		t.Fatalf(err.Error())
	}
	for _, f := range flows {
		if f.WorkerType == flow.WorkerType && f.FlowType == flow.FlowType && f.FlowValue == flow.FlowValue && f.MaxNums == flow.MaxNums {
			existed = true
			break
		}
	}
	if existed {
		t.Fatalf("delete failed")
	}
}

func TestListFlowControls(t *testing.T) {
	// add multiple flow controls
	var (
		err   error
		flow1 *proto.FlowControl
		flow2 *proto.FlowControl
		flow3 *proto.FlowControl
	)
	flow1, err = proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "spark", 2000)
	if err != nil {
		t.Fatalf(err.Error())
	}
	flow2, err = proto.NewFlowControl(proto.WorkerTypeSmartVolume, proto.FlowTypeCluster, "test", 2000)
	if err != nil {
		t.Fatalf(err.Error())
	}
	flow3, err = proto.NewFlowControl(proto.WorkerTypeCompact, proto.FlowTypeCluster, "spark", 99999999)
	if err != nil {
		t.Fatalf(err.Error())
	}

	// clean test data
	err = mysql.DeleteFlowControl(flow1)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.DeleteFlowControl(flow2)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.DeleteFlowControl(flow3)
	if err != nil {
		t.Fatalf(err.Error())
	}

	err = mysql.AddFlowControl(flow1)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.AddFlowControl(flow2)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.AddFlowControl(flow3)
	if err != nil {
		t.Fatalf(err.Error())
	}

	reqURL := fmt.Sprintf("%v%v", ListenAddr, ScheduleNodeAPIFlowList)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("list flow controls failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	flows, err := mysql.SelectFlowControls()
	if err != nil {
		t.Fatalf(err.Error())
	}
	if len(flows) < 3 {
		t.Fatalf("list result is not enough")
	}

	err = mysql.DeleteFlowControl(flow1)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.DeleteFlowControl(flow2)
	if err != nil {
		t.Fatalf(err.Error())
	}
	err = mysql.DeleteFlowControl(flow3)
	if err != nil {
		t.Fatalf(err.Error())
	}
}

func addTasks() (tasks []*proto.Task, err error) {
	var (
		cluster    = "sparkcfs.jd.local"
		volumeName = "vol_liyubo"
		dpId1      = 101
		dpId2      = 102
		dpId3      = 103
		taskInfo   = "testTaskInfo"
	)
	// add task
	task1 := &proto.Task{
		TaskType: proto.WorkerTypeSmartVolume,
		Cluster:  cluster,
		VolName:  volumeName,
		DpId:     uint64(dpId1),
		TaskInfo: taskInfo,
		Status:   proto.TaskStatusUnallocated,
	}
	task2 := &proto.Task{
		TaskType: proto.WorkerTypeSmartVolume,
		Cluster:  cluster,
		VolName:  volumeName,
		DpId:     uint64(dpId2),
		TaskInfo: taskInfo,
		Status:   proto.TaskStatusUnallocated,
	}
	task3 := &proto.Task{
		TaskType: proto.WorkerTypeSmartVolume,
		Cluster:  cluster,
		VolName:  volumeName,
		DpId:     uint64(dpId3),
		TaskInfo: taskInfo,
		Status:   proto.TaskStatusUnallocated,
	}
	var taskId1, taskId2, taskId3 uint64
	taskId1, err = mysql.AddTask(task1)
	if err != nil {
		return nil, err
	}
	fmt.Println(fmt.Sprintf("add task1 success, taskId1： %v", taskId1))
	taskId2, err = mysql.AddTask(task2)
	if err != nil {
		return nil, err
	}
	fmt.Println(fmt.Sprintf("add task2 success, taskId2： %v", taskId2))
	taskId3, err = mysql.AddTask(task3)
	if err != nil {
		return nil, err
	}
	fmt.Println(fmt.Sprintf("add task3 success, taskId3： %v", taskId3))
	task1.TaskId = taskId1
	task2.TaskId = taskId2
	task3.TaskId = taskId3
	tasks = append(tasks, task1)
	tasks = append(tasks, task2)
	tasks = append(tasks, task3)
	return tasks, nil
}

func addHistoryTasks() (tasks []*proto.Task, err error) {
	var (
		cluster    = "sparkcfs.jd.local"
		volumeName = "vol_liyubo"
		dpId1      = 101
		dpId2      = 102
		dpId3      = 103
		taskInfo   = "testTaskInfo"
	)
	// add task
	task1 := &proto.Task{
		TaskType:   proto.WorkerTypeSmartVolume,
		Cluster:    cluster,
		VolName:    volumeName,
		DpId:       uint64(dpId1),
		TaskInfo:   taskInfo,
		CreateTime: time.Now(),
		UpdateTime: time.Now(),
		Status:     proto.TaskStatusUnallocated,
	}
	task2 := &proto.Task{
		TaskType:   proto.WorkerTypeSmartVolume,
		Cluster:    cluster,
		VolName:    volumeName,
		DpId:       uint64(dpId2),
		TaskInfo:   taskInfo,
		CreateTime: time.Now(),
		UpdateTime: time.Now(),
		Status:     proto.TaskStatusUnallocated,
	}
	task3 := &proto.Task{
		TaskType:   proto.WorkerTypeSmartVolume,
		Cluster:    cluster,
		VolName:    volumeName,
		DpId:       uint64(dpId3),
		TaskInfo:   taskInfo,
		CreateTime: time.Now(),
		UpdateTime: time.Now(),
		Status:     proto.TaskStatusUnallocated,
	}
	var taskId1, taskId2, taskId3 uint64
	task1.TaskId = taskId1
	task2.TaskId = taskId2
	task3.TaskId = taskId3
	tasks = append(tasks, task1)
	tasks = append(tasks, task2)
	tasks = append(tasks, task3)
	err = mysql.AddTasksToHistory(tasks)
	return tasks, err
}

func deleteTasks(tasks []*proto.Task) error {
	return mysql.DeleteTasks(tasks)
}

func TestAddScheduleConfig(t *testing.T) {
	configKey := "smartVolume"
	configValue := "0.3"
	sc := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey, configValue)
	err := mysql.DeleteScheduleConfig(sc)
	if err != nil && !strings.Contains(err.Error(), "affected rows less then one") {
		t.Fatalf(err.Error())
	}

	// add new via api
	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v", ListenAddr, ScheduleNodeAPIConfigAdd,
		ParamKeyConfigType, sc.ConfigType,
		ParamKeyConfigKey, sc.ConfigKey,
		ParamKeyConfigValue, sc.ConfigValue)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("add schedule config failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	// clean
	defer func() {
		if err = mysql.DeleteScheduleConfig(sc); err != nil {
			t.Fatalf(err.Error())
		}
	}()

	// select to check add success
	var existed bool
	scs, err := mysql.SelectScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold)
	if err != nil {
		t.Fatalf(err.Error())
	}
	for _, sc := range scs {
		if sc.ConfigType == proto.ScheduleConfigTypeMigrateThreshold && sc.ConfigKey == configKey && sc.ConfigValue == configValue {
			existed = true
			break
		}
	}
	if !existed {
		t.Fatalf("add failed")
	}
}

func TestAddScheduleConfigException(t *testing.T) {
	configKey := "smartVolume"
	configValue1 := "test"
	configValue2 := "3"
	sc := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey, configValue1)

	// add new via api
	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v", ListenAddr, ScheduleNodeAPIConfigAdd,
		ParamKeyConfigType, sc.ConfigType,
		ParamKeyConfigKey, sc.ConfigKey,
		ParamKeyConfigValue, sc.ConfigValue)
	fmt.Println(reqURL)
	_, err := process(reqURL)
	if err != nil && !strings.Contains(err.Error(), "parse migrate threshold failed") {
		t.Fatalf(err.Error())
	}

	sc.ConfigValue = configValue2
	// add new via api
	reqURL = fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v", ListenAddr, ScheduleNodeAPIConfigAdd,
		ParamKeyConfigType, sc.ConfigType,
		ParamKeyConfigKey, sc.ConfigKey,
		ParamKeyConfigValue, sc.ConfigValue)
	fmt.Println(reqURL)
	_, err = process(reqURL)
	if err != nil && !strings.Contains(err.Error(), "migrate threshold value is invalid") {
		t.Fatalf(err.Error())
	}
}

func TestModifyScheduleConfig(t *testing.T) {
	var (
		err error
		scs []*proto.ScheduleConfig
	)
	configKey := "smartVolume"
	configValue1 := "0.3"
	configValue2 := "0.6"
	sc := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey, configValue1)
	if err = mysql.AddScheduleConfig(sc); err != nil {
		t.Fatalf(err.Error())
	}

	// clean
	defer func() {
		if err = mysql.DeleteScheduleConfig(sc); err != nil {
			t.Fatalf(err.Error())
		}
	}()

	sc.ConfigValue = configValue2
	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v", ListenAddr, ScheduleNodeAPIConfigUpdate,
		ParamKeyConfigType, sc.ConfigType,
		ParamKeyConfigKey, sc.ConfigKey,
		ParamKeyConfigValue, sc.ConfigValue)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("modify schedule config failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	// select and check
	if scs, err = mysql.SelectScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold); err != nil {
		t.Fatalf(err.Error())
	}
	var existed bool
	for _, sc := range scs {
		if sc.ConfigType == proto.ScheduleConfigTypeMigrateThreshold && sc.ConfigKey == configKey && sc.ConfigValue == configValue2 {
			existed = true
		}
	}
	if !existed {
		t.Fatalf("modify failed")
	}
}

func TestSelectScheduleConfig(t *testing.T) {
	var (
		err error
		scs []*proto.ScheduleConfig
	)
	configKey1 := "smartVolume1"
	configKey2 := "smartVolume2"
	configValue1 := "0.3"
	configValue2 := "0.6"
	sc1 := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey1, configValue1)
	sc2 := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey2, configValue2)
	err = mysql.DeleteScheduleConfig(sc1)
	if err != nil && !strings.Contains(err.Error(), "affected rows less then one") {
		t.Fatalf(err.Error())
	}
	err = mysql.DeleteScheduleConfig(sc2)
	if err != nil && !strings.Contains(err.Error(), "affected rows less then one") {
		t.Fatalf(err.Error())
	}

	if err = mysql.AddScheduleConfig(sc1); err != nil {
		t.Fatalf(err.Error())
	}
	if err = mysql.AddScheduleConfig(sc2); err != nil {
		t.Fatalf(err.Error())
	}

	// clean
	defer func() {
		if err = mysql.DeleteScheduleConfig(sc1); err != nil {
			t.Fatalf(err.Error())
		}
		if err = mysql.DeleteScheduleConfig(sc2); err != nil {
			t.Fatalf(err.Error())
		}
	}()

	var existed1 bool
	var existed2 bool
	reqURL := fmt.Sprintf("%v%v?%s=%v", ListenAddr, ScheduleNodeAPIConfigSelect, ParamKeyConfigType, proto.ScheduleConfigTypeMigrateThreshold)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("modify schedule config failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}
	scs = make([]*proto.ScheduleConfig, 0)
	err = json.Unmarshal(reply.Data, &scs)
	for _, sc := range scs {
		if sc.ConfigType == proto.ScheduleConfigTypeMigrateThreshold && sc.ConfigKey == configKey1 && sc.ConfigValue == configValue1 {
			existed1 = true
		}
		if sc.ConfigType == proto.ScheduleConfigTypeMigrateThreshold && sc.ConfigKey == configKey2 && sc.ConfigValue == configValue2 {
			existed2 = true
		}
	}
	if !existed1 {
		t.Fatalf("modify failed")
	}
	if !existed2 {
		t.Fatalf("modify failed")
	}
}

func TestDeleteScheduleConfig(t *testing.T) {
	var (
		err error
		scs []*proto.ScheduleConfig
	)
	configKey := "smartVolume1"
	configValue := "0.3"
	sc := proto.NewScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold, configKey, configValue)
	err = mysql.DeleteScheduleConfig(sc)
	if err != nil && !strings.Contains(err.Error(), "affected rows less then one") {
		t.Fatalf(err.Error())
	}
	if err = mysql.AddScheduleConfig(sc); err != nil {
		t.Fatalf(err.Error())
	}

	reqURL := fmt.Sprintf("%v%v?%s=%v&%s=%s&%s=%v", ListenAddr, ScheduleNodeAPIConfigDelete,
		ParamKeyConfigType, sc.ConfigType,
		ParamKeyConfigKey, sc.ConfigKey,
		ParamKeyConfigValue, sc.ConfigValue)
	fmt.Println(reqURL)
	reply, err := process(reqURL)
	if err != nil {
		t.Fatalf(err.Error())
	}
	if reply.Code != 200 {
		fmt.Printf("modify schedule config failed, code(%v), message(%v)\n", reply.Code, reply.Msg)
		return
	}

	// select and check
	if scs, err = mysql.SelectScheduleConfig(proto.ScheduleConfigTypeMigrateThreshold); err != nil {
		t.Fatalf(err.Error())
	}
	var existed bool
	for _, sc := range scs {
		if sc.ConfigType == proto.ScheduleConfigTypeMigrateThreshold && sc.ConfigKey == configKey && sc.ConfigValue == configValue {
			existed = true
		}
	}
	if existed {
		t.Fatalf("delete failed")
	}
}

type HttpReply struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func process(reqURL string) (reply *HttpReply, err error) {
	resp, err := http.Get(reqURL)
	if err != nil {
		return
	}
	fmt.Println(resp.StatusCode)
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	reply = &HttpReply{}
	if err = json.Unmarshal(body, reply); err != nil {
		return
	}
	if reply.Code != 200 {
		err = errors.New(fmt.Sprintf("failed,msg[%v],data[%v]", reply.Msg, reply.Data))
		return
	}
	return
}

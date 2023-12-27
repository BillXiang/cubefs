package main

import (
	"flag"
	"fmt"
	"github.com/cubefs/cubefs/cli/cmd/data_check"
	"github.com/cubefs/cubefs/proto"
	syslog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cubefs/cubefs/util/exporter"
	_ "net/http/pprof"

	"github.com/cubefs/cubefs/cli/repaircrc/repaircrc_server"
	"github.com/cubefs/cubefs/util/config"
	"github.com/cubefs/cubefs/util/log"
)

var (
	CommitID   string
	BranchName string
	BuildTime  string
)
var (
	configFile    = flag.String("c", "", "config file path")
	configVersion = flag.Bool("v", false, "Show client version")
)
var (
	configKeyLogDir    = "logDir"
	configKeyLogLevel  = "logLevel"
	configKeyProfPort  = "prof"
	configKeyUmpPrefix = "umpPrefix"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()
	Version := proto.DumpVersion("CRC Server", BranchName, CommitID, BuildTime)
	if *configVersion {
		fmt.Printf("%v", Version)
		return nil
	}
	cfg, err := config.LoadConfigFile(*configFile)
	if err != nil {
		return err
	}
	umpPrefix := cfg.GetString(configKeyUmpPrefix)
	if umpPrefix != "" {
		data_check.UmpWarnKey = umpPrefix + "_" + data_check.UmpWarnKey
	}
	logDir := cfg.GetString(configKeyLogDir)
	logLevel := cfg.GetString(configKeyLogLevel)
	profPort := cfg.GetString(configKeyProfPort)

	var (
		server *repaircrc_server.RepairServer
	)
	server = repaircrc_server.NewServer()

	// Init logging
	var (
		level log.Level
	)
	switch strings.ToLower(logLevel) {
	case "debug":
		level = log.DebugLevel
	case "info":
		level = log.InfoLevel
	case "warn":
		level = log.WarnLevel
	case "error":
		level = log.ErrorLevel
	default:
		level = log.ErrorLevel
	}

	_, err = log.InitLog(logDir, "repair_server", level, nil)
	if err != nil {
		return err
	}
	defer log.LogFlush()

	exporter.Init(exporter.NewOptionFromConfig(cfg).WithCluster("check_crc_cluster").WithModule("check_ctc"))

	var profNetListener net.Listener = nil
	if profPort != "" {
		// 监听prof端口
		if profNetListener, err = net.Listen("tcp", fmt.Sprintf(":%v", profPort)); err != nil {
			log.LogErrorf("listen prof port %v failed: %v", profPort, err)
			log.LogFlush()
			syslog.Printf("Fatal: listen prof port %v failed: %v", profPort, err)
			return err
		}
		// 在prof端口监听上启动http API.
		go func() {
			_ = http.Serve(profNetListener, http.DefaultServeMux)
		}()
	}
	interceptSignal(server)
	if err = server.DoStart(cfg); err != nil {
		log.LogErrorf("start service failed: %v", err)
		log.LogFlush()
		syslog.Printf("Fatal: failed to start %v - ", err)
		return err
	}
	log.LogInfof("repair server started")
	// Block main goroutine until server shutdown.
	server.Sync()
	log.LogFlush()
	if profNetListener != nil {
		// 关闭prof端口监听
		_ = profNetListener.Close()
	}

	return nil
}

func interceptSignal(s *repaircrc_server.RepairServer) {
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	syslog.Println("action[interceptSignal] register system signal.")
	go func() {
		sig := <-sigC
		syslog.Printf("action[interceptSignal] received signal: %s.", sig.String())
		s.Shutdown()
	}()
}

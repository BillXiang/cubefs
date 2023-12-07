package monitor

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cubefs/cubefs/cmd/common"
	"github.com/cubefs/cubefs/util/config"
	"github.com/cubefs/cubefs/util/errors"
	"github.com/cubefs/cubefs/util/log"
	"github.com/cubefs/cubefs/util/statistics"
	"github.com/gorilla/mux"
)

var (
	// Regular expression used to verify the configuration of the service listening listenPort.
	// A valid service listening listenPort configuration is a string containing only numbers.
	regexpListen = regexp.MustCompile("^(\\d)+$")
)

type Monitor struct {
	port             string
	thriftAddr       string
	namespace        string
	queryIP          string
	clusters         []string
	splitRegionRules map[string]int64 	// clusterName:regionNum
	splitKeysForVol	 []string
	apiServer        *http.Server
	jmqConfig        *JMQConfig
	mqProducer       *MQProducer
	stopC            chan bool
	control          common.Control
}

func NewServer() *Monitor {
	return &Monitor{}
}

func (m *Monitor) Start(cfg *config.Config) error {
	return m.control.Start(m, cfg, doStart)
}

func (m *Monitor) Shutdown() {
	m.control.Shutdown(m, doShutdown)
}

func (m *Monitor) Sync() {
	m.control.Sync()
}

func doStart(s common.Server, cfg *config.Config) (err error) {
	m, ok := s.(*Monitor)
	if !ok {
		err = errors.New("Invalid Node Type")
		return
	}
	m.stopC = make(chan bool)
	// parse config
	if err = m.parseConfig(cfg); err != nil {
		return
	}

	if m.jmqConfig != nil {
		if m.mqProducer, err = initMQProducer(m.jmqConfig); err != nil {
			return
		}
	}

	// start http service
	m.startHTTPService()

	return
}

func doShutdown(s common.Server) {
	m, ok := s.(*Monitor)
	if !ok {
		return
	}
	// 1.http Server
	if m.apiServer != nil {
		if err := m.apiServer.Shutdown(context.Background()); err != nil {
			log.LogErrorf("action[Shutdown] failed, err: %v", err)
		}
	}
	// 2. MQProducer
	if m.mqProducer != nil {
		m.mqProducer.closeMQProducer()
	}
	close(m.stopC)
	return
}

func (m *Monitor) parseConfig(cfg *config.Config) (err error) {
	clusters := cfg.GetString(ConfigCluster)
	m.clusters = strings.Split(clusters, ",")

	listen := cfg.GetString(ConfigListenPort)
	if !regexpListen.MatchString(listen) {
		return fmt.Errorf("Port must be a string only contains numbers.")
	}
	m.port = listen

	thriftAddr := cfg.GetString(ConfigThriftAddr)
	m.thriftAddr = thriftAddr

	namespace := cfg.GetString(ConfigNamespace)
	if namespace == "" {
		namespace = defaultNamespace
	}
	m.namespace = namespace

	tableExpiredDay := cfg.GetInt64(ConfigExpiredDay)
	if tableExpiredDay > 0 {
		TableClearTime = time.Duration(tableExpiredDay) * 24 * time.Hour
	}

	queryIP := cfg.GetString(ConfigQueryIP)
	if queryIP == "" {
		queryIP = defaultQueryIP
	}
	m.queryIP = queryIP

	var (
		jmqTopic    = cfg.GetString(ConfigTopic)
		jmqAddress  = cfg.GetString(ConfigJMQAddress)
		jmqClientID = cfg.GetString(ConfigJMQClientID)
		producerNum = cfg.GetInt64(ConfigProducerNum)
	)
	if jmqTopic != "" && jmqAddress != "" && jmqClientID != "" {
		m.jmqConfig = &JMQConfig{
			topic:    strings.Split(jmqTopic, ","),
			address:  jmqAddress,
			clientID: jmqClientID,
			produceNum: func() int64 {
				if producerNum <= 0 {
					return defaultProducerNum
				}
				return producerNum
			}(),
		}
	}

	m.splitRegionRules = getSplitRules(cfg.GetStringSlice(ConfigSplitRegion))
	m.splitKeysForVol = cfg.GetStringSlice(ConfigSplitVol)

	log.LogInfof("action[parseConfig] load listen port(%v).", m.port)
	log.LogInfof("action[parseConfig] load cluster name(%v).", m.clusters)
	log.LogInfof("action[parseConfig] load table expired time(%v).", TableClearTime)
	log.LogInfof("action[parseConfig] load query ip(%v).", m.queryIP)
	log.LogInfof("action[parseConfig] load thrift server address(%v).", m.thriftAddr)
	if m.jmqConfig != nil {
		log.LogInfof("action[parseConfig] load JMQ topics(%v).", m.jmqConfig.topic)
		log.LogInfof("action[parseConfig] load JMQ address(%v).", m.jmqConfig.address)
		log.LogInfof("action[parseConfig] load JMQ clientID(%v).", m.jmqConfig.clientID)
		log.LogInfof("action[parseConfig] load producer num(%v).", m.jmqConfig.produceNum)
	}
	log.LogInfof("action[parseConfig] load splitRegionRules(%v).", m.splitRegionRules)
	log.LogInfof("action[parseConfig] load splitKeysForVol(%v).", m.splitKeysForVol)
	return
}

func (m *Monitor) registerAPIRoutes(router *mux.Router) {
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorCollect).
		HandlerFunc(m.collect)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorCluster).
		HandlerFunc(m.setCluster)

	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorClusterTopIP).
		HandlerFunc(m.getClusterTopIP)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorClusterTopVol).
		HandlerFunc(m.getClusterTopVol)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorOpTopIP).
		HandlerFunc(m.getOpTopIP)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorOpTopVol).
		HandlerFunc(m.getOpTopVol)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorTopPartition).
		HandlerFunc(m.getTopPartition)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorTopOp).
		HandlerFunc(m.getTopOp)
	router.NewRoute().Methods(http.MethodGet, http.MethodPost).
		Path(statistics.MonitorTopIP).
		HandlerFunc(m.getTopIP)
}

func (m *Monitor) startHTTPService() {
	router := mux.NewRouter().SkipClean(true)
	m.registerAPIRoutes(router)
	var server = &http.Server{
		Addr:    colonSplit + m.port,
		Handler: router,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			log.LogErrorf("serveAPI: serve http server failed: err(%v)", err)
			return
		}
	}()
	log.LogDebugf("startHTTPService successfully: port(%v)", m.port)
	m.apiServer = server
	return
}

func (m *Monitor) hasPrefixTable(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

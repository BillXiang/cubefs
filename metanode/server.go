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

package metanode

import (
	"context"
	"fmt"
	"github.com/cubefs/cubefs/util/exporter"
	"io"
	"net"
	"time"
	"github.com/cbnet/cbrdma"
	"github.com/cubefs/cubefs/util/unit"
	"strconv"
	"unsafe"
	"github.com/cubefs/cubefs/util/log"
)

func onRecv(conn *cbrdma.RDMAConn, buffer []byte, recvLen int, status int) {
	m := (*MetaNode) (conn.GetUserContext())
	p := NewPacket(context.Background())
	_ = p.UnmarshalHeader(buffer)
	p.Data = make([]byte, p.Size)
	copy(p.Data, buffer[unit.PacketHeaderSize: unit.PacketHeaderSize + p.Size])
	go m.handlePacket(conn, p, conn.RemoteAddr().String())
	return
}

func onDisconnected(conn *cbrdma.RDMAConn) {
	conn.Close()
	return
}

func onClosed(conn *cbrdma.RDMAConn) {
	conn.Close()
	return
}

func AcceptCbFunc(server *cbrdma.RDMAServer) *cbrdma.RDMAConn {
	conn := &cbrdma.RDMAConn{}
	conn.Init(onRecv, nil, onDisconnected, onClosed, server.GetUserContext())
	return conn
}

// StartTcpService binds and listens to the specified port.
func (m *MetaNode) startServer() (err error) {
	// initialize and start the server.
	m.httpStopC = make(chan uint8)
	ln, err := net.Listen("tcp", ":"+m.listen)
	if err != nil {
		return
	}

	m.rdmaServer = &cbrdma.RDMAServer{}
	m.rdmaServer.Init(AcceptCbFunc, onRecv, nil, onDisconnected, onClosed, unsafe.Pointer(m))
	port, _ := strconv.Atoi(m.listen)
	port += 10000
	if rdmaErr := m.rdmaServer.Listen(m.localAddr, port, 8 * KB, 8, 0); rdmaErr != nil {
		log.LogErrorf("rdma listen failed, err:%v", rdmaErr.Error())
		m.rdmaServer = nil
	}

	go func(stopC chan uint8) {
		var latestAlarm time.Time
		defer func() {
			ln.Close()
			if m.rdmaServer != nil {
				m.rdmaServer.Close()
			}
			m.rdmaServer = nil
		}()
		for {
			conn, err := ln.Accept()
			select {
			case <-stopC:
				return
			default:
			}
			if err != nil {
				log.LogErrorf("action[startTCPService] failed to accept, err:%s", err.Error())
				// Alarm only once in 1 minute
				if time.Now().Sub(latestAlarm) > time.Minute {
					message := fmt.Sprintf("SERVER ACCEPT CONNECTION FAILED!\n"+
						"Failed on accept connection from %v and will retry after 10s.\n"+
						"Error message: %s",
						ln.Addr(), err.Error())
					exporter.WarningCritical(message)
					latestAlarm = time.Now()
				}
				time.Sleep(time.Second * 5)
				continue
			}
			go m.serveConn(conn, stopC)
		}
	}(m.httpStopC)
	log.LogInfof("start server over...")
	return
}

func (m *MetaNode) stopServer() {
	if m.httpStopC != nil {
		defer func() {
			if r := recover(); r != nil {
				log.LogErrorf("action[StopTcpServer],err:%v", r)
			}
		}()
		close(m.httpStopC)
	}
}

const (
	MetaNodeServerTimeOut = 60*5
)
// Read data from the specified tcp connection until the connection is closed by the remote or the tcp service is down.
func (m *MetaNode) serveConn(conn net.Conn, stopC chan uint8) {
	defer conn.Close()
	c := conn.(*net.TCPConn)
	_ = c.SetKeepAlive(true) // Ignore error
	_ = c.SetNoDelay(true)   // Ignore error
	remoteAddr := conn.RemoteAddr().String()
	for {
		select {
		case <-stopC:
			return
		default:
		}
		p := NewPacket(context.Background())
		if err := p.ReadFromConn(conn, MetaNodeServerTimeOut); err != nil {
			if err != io.EOF {
				log.LogErrorf("conn (remote: %v) serve MetaNode: %v", remoteAddr, err.Error())
			}
			return
		}
		p.receiveTimestamp = time.Now().Unix()
		if err := m.handlePacket(conn, p, remoteAddr); err != nil {
			log.LogErrorf("serve handlePacket fail: %v", err)
		}
	}
}

func (m *MetaNode) handlePacket(conn net.Conn, p *Packet, remoteAddr string) (err error) {
	// Handle request
	err = m.metadataManager.HandleMetadataOperation(conn, p, remoteAddr)
	return
}

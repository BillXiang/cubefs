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

package stream

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/sdk/data/wrapper"
	"github.com/cubefs/cubefs/util"
	"github.com/cubefs/cubefs/util/errors"
)

var (
	TryOtherAddrError = errors.New("TryOtherAddrError")
	DpDiscardError    = errors.New("DpDiscardError")
)

const (
	StreamSendMaxRetry      = 200
	StreamSendSleepInterval = 100 * time.Millisecond
)

type GetReplyFunc func(conn *net.TCPConn) (err error, again bool)

// StreamConn defines the struct of the stream connection.
type StreamConn struct {
	dp       *wrapper.DataPartition
	currAddr string
}

var StreamConnPool = util.NewConnectPool()

// NewStreamConn returns a new stream connection.
func NewStreamConn(ctx context.Context, dp *wrapper.DataPartition, follower bool) (sc *StreamConn) {
	if !follower {
		sc = &StreamConn{
			dp:       dp,
			currAddr: dp.LeaderAddr,
		}
		return
	}

	defer func() {
		if sc.currAddr == "" {
			/*
			 * If followerRead is enabled, and there is no preferred choice,
			 * currAddr can be arbitrarily selected from the hosts.
			 */
			for _, h := range dp.Hosts {
				if h != "" {
					sc.currAddr = h
					break
				}
			}
		}
	}()

	if dp.ClientWrapper.NearRead() {
		sc = &StreamConn{
			dp:       dp,
			currAddr: getNearestHost(dp),
		}
		return
	}

	epoch := atomic.AddUint64(&dp.Epoch, 1)
	hosts := sortByStatus(ctx, dp, false)
	choice := len(hosts)
	currAddr := dp.LeaderAddr
	if choice > 0 {
		index := int(epoch) % choice
		currAddr = hosts[index]
	}

	sc = &StreamConn{
		dp:       dp,
		currAddr: currAddr,
	}
	return
}

// String returns the string format of the stream connection.
func (sc *StreamConn) String() string {
	return fmt.Sprintf("Partition(%v) CurrentAddr(%v) Hosts(%v)", sc.dp.PartitionID, sc.currAddr, sc.dp.Hosts)
}

// Send send the given packet over the network through the stream connection until success
// or the maximum number of retries is reached.
func (sc *StreamConn) Send(ctx context.Context, retry *bool, req *Packet, getReply GetReplyFunc) (err error) {
	span := proto.SpanFromContext(ctx)
	for i := 0; i < StreamSendMaxRetry; i++ {
		err = sc.sendToDataPartition(ctx, req, retry, getReply)
		if err == nil || err == proto.ErrCodeVersionOp || !*retry || err == TryOtherAddrError {
			return
		}
		span.Warnf("StreamConn Send: err(%v)", err)
		time.Sleep(StreamSendSleepInterval)
	}
	return errors.New(fmt.Sprintf("StreamConn Send: retried %v times and still failed, sc(%v) reqPacket(%v)", StreamSendMaxRetry, sc, req))
}

func (sc *StreamConn) sendToDataPartition(ctx context.Context, req *Packet, retry *bool, getReply GetReplyFunc) (err error) {
	span := proto.SpanFromContext(ctx)
	conn, err := StreamConnPool.GetConnect(sc.currAddr)
	if err == nil {
		span.Debugf("req opcode %v, conn %v", req.Opcode, conn)
		err = sc.sendToConn(ctx, conn, req, getReply)
		if err == nil {
			StreamConnPool.PutConnect(conn, false)
			return
		}
		span.Warnf("sendToDataPartition: send to curr addr failed, addr(%v) reqPacket(%v) err(%v)", sc.currAddr, req, err)
		StreamConnPool.PutConnect(conn, true)
		if err != TryOtherAddrError || !*retry {
			return
		}
	} else {
		span.Warnf("sendToDataPartition: get connection to curr addr failed, addr(%v) reqPacket(%v) err(%v)", sc.currAddr, req, err)
	}

	hosts := sortByStatus(ctx, sc.dp, true)

	for _, addr := range hosts {
		span.Warnf("sendToDataPartition: try addr(%v) reqPacket(%v)", addr, req)
		conn, err = StreamConnPool.GetConnect(addr)
		if err != nil {
			span.Warnf("sendToDataPartition: failed to get connection to addr(%v) reqPacket(%v) err(%v)", addr, req, err)
			continue
		}
		sc.currAddr = addr
		sc.dp.LeaderAddr = addr
		err = sc.sendToConn(ctx, conn, req, getReply)
		if err == nil {
			StreamConnPool.PutConnect(conn, false)
			return
		}
		StreamConnPool.PutConnect(conn, true)
		if err != TryOtherAddrError {
			return
		}
		span.Warnf("sendToDataPartition: try addr(%v) failed! reqPacket(%v) err(%v)", addr, req, err)
	}
	return errors.New(fmt.Sprintf("sendToPatition Failed: sc(%v) reqPacket(%v)", sc, req))
}

func (sc *StreamConn) sendToConn(ctx context.Context, conn *net.TCPConn, req *Packet, getReply GetReplyFunc) (err error) {
	span := proto.SpanFromContext(ctx)
	for i := 0; i < StreamSendMaxRetry; i++ {
		span.Debugf("sendToConn: send to addr(%v), reqPacket(%v)", sc.currAddr, req)
		err = req.WriteToConn(conn)
		if err != nil {
			msg := fmt.Sprintf("sendToConn: failed to write to addr(%v) err(%v)", sc.currAddr, err)
			span.Warn(msg)
			break
		}

		var again bool
		err, again = getReply(conn)
		if !again {
			if err != nil {
				span.Warnf("sendToConn: getReply error and RETURN, addr(%v) reqPacket(%v) err(%v)", sc.currAddr, req, err)
			}
			break
		}

		span.Warnf("sendToConn: getReply error and will RETRY, sc(%v) err(%v)", sc, err)
		time.Sleep(StreamSendSleepInterval)
	}

	span.Debugf("sendToConn exit: send to addr(%v) reqPacket(%v) err(%v)", sc.currAddr, req, err)
	return
}

// sortByStatus will return hosts list sort by host status for DataPartition.
// If param selectAll is true, hosts with status(true) is in front and hosts with status(false) is in behind.
// If param selectAll is false, only return hosts with status(true).
func sortByStatus(ctx context.Context, dp *wrapper.DataPartition, selectAll bool) (hosts []string) {
	span := proto.SpanFromContext(ctx)
	var failedHosts []string
	hostsStatus := dp.ClientWrapper.HostsStatus
	var dpHosts []string
	if dp.ClientWrapper.FollowerRead() && dp.ClientWrapper.NearRead() {
		dpHosts = dp.NearHosts
		if len(dpHosts) == 0 {
			dpHosts = dp.Hosts
		}
	} else {
		dpHosts = dp.Hosts
	}

	for _, addr := range dpHosts {
		status, ok := hostsStatus[addr]
		if ok {
			if status {
				hosts = append(hosts, addr)
			} else {
				failedHosts = append(failedHosts, addr)
			}
		} else {
			failedHosts = append(failedHosts, addr)
			span.Warnf("sortByStatus: can not find host[%v] in HostsStatus, dp[%d]", addr, dp.PartitionID)
		}
	}

	if selectAll {
		hosts = append(hosts, failedHosts...)
	}

	return
}

func getNearestHost(dp *wrapper.DataPartition) string {
	hostsStatus := dp.ClientWrapper.HostsStatus
	for _, addr := range dp.NearHosts {
		status, ok := hostsStatus[addr]
		if ok {
			if !status {
				continue
			}
		}
		return addr
	}
	return dp.LeaderAddr
}

func NewStreamConnByHost(host string) *StreamConn {
	return &StreamConn{
		currAddr: host,
	}
}

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

package meta

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/util/errors"
	"github.com/chubaofs/chubaofs/util/log"
	"github.com/chubaofs/chubaofs/util/tracing"
)

const (
	SendRetryLimit    = 100
	SendRetryInterval = 100 * time.Millisecond
	SendTimeLimit     = 20 * time.Second
)

type MetaConn struct {
	conn *net.TCPConn
	id   uint64 //PartitionID
	addr string //MetaNode addr
}

// Connection managements
//

func (mc *MetaConn) String() string {
	if mc == nil {
		return ""
	}
	return fmt.Sprintf("partitionID(%v) addr(%v)", mc.id, mc.addr)
}

func (mw *MetaWrapper) getConn(ctx context.Context, partitionID uint64, addr string) (*MetaConn, error) {
	var tracer = tracing.TracerFromContext(ctx).ChildTracer("MetaWrapper.getConn").
		SetTag("volume", mw.volname).
		SetTag("partitionID", partitionID).
		SetTag("address", addr)
	defer tracer.Finish()
	ctx = tracer.Context()

	conn, err := mw.conns.GetConnect(addr)
	if err != nil {
		log.LogWarnf("GetConnect conn: addr(%v) err(%v)", addr, err)
		return nil, err
	}
	mc := &MetaConn{conn: conn, id: partitionID, addr: addr}
	return mc, nil
}

func (mw *MetaWrapper) putConn(mc *MetaConn, err error) {
	mw.conns.PutConnectWithErr(mc.conn, err)
}

func (mw *MetaWrapper) sendWriteToMP(ctx context.Context, mp *MetaPartition, req *proto.Packet) (resp *proto.Packet, err error) {
	addr := mp.LeaderAddr
	resp, err = mw.sendToMetaPartition(ctx, mp, req, addr)
	return
}

func (mw *MetaWrapper) sendReadToMP(ctx context.Context, mp *MetaPartition, req *proto.Packet) (resp *proto.Packet, err error) {
	addr := mp.LeaderAddr
	resp, err = mw.sendToMetaPartition(ctx, mp, req, addr)
	if err == nil && resp != nil {
		return
	}
	log.LogWarnf("sendReadToMP: send to leader failed and try to read consistent, req(%v) mp(%v) err(%v) resp(%v)", req, mp, err, resp)
	resp, err = mw.readConsistentFromHosts(ctx, mp, req)
	return
}

func (mw *MetaWrapper) readConsistentFromHosts(ctx context.Context, mp *MetaPartition, req *proto.Packet) (resp *proto.Packet, err error) {
	// compare applied ID of replicas and choose the max one
	targetHosts, isErr := mw.GetMaxAppliedIDHosts(ctx, mp)
	if !isErr && len(targetHosts) > 0 {
		req.ArgLen = 1
		req.Arg = make([]byte, req.ArgLen)
		req.Arg[0] = proto.FollowerReadFlag
		for _, host := range targetHosts {
			resp, err = mw.sendToHost(ctx, mp, req, host)
			if err == nil && resp.ResultCode == proto.OpOk {
				return
			}
			log.LogWarnf("mp readConsistentFromHosts: failed req(%v) mp(%v) addr(%v) err(%v) resp(%v), try next host", req, mp, host, err, resp)
		}
	}
	log.LogWarnf("mp readConsistentFromHosts exit: failed req(%v) mp(%v) isErr(%v) targetHosts(%v), try next host", req, mp, isErr, targetHosts)
	return nil, errors.New(fmt.Sprintf("readConsistentFromHosts: failed, req(%v) mp(%v) isErr(%v) targetHosts(%v), try next host", req, mp, isErr, targetHosts))
}

func (mw *MetaWrapper) sendToMetaPartition(ctx context.Context, mp *MetaPartition, req *proto.Packet, addr string) (resp *proto.Packet, err error) {
	var tracer = tracing.TracerFromContext(ctx).ChildTracer("MetaWrapper.sendToMetaPartition").
		SetTag("mpID", mp.PartitionID).
		SetTag("reqID", req.ReqID).
		SetTag("reqSize", req.Size).
		SetTag("reqOp", req.GetOpMsg())
	defer tracer.Finish()
	ctx = tracer.Context()

	var (
		errMap map[int]error
		start  time.Time
		j      int
	)
	resp, err = mw.sendToHost(ctx, mp, req, addr)
	if err == nil && !resp.ShouldRetry() {
		goto out
	}
	log.LogWarnf("sendToMetaPartition: leader failed req(%v) mp(%v) addr(%v) err(%v) resp(%v)", req, mp, addr, err, resp)

	errMap = make(map[int]error, len(mp.Members))
	start = time.Now()

	for i := 0; i < SendRetryLimit; i++ {

		for j, addr = range mp.Members {
			resp, err = mw.sendToHost(ctx, mp, req, addr)
			if err == nil && !resp.ShouldRetry() {
				goto out
			}
			if err == nil {
				err = errors.New(fmt.Sprintf("request should retry[%v]", resp.GetResultMsg()))
			}
			errMap[j] = err
			log.LogWarnf("sendToMetaPartition: retry failed req(%v) mp(%v) addr(%v) err(%v) resp(%v)", req, mp, addr, err, resp)
		}
		if time.Since(start) > SendTimeLimit {
			log.LogWarnf("sendToMetaPartition: retry timeout req(%v) mp(%v) time(%v)", req, mp, time.Since(start))
			break
		}
		log.LogWarnf("sendToMetaPartition: req(%v) mp(%v) retry in (%v)", req, mp, SendRetryInterval)
		time.Sleep(SendRetryInterval)
	}

out:
	if err != nil || resp == nil {
		return nil, errors.New(fmt.Sprintf("sendToMetaPartition failed: req(%v) mp(%v) errs(%v) resp(%v)", req, mp, errMap, resp))
	}
	log.LogDebugf("sendToMetaPartition successful: req(%v) mp(%v) addr(%v) resp(%v)", req, mp, addr, resp)
	return resp, nil
}

func (mw *MetaWrapper) sendToHost(ctx context.Context, mp *MetaPartition, req *proto.Packet, addr string) (resp *proto.Packet, err error) {
	var tracer = tracing.TracerFromContext(ctx).ChildTracer("MetaWrapper.sendToHost").
		SetTag("mpID", mp.PartitionID).
		SetTag("reqID", req.ReqID).
		SetTag("reqSize", req.Size).
		SetTag("reqOp", req.GetOpMsg()).
		SetTag("address", addr)
	defer tracer.Finish()
	ctx = tracer.Context()

	var mc *MetaConn
	if addr == "" {
		return nil, errors.New(fmt.Sprintf("sendToHost failed: leader addr empty, req(%v) mp(%v)", req, mp))
	}
	mc, err = mw.getConn(ctx, mp.PartitionID, addr)
	if err != nil {
		return
	}
	defer func() {
		mw.putConn(mc, err)
	}()

	// Write to connection with tracing.
	if err = func() (err error) {
		var tracer = tracing.TracerFromContext(ctx).ChildTracer("MetaConn.send[WriteToConn]").
			SetTag("remote", mc.conn.RemoteAddr().String())
		defer tracer.Finish()
		err = req.WriteToConn(mc.conn)
		tracer.SetTag("error", err)
		return
	}(); err != nil {
		return nil, errors.Trace(err, "Failed to write to conn, req(%v)", req)
	}

	resp = proto.NewPacket(req.Ctx())

	// Read from connection with tracing.
	if err = func() (err error) {
		var tracer = tracing.TracerFromContext(ctx).ChildTracer("MetaWrapper.send[ReadFromConn]").
			SetTag("remote", mc.conn.RemoteAddr())
		defer tracer.Finish()
		err = resp.ReadFromConn(mc.conn, proto.ReadDeadlineTime)
		tracer.SetTag("error", err)
		return
	}(); err != nil {
		tracer.SetTag("error", err)
		return nil, errors.Trace(err, "Failed to read from conn, req(%v)", req)
	}
	// Check if the ID and OpCode of the response are consistent with the request.
	if resp.ReqID != req.ReqID || resp.Opcode != req.Opcode {
		log.LogErrorf("sendToHost err: the response packet mismatch with request: conn(%v to %v) req(%v) resp(%v)",
			mc.conn.LocalAddr(), mc.conn.RemoteAddr(), req, resp)
		err = syscall.EBADMSG
		return nil, err
	}
	log.LogDebugf("sendToHost successful: mp(%v) addr(%v) req(%v) resp(%v)", mp, addr, req, resp)
	return resp, nil
}

func sortMembers(leader string, members []string) []string {
	if leader == "" {
		return members
	}
	for i, addr := range members {
		if addr == leader {
			members[i], members[0] = members[0], members[i]
			break
		}
	}
	return members
}

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

package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cubefs/cubefs/depends/bazil.org/fuse"
	"github.com/cubefs/cubefs/depends/bazil.org/fuse/fs"

	"github.com/cubefs/cubefs/proto"
	"github.com/cubefs/cubefs/sdk/meta"
	"github.com/cubefs/cubefs/util/auditlog"
	"github.com/cubefs/cubefs/util/exporter"
	"github.com/cubefs/cubefs/util/log"
	"github.com/cubefs/cubefs/util/stat"
)

// used to locate the position in parent
type DirContext struct {
	Name string
}

type DirContexts struct {
	sync.RWMutex
	dirCtx map[fuse.HandleID]*DirContext
}

func NewDirContexts() (dctx *DirContexts) {
	dctx = &DirContexts{}
	dctx.dirCtx = make(map[fuse.HandleID]*DirContext)
	return
}

func (dctx *DirContexts) GetCopy(handle fuse.HandleID) DirContext {
	dctx.RLock()
	dirCtx, found := dctx.dirCtx[handle]
	dctx.RUnlock()

	if found {
		return DirContext{dirCtx.Name}
	} else {
		return DirContext{}
	}
}

func (dctx *DirContexts) Put(handle fuse.HandleID, dirCtx *DirContext) {
	dctx.Lock()
	defer dctx.Unlock()

	oldCtx, found := dctx.dirCtx[handle]
	if found {
		oldCtx.Name = dirCtx.Name
		return
	}

	dctx.dirCtx[handle] = dirCtx
}

func (dctx *DirContexts) Remove(handle fuse.HandleID) {
	dctx.Lock()
	delete(dctx.dirCtx, handle)
	dctx.Unlock()
}

// Dir defines the structure of a directory
type Dir struct {
	super     *Super
	info      *proto.InodeInfo
	dcache    *DentryCache
	dctx      *DirContexts
	parentIno uint64
	name      string
}

// Functions that Dir needs to implement
var (
	_ fs.Node                = (*Dir)(nil)
	_ fs.NodeCreater         = (*Dir)(nil)
	_ fs.NodeForgetter       = (*Dir)(nil)
	_ fs.NodeMkdirer         = (*Dir)(nil)
	_ fs.NodeMknoder         = (*Dir)(nil)
	_ fs.NodeRemover         = (*Dir)(nil)
	_ fs.NodeFsyncer         = (*Dir)(nil)
	_ fs.NodeRequestLookuper = (*Dir)(nil)
	_ fs.HandleReadDirAller  = (*Dir)(nil)
	_ fs.NodeRenamer         = (*Dir)(nil)
	_ fs.NodeSetattrer       = (*Dir)(nil)
	_ fs.NodeSymlinker       = (*Dir)(nil)
	_ fs.NodeGetxattrer      = (*Dir)(nil)
	_ fs.NodeListxattrer     = (*Dir)(nil)
	_ fs.NodeSetxattrer      = (*Dir)(nil)
	_ fs.NodeRemovexattrer   = (*Dir)(nil)
)

// NewDir returns a new directory.
func NewDir(s *Super, i *proto.InodeInfo, pino uint64, dirName string) fs.Node {
	return &Dir{
		super:     s,
		info:      i,
		parentIno: pino,
		name:      dirName,
		dctx:      NewDirContexts(),
	}
}

// Attr set the attributes of a directory.
func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	ctx = ctxOperation(ctx, "DirAttr")
	span := getSpan(ctx)
	var err error
	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Attr", err, bgTime, 1)
	}()

	ino := d.info.Inode
	info, err := d.super.InodeGet(ctx, ino)
	if err != nil {
		span.Errorf("Attr: ino(%v) err(%v)", ino, err)
		return ParseError(err)
	}
	fillAttr(info, a)
	span.Debugf("TRACE Attr: inode(%v)", info)
	return nil
}

func (d *Dir) Release(ctx context.Context, req *fuse.ReleaseRequest) (err error) {
	d.dctx.Remove(req.Handle)
	return nil
}

// Create handles the create request.
func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	ctx = ctxOperation(ctx, "DirCreate")
	span := getSpan(ctx)
	start := time.Now()

	bgTime := stat.BeginStat()
	var err error
	var newInode uint64
	metric := exporter.NewTPCnt("filecreate")
	fullPath := path.Join(d.getCwd(ctx), req.Name)
	defer func() {
		stat.EndStat("Create", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
		auditlog.LogClientOp("Create", fullPath, "nil", err, time.Since(start).Microseconds(), newInode, 0)
	}()

	info, err := d.super.mw.Create_ll(ctx, d.info.Inode, req.Name, proto.Mode(req.Mode.Perm()), req.Uid, req.Gid, nil, fullPath)
	if err != nil {
		span.Errorf("Create: parent(%v) req(%v) err(%v)", d.info.Inode, req, err)
		return nil, nil, ParseError(err)
	}

	d.super.ic.Put(info)
	child := NewFile(d.super, info, uint32(req.Flags&DefaultFlag), d.info.Inode, req.Name)
	newInode = info.Inode

	d.super.ec.OpenStream(ctx, info.Inode)
	d.super.fslock.Lock()
	d.super.nodeCache[info.Inode] = child
	d.super.fslock.Unlock()

	if d.super.keepCache {
		resp.Flags |= fuse.OpenKeepCache
	}
	resp.EntryValid = LookupValidDuration

	d.super.ic.Delete(d.info.Inode)

	elapsed := time.Since(start)
	span.Debugf("TRACE Create: parent(%v) req(%v) resp(%v) ino(%v) (%v)ns", d.info.Inode, req, resp, info.Inode, elapsed.Nanoseconds())
	return child, child, nil
}

// Forget is called when the evict is invoked from the kernel.
func (d *Dir) Forget() {
	bgTime := stat.BeginStat()
	ino := d.info.Inode
	defer func() {
		stat.EndStat("Forget", nil, bgTime, 1)
		log.Debugf("TRACE Forget: ino(%v)", ino)
	}()

	d.super.ic.Delete(ino)

	d.super.fslock.Lock()
	delete(d.super.nodeCache, ino)
	d.super.fslock.Unlock()
}

// Mkdir handles the mkdir request.
func (d *Dir) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	ctx = ctxOperation(ctx, "DirMkdir")
	span := getSpan(ctx)
	start := time.Now()

	bgTime := stat.BeginStat()
	var err error
	var newInode uint64
	metric := exporter.NewTPCnt("mkdir")
	fullPath := path.Join(d.getCwd(ctx), req.Name)
	defer func() {
		stat.EndStat("Mkdir", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
		auditlog.LogClientOp("Mkdir", fullPath, "nil", err, time.Since(start).Microseconds(), newInode, 0)
	}()

	info, err := d.super.mw.Create_ll(ctx, d.info.Inode, req.Name, proto.Mode(os.ModeDir|req.Mode.Perm()), req.Uid, req.Gid, nil, fullPath)
	if err != nil {
		span.Errorf("Mkdir: parent(%v) req(%v) err(%v)", d.info.Inode, req, err)
		return nil, ParseError(err)
	}

	d.super.ic.Put(info)
	child := NewDir(d.super, info, d.info.Inode, req.Name)
	newInode = info.Inode
	d.super.fslock.Lock()
	d.super.nodeCache[info.Inode] = child
	d.super.fslock.Unlock()

	d.super.ic.Delete(d.info.Inode)

	elapsed := time.Since(start)
	span.Debugf("TRACE Mkdir: parent(%v) req(%v) ino(%v) (%v)ns", d.info.Inode, req, info.Inode, elapsed.Nanoseconds())
	return child, nil
}

// Remove handles the remove request.
func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	ctx = ctxOperation(ctx, "DirRemove")
	span := getSpan(ctx)
	start := time.Now()
	d.dcache.Delete(req.Name)
	dcacheKey := d.buildDcacheKey(d.info.Inode, req.Name)
	d.super.dc.Delete(dcacheKey)

	bgTime := stat.BeginStat()
	var err error
	var deletedInode uint64
	metric := exporter.NewTPCnt("remove")
	fullPath := path.Join(d.getCwd(ctx), req.Name)
	defer func() {
		stat.EndStat("Remove", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
		auditlog.LogClientOp("Remove", fullPath, "nil", err, time.Since(start).Microseconds(), deletedInode, 0)
	}()

	info, err := d.super.mw.Delete_ll(ctx, d.info.Inode, req.Name, req.Dir, fullPath)
	if err != nil {
		span.Errorf("Remove: parent(%v) name(%v) err(%v)", d.info.Inode, req.Name, err)
		return ParseError(err)
	}

	if info != nil {
		deletedInode = info.Inode
	}
	d.super.ic.Delete(d.info.Inode)

	if info != nil && info.Nlink == 0 && !proto.IsDir(info.Mode) {
		d.super.orphan.Put(info.Inode)
		span.Debugf("Remove: add to orphan inode list, ino(%v)", info.Inode)
	}

	elapsed := time.Since(start)
	span.Debugf("TRACE Remove: parent(%v) req(%v) inode(%v) (%v)ns", d.info.Inode, req, info, elapsed.Nanoseconds())
	return nil
}

func (d *Dir) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	return nil
}

// Lookup handles the lookup request.
func (d *Dir) Lookup(ctx context.Context, req *fuse.LookupRequest, resp *fuse.LookupResponse) (fs.Node, error) {
	ctx = ctxOperation(ctx, "DirLookup")
	span := getSpan(ctx)
	var (
		ino      uint64
		err      error
		dcachev2 bool
	)

	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Lookup", err, bgTime, 1)
	}()

	span.Debugf("TRACE Lookup: parent(%v) req(%v)", d.info.Inode, req)
	span.Debugf("TRACE Lookup: parent(%v) path(%v) d.super.bcacheDir(%v)", d.info.Inode, d.getCwd(ctx), d.super.bcacheDir)

	if d.needDentrycache(ctx) {
		dcachev2 = true
	}
	if dcachev2 {
		lookupMetric := exporter.NewCounter("lookupDcache")
		lookupMetric.AddWithLabels(1, map[string]string{exporter.Vol: d.super.volname})
		dcacheKey := d.buildDcacheKey(d.info.Inode, req.Name)
		dentryInfo := d.super.dc.Get(dcacheKey)
		if dentryInfo == nil {
			lookupMetric := exporter.NewCounter("lookupDcacheMiss")
			lookupMetric.AddWithLabels(1, map[string]string{exporter.Vol: d.super.volname})
			ino, _, err = d.super.mw.Lookup_ll(ctx, d.info.Inode, req.Name)
			if err != nil {
				if err != syscall.ENOENT {
					span.Errorf("Lookup: parent(%v) name(%v) err(%v)", d.info.Inode, req.Name, err)
				}
				return nil, ParseError(err)
			}
			info := &proto.DentryInfo{
				Name:  dcacheKey,
				Inode: ino,
			}
			d.super.dc.Put(info)
		} else {
			lookupMetric := exporter.NewCounter("lookupDcacheHit")
			lookupMetric.AddWithLabels(1, map[string]string{exporter.Vol: d.super.volname})
			ino = dentryInfo.Inode
		}
	} else {
		cino, ok := d.dcache.Get(req.Name)
		if !ok {
			cino, _, err = d.super.mw.Lookup_ll(ctx, d.info.Inode, req.Name)
			if err != nil {
				if err != syscall.ENOENT {
					span.Errorf("Lookup: parent(%v) name(%v) err(%v)", d.info.Inode, req.Name, err)
				}
				return nil, ParseError(err)
			}
		}
		ino = cino
	}

	info, err := d.super.InodeGet(ctx, ino)
	if err != nil {
		span.Errorf("Lookup: parent(%v) name(%v) ino(%v) err(%v)", d.info.Inode, req.Name, ino, err)
		dummyInodeInfo := &proto.InodeInfo{Inode: ino}
		dummyChild := NewFile(d.super, dummyInodeInfo, DefaultFlag, d.info.Inode, req.Name)
		return dummyChild, nil
	}
	mode := proto.OsMode(info.Mode)
	d.super.fslock.Lock()
	child, ok := d.super.nodeCache[ino]
	if !ok {
		if mode.IsDir() {
			child = NewDir(d.super, info, d.info.Inode, req.Name)
		} else {
			child = NewFile(d.super, info, DefaultFlag, d.info.Inode, req.Name)
		}
		d.super.nodeCache[ino] = child
	}
	d.super.fslock.Unlock()

	resp.EntryValid = LookupValidDuration

	span.Debugf("TRACE Lookup exit: parent(%v) req(%v) cost (%d)", d.info.Inode, req, time.Since(*bgTime).Microseconds())
	return child, nil
}

func (d *Dir) buildDcacheKey(inode uint64, name string) string {
	return fmt.Sprintf("%v_%v", inode, name)
}

func (d *Dir) ReadDir(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) ([]fuse.Dirent, error) {
	ctx = ctxOperation(ctx, "DirReadDir")
	span := getSpan(ctx)
	var err error
	var limit uint64 = DefaultReaddirLimit
	start := time.Now()

	bgTime := stat.BeginStat()
	// var err error
	metric := exporter.NewTPCnt("readdir")
	defer func() {
		stat.EndStat("ReadDirLimit", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
	}()
	var dirCtx DirContext
	if req.Offset != 0 {
		dirCtx = d.dctx.GetCopy(req.Handle)
	} else {
		dirCtx = DirContext{}
	}
	children, err := d.super.mw.ReadDirLimit_ll(ctx, d.info.Inode, dirCtx.Name, limit)
	if err != nil {
		span.Errorf("readdirlimit: Readdir: ino(%v) err(%v) offset %v", d.info.Inode, err, req.Offset)
		return make([]fuse.Dirent, 0), ParseError(err)
	}

	if req.Offset == 0 {
		if len(children) == 0 {
			dirents := make([]fuse.Dirent, 0, len(children))
			dirents = append(dirents, fuse.Dirent{
				Inode: d.info.Inode,
				Type:  fuse.DT_Dir,
				Name:  ".",
			})
			pid := uint64(req.Pid)
			if d.info.Inode == 1 {
				pid = d.info.Inode
			}
			dirents = append(dirents, fuse.Dirent{
				Inode: pid,
				Type:  fuse.DT_Dir,
				Name:  "..",
			})
			return dirents, io.EOF
		}
		children = append([]proto.Dentry{{
			Name:  ".",
			Inode: d.info.Inode,
			Type:  uint32(os.ModeDir),
		}, {
			Name:  "..",
			Inode: uint64(req.Pid),
			Type:  uint32(os.ModeDir),
		}}, children...)
	}

	// skip the first one, which is already accessed
	childrenNr := uint64(len(children))
	if childrenNr == 0 || (dirCtx.Name != "" && childrenNr == 1) {
		return make([]fuse.Dirent, 0), io.EOF
	} else if childrenNr < limit {
		err = io.EOF
	}
	if dirCtx.Name != "" {
		children = children[1:]
	}

	/* update dirCtx */
	dirCtx.Name = children[len(children)-1].Name
	d.dctx.Put(req.Handle, &dirCtx)

	inodes := make([]uint64, 0, len(children))
	dirents := make([]fuse.Dirent, 0, len(children))

	span.Debugf("Readdir ino(%v) path(%v) d.super.bcacheDir(%v)", d.info.Inode, d.getCwd(ctx), d.super.bcacheDir)
	var dcache *DentryCache
	if !d.super.disableDcache {
		dcache = NewDentryCache()
	}

	var dcachev2 bool
	if d.needDentrycache(ctx) {
		dcachev2 = true
	}

	for _, child := range children {
		dentry := fuse.Dirent{
			Inode: child.Inode,
			Type:  ParseType(child.Type),
			Name:  child.Name,
		}

		inodes = append(inodes, child.Inode)
		dirents = append(dirents, dentry)
		if dcachev2 {
			info := &proto.DentryInfo{
				Name:  d.buildDcacheKey(d.info.Inode, child.Name),
				Inode: child.Inode,
			}
			d.super.dc.Put(info)
		} else {
			dcache.Put(child.Name, child.Inode)
		}
	}

	infos := d.super.mw.BatchInodeGet(ctx, inodes)
	for _, info := range infos {
		d.super.ic.Put(info)
	}

	d.dcache = dcache
	elapsed := time.Since(start)
	span.Debugf("TRACE ReadDir exit: ino(%v) (%v)ns %v", d.info.Inode, elapsed.Nanoseconds(), req)
	return dirents, err
}

// ReadDirAll gets all the dentries in a directory and puts them into the cache.
func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	ctx = ctxOperation(ctx, "DirReadDirAll")
	span := getSpan(ctx)
	start := time.Now()
	bgTime := stat.BeginStat()
	var err error
	metric := exporter.NewTPCnt("readdir")
	defer func() {
		stat.EndStat("ReadDirAll", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
	}()

	// transform ReadDirAll to ReadDirLimit_ll
	noMore := false
	from := ""
	var children []proto.Dentry
	for !noMore {
		batches, err := d.super.mw.ReadDirLimit_ll(ctx, d.info.Inode, from, DefaultReaddirLimit)
		if err != nil {
			span.Errorf("Readdir: ino(%v) err(%v) from(%v)", d.info.Inode, err, from)
			return make([]fuse.Dirent, 0), ParseError(err)
		}
		batchNr := uint64(len(batches))
		if batchNr == 0 || (from != "" && batchNr == 1) {
			break
		} else if batchNr < DefaultReaddirLimit {
			noMore = true
		}
		if from != "" {
			batches = batches[1:]
		}
		children = append(children, batches...)
		from = batches[len(batches)-1].Name
	}

	inodes := make([]uint64, 0, len(children))
	dirents := make([]fuse.Dirent, 0, len(children))

	span.Debugf("Readdir ino(%v) path(%v) d.super.bcacheDir(%v)", d.info.Inode, d.getCwd(ctx), d.super.bcacheDir)
	var dcache *DentryCache
	if !d.super.disableDcache {
		dcache = NewDentryCache()
	}

	var dcachev2 bool
	if d.needDentrycache(ctx) {
		dcachev2 = true
	}

	for _, child := range children {
		dentry := fuse.Dirent{
			Inode: child.Inode,
			Type:  ParseType(child.Type),
			Name:  child.Name,
		}

		inodes = append(inodes, child.Inode)
		dirents = append(dirents, dentry)
		if dcachev2 {
			info := &proto.DentryInfo{
				Name:  d.buildDcacheKey(d.info.Inode, child.Name),
				Inode: child.Inode,
			}
			d.super.dc.Put(info)
		} else {
			dcache.Put(child.Name, child.Inode)
		}
	}

	infos := d.super.mw.BatchInodeGet(ctx, inodes)
	for _, info := range infos {
		d.super.ic.Put(info)
	}
	d.dcache = dcache
	elapsed := time.Since(start)
	span.Debugf("TRACE ReadDirAll: ino(%v) (%v)ns", d.info.Inode, elapsed.Nanoseconds())
	return dirents, nil
}

// Rename handles the rename request.
func (d *Dir) Rename(ctx context.Context, req *fuse.RenameRequest, newDir fs.Node) error {
	ctx = ctxOperation(ctx, "DirRename")
	span := getSpan(ctx)
	dstDir, ok := newDir.(*Dir)
	if !ok {
		span.Errorf("Rename: NOT DIR, parent(%v) req(%v)", d.info.Inode, req)
		return fuse.ENOTSUP
	}
	start := time.Now()
	var srcInode uint64 // must exist
	var dstInode uint64 // may not exist
	var err error
	if ino, ok := dstDir.dcache.Get(req.NewName); ok {
		dstInode = ino
	}
	if ino, ok := d.dcache.Get(req.OldName); ok {
		srcInode = ino
	} else {
		// will not get there
		if ino, _, err := d.super.mw.Lookup_ll(ctx, d.info.Inode, req.OldName); err == nil {
			srcInode = ino
		}
	}
	d.dcache.Delete(req.OldName)
	dcacheKey := d.buildDcacheKey(d.info.Inode, req.OldName)
	d.super.dc.Delete(dcacheKey)

	bgTime := stat.BeginStat()

	metric := exporter.NewTPCnt("rename")
	srcPath := path.Join(d.getCwd(ctx), req.OldName)
	dstPath := path.Join(dstDir.getCwd(ctx), req.NewName)
	defer func() {
		stat.EndStat("Rename", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
		d.super.fslock.Lock()
		node, ok := d.super.nodeCache[srcInode]
		if ok && srcInode != 0 {
			if dir, ok := node.(*Dir); ok {
				dir.name = req.NewName
				dir.parentIno = dstDir.info.Inode
			} else {
				file := node.(*File)
				file.name = req.NewName
				file.parentIno = dstDir.info.Inode
			}
		}
		d.super.fslock.Unlock()
		auditlog.LogClientOp("Rename", srcPath, dstPath, err, time.Since(start).Microseconds(), srcInode, dstInode)
	}()
	// changePathMap := d.super.mw.GetChangeQuota(d.getCwd(ctx)+"/"+req.OldName, dstDir.getCwd(ctx)+"/"+req.NewName)
	if d.super.mw.EnableQuota {
		if !d.canRenameByQuota(ctx, dstDir, req.OldName) {
			return fuse.EPERM
		}
	}
	err = d.super.mw.Rename_ll(ctx, d.info.Inode, req.OldName, dstDir.info.Inode, req.NewName, srcPath, dstPath, true)
	if err != nil {
		span.Errorf("Rename: parent(%v) req(%v) err(%v)", d.info.Inode, req, err)
		return ParseError(err)
	}
	// if len(changePathMap) != 0 {
	// 	d.super.mw.BatchModifyQuotaPath(changePathMap)
	// }
	d.super.ic.Delete(d.info.Inode)
	d.super.ic.Delete(dstDir.info.Inode)

	elapsed := time.Since(start)
	span.Debugf("TRACE Rename: SrcParent(%v) OldName(%v) DstParent(%v) NewName(%v) (%v)ns", d.info.Inode, req.OldName, dstDir.info.Inode, req.NewName, elapsed.Nanoseconds())
	return nil
}

// Setattr handles the setattr request.
func (d *Dir) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	ctx = ctxOperation(ctx, "DirSetattr")
	span := getSpan(ctx)
	var err error
	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Setattr", err, bgTime, 1)
	}()

	ino := d.info.Inode
	start := time.Now()
	info, err := d.super.InodeGet(ctx, ino)
	if err != nil {
		span.Errorf("Setattr: ino(%v) err(%v)", ino, err)
		return ParseError(err)
	}

	if valid := setattr(info, req); valid != 0 {
		err = d.super.mw.Setattr(ctx, ino, valid, info.Mode, info.Uid, info.Gid, info.AccessTime.Unix(),
			info.ModifyTime.Unix())
		if err != nil {
			d.super.ic.Delete(ino)
			return ParseError(err)
		}
	}

	fillAttr(info, &resp.Attr)

	elapsed := time.Since(start)
	span.Debugf("TRACE Setattr: ino(%v) req(%v) inodeSize(%v) (%v)ns", ino, req, info.Size, elapsed.Nanoseconds())
	return nil
}

func (d *Dir) Mknod(ctx context.Context, req *fuse.MknodRequest) (fs.Node, error) {
	if req.Rdev != 0 {
		return nil, fuse.ENOSYS
	}

	ctx = ctxOperation(ctx, "DirMknod")
	span := getSpan(ctx)
	start := time.Now()

	bgTime := stat.BeginStat()
	var err error
	metric := exporter.NewTPCnt("mknod")
	defer func() {
		stat.EndStat("Mknod", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
	}()
	fullPath := path.Join(d.getCwd(ctx), req.Name)
	info, err := d.super.mw.Create_ll(ctx, d.info.Inode, req.Name, proto.Mode(req.Mode), req.Uid, req.Gid, nil, fullPath)
	if err != nil {
		span.Errorf("Mknod: parent(%v) req(%v) err(%v)", d.info.Inode, req, err)
		return nil, ParseError(err)
	}

	d.super.ic.Put(info)
	child := NewFile(d.super, info, DefaultFlag, d.info.Inode, req.Name)

	d.super.fslock.Lock()
	d.super.nodeCache[info.Inode] = child
	d.super.fslock.Unlock()

	elapsed := time.Since(start)
	span.Debugf("TRACE Mknod: parent(%v) req(%v) ino(%v) (%v)ns", d.info.Inode, req, info.Inode, elapsed.Nanoseconds())
	return child, nil
}

// Symlink handles the symlink request.
func (d *Dir) Symlink(ctx context.Context, req *fuse.SymlinkRequest) (fs.Node, error) {
	ctx = ctxOperation(ctx, "DirSymlink")
	span := getSpan(ctx)
	parentIno := d.info.Inode
	start := time.Now()

	bgTime := stat.BeginStat()
	var err error
	metric := exporter.NewTPCnt("symlink")
	defer func() {
		stat.EndStat("Symlink", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
	}()
	fullPath := path.Join(d.getCwd(ctx), req.NewName)
	info, err := d.super.mw.Create_ll(ctx, parentIno, req.NewName, proto.Mode(os.ModeSymlink|os.ModePerm), req.Uid, req.Gid, []byte(req.Target), fullPath)
	if err != nil {
		span.Errorf("Symlink: parent(%v) NewName(%v) err(%v)", parentIno, req.NewName, err)
		return nil, ParseError(err)
	}

	d.super.ic.Put(info)
	child := NewFile(d.super, info, DefaultFlag, d.info.Inode, req.NewName)

	d.super.fslock.Lock()
	d.super.nodeCache[info.Inode] = child
	d.super.fslock.Unlock()

	elapsed := time.Since(start)
	span.Debugf("TRACE Symlink: parent(%v) req(%v) ino(%v) (%v)ns", parentIno, req, info.Inode, elapsed.Nanoseconds())
	return child, nil
}

// Link handles the link request.
func (d *Dir) Link(ctx context.Context, req *fuse.LinkRequest, old fs.Node) (fs.Node, error) {
	ctx = ctxOperation(ctx, "DirLink")
	span := getSpan(ctx)
	var oldInode *proto.InodeInfo
	switch old := old.(type) {
	case *File:
		oldInode = old.info
	default:
		return nil, fuse.EPERM
	}

	if !proto.IsRegular(oldInode.Mode) {
		span.Errorf("Link: not regular, parent(%v) name(%v) ino(%v) mode(%v)", d.info.Inode, req.NewName, oldInode.Inode, proto.OsMode(oldInode.Mode))
		return nil, fuse.EPERM
	}

	start := time.Now()

	bgTime := stat.BeginStat()
	var err error
	metric := exporter.NewTPCnt("link")
	defer func() {
		stat.EndStat("Link", err, bgTime, 1)
		metric.SetWithLabels(err, map[string]string{exporter.Vol: d.super.volname})
	}()
	fullPath := path.Join(d.getCwd(ctx), req.NewName)
	info, err := d.super.mw.Link(ctx, d.info.Inode, req.NewName, oldInode.Inode, fullPath)
	if err != nil {
		span.Errorf("Link: parent(%v) name(%v) ino(%v) err(%v)", d.info.Inode, req.NewName, oldInode.Inode, err)
		return nil, ParseError(err)
	}

	d.super.ic.Put(info)

	d.super.fslock.Lock()
	newFile, ok := d.super.nodeCache[info.Inode]
	if !ok {
		newFile = NewFile(d.super, info, DefaultFlag, d.info.Inode, req.NewName)
		d.super.nodeCache[info.Inode] = newFile
	}
	d.super.fslock.Unlock()

	elapsed := time.Since(start)
	span.Debugf("TRACE Link: parent(%v) name(%v) ino(%v) (%v)ns", d.info.Inode, req.NewName, info.Inode, elapsed.Nanoseconds())
	return newFile, nil
}

// Getxattr has not been implemented yet.
func (d *Dir) Getxattr(ctx context.Context, req *fuse.GetxattrRequest, resp *fuse.GetxattrResponse) error {
	if !d.super.enableXattr {
		return fuse.ENOSYS
	}
	ctx = ctxOperation(ctx, "DirGetxattr")
	span := getSpan(ctx)
	ino := d.info.Inode
	name := req.Name
	size := req.Size
	pos := req.Position

	var value []byte
	var info *proto.XAttrInfo
	var err error

	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Getxattr", err, bgTime, 1)
	}()

	if name == meta.SummaryKey {
		if !d.super.mw.EnableSummary {
			return fuse.ENOSYS
		}
		var summaryInfo meta.SummaryInfo
		cacheSummaryInfo := d.super.sc.Get(ino)
		if cacheSummaryInfo != nil {
			summaryInfo = *cacheSummaryInfo
		} else {
			summaryInfo, err = d.super.mw.GetSummary_ll(ctx, ino, 20)
			if err != nil {
				span.Errorf("GetXattr: ino(%v) name(%v) err(%v)", ino, name, err)
				return ParseError(err)
			}
			d.super.sc.Put(ino, &summaryInfo)
		}

		files := summaryInfo.Files
		subdirs := summaryInfo.Subdirs
		fbytes := summaryInfo.Fbytes
		summaryStr := "Files:" + strconv.FormatInt(int64(files), 10) + "," +
			"Dirs:" + strconv.FormatInt(int64(subdirs), 10) + "," +
			"Bytes:" + strconv.FormatInt(int64(fbytes), 10)
		value = []byte(summaryStr)

	} else {
		info, err = d.super.mw.XAttrGet_ll(ctx, ino, name)
		if err != nil {
			span.Errorf("GetXattr: ino(%v) name(%v) err(%v)", ino, name, err)
			return ParseError(err)
		}
		value = info.Get(name)
	}

	if pos > 0 {
		value = value[pos:]
	}
	if size > 0 && size < uint32(len(value)) {
		value = value[:size]
	}
	resp.Xattr = value
	span.Debugf("TRACE GetXattr: ino(%v) name(%v)", ino, name)
	return nil
}

// Listxattr has not been implemented yet.
func (d *Dir) Listxattr(ctx context.Context, req *fuse.ListxattrRequest, resp *fuse.ListxattrResponse) error {
	if !d.super.enableXattr {
		return fuse.ENOSYS
	}
	ctx = ctxOperation(ctx, "DirListxattr")
	span := getSpan(ctx)

	var err error
	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Getxattr", err, bgTime, 1)
	}()

	ino := d.info.Inode
	_ = req.Size     // ignore currently
	_ = req.Position // ignore currently

	keys, err := d.super.mw.XAttrsList_ll(ctx, ino)
	if err != nil {
		span.Errorf("ListXattr: ino(%v) err(%v)", ino, err)
		return ParseError(err)
	}
	for _, key := range keys {
		resp.Append(key)
	}
	span.Debugf("TRACE Listxattr: ino(%v)", ino)
	return nil
}

// Setxattr has not been implemented yet.
func (d *Dir) Setxattr(ctx context.Context, req *fuse.SetxattrRequest) error {
	if !d.super.enableXattr {
		return fuse.ENOSYS
	}
	ctx = ctxOperation(ctx, "DirSetxattr")
	span := getSpan(ctx)

	var err error
	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Setxattr", err, bgTime, 1)
	}()

	ino := d.info.Inode
	name := req.Name
	value := req.Xattr
	if name == meta.SummaryKey {
		span.Errorf("Set 'DirStat' is not supported.")
		return fuse.ENOSYS
	}
	// TODO： implement flag to improve compatible (Mofei Zhang)
	if err = d.super.mw.XAttrSet_ll(ctx, ino, []byte(name), []byte(value)); err != nil {
		span.Errorf("Setxattr: ino(%v) name(%v) err(%v)", ino, name, err)
		return ParseError(err)
	}
	span.Debugf("TRACE Setxattr: ino(%v) name(%v)", ino, name)
	return nil
}

// Removexattr has not been implemented yet.
func (d *Dir) Removexattr(ctx context.Context, req *fuse.RemovexattrRequest) error {
	if !d.super.enableXattr {
		return fuse.ENOSYS
	}
	ctx = ctxOperation(ctx, "DirRemovexattr")
	span := getSpan(ctx)

	var err error
	bgTime := stat.BeginStat()
	defer func() {
		stat.EndStat("Removexattr", err, bgTime, 1)
	}()

	ino := d.info.Inode
	name := req.Name
	if name == meta.SummaryKey {
		span.Errorf("Remove 'DirStat' is not supported.")
		return fuse.ENOSYS
	}
	if err = d.super.mw.XAttrDel_ll(ctx, ino, name); err != nil {
		span.Errorf("Removexattr: ino(%v) name(%v) err(%v)", ino, name, err)
		return ParseError(err)
	}
	span.Debugf("TRACE RemoveXattr: ino(%v) name(%v)", ino, name)
	return nil
}

func (d *Dir) getCwd(ctx context.Context) string {
	dirPath := ""
	if d.info.Inode == d.super.rootIno {
		return "/"
	}
	curIno := d.info.Inode
	for curIno != d.super.rootIno {
		d.super.fslock.Lock()
		node, ok := d.super.nodeCache[curIno]
		d.super.fslock.Unlock()
		if !ok {
			getSpan(ctx).Errorf("Get node cache failed: ino(%v)", curIno)
			return "unknown" + dirPath
		}
		curDir, ok := node.(*Dir)
		if !ok {
			getSpan(ctx).Errorf("Type error: Can not convert node -> *Dir, ino(%v)", curDir.parentIno)
			return "unknown" + dirPath
		}
		dirPath = "/" + curDir.name + dirPath
		curIno = curDir.parentIno
	}
	return dirPath
}

func (d *Dir) needDentrycache(ctx context.Context) bool {
	return !DisableMetaCache && d.super.bcacheDir != "" && strings.HasPrefix(d.getCwd(ctx), d.super.bcacheDir)
}

func dentryExpired(info *proto.DentryInfo) bool {
	return time.Now().UnixNano() > info.Expiration()
}

func dentrySetExpiration(info *proto.DentryInfo, t time.Duration) {
	info.SetExpiration(time.Now().Add(t).UnixNano())
}

func (d *Dir) canRenameByQuota(ctx context.Context, dstDir *Dir, srcName string) bool {
	fullPaths := d.super.mw.GetQuotaFullPaths()
	if len(fullPaths) == 0 {
		return true
	}
	var srcPath string
	if d.getCwd(ctx) == "/" {
		srcPath = "/" + srcName
	} else {
		srcPath = d.getCwd(ctx) + "/" + srcName
	}

	span := getSpan(ctx)
	for _, fullPath := range fullPaths {
		span.Debugf("srcPath [%v] fullPath[%v].", srcPath, fullPath)
		if proto.IsAncestor(srcPath, fullPath) {
			return false
		}
	}
	return true
}

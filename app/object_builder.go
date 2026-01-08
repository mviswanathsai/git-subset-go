package main

import (
	"bufio"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strconv"

	"github.com/codecrafters-io/git-starter-go/internal/git"
)

type ObjectBuilder struct {
	packIndex   map[uint64]PackNode
	lookupCache map[uint64]*ResolvedObject
	hashMap     map[string]*ResolvedObject
	packOrder   []uint64
	file        *os.File
	fileInfo    os.FileInfo
	br          *bufio.Reader
	zr          io.ReadCloser
	h           hash.Hash
	lp          *LeveledPool
}

func (builder *ObjectBuilder) ForEachObject(fn func(res *ResolvedObject) error) error {
	for _, offset := range builder.packOrder {
		node := builder.packIndex[offset]
		result := builder.buildObject(node)
		if err := fn(result); err != nil {
			return err
		}
	}
	return nil
}

func (builder *ObjectBuilder) ClearCache() {
	builder.lookupCache = nil
}

func (builder *ObjectBuilder) ForEachObjectResult(fn func(res *ObjectResult) error) error {
	for i, offset := range builder.packOrder {
		node := builder.packIndex[offset]

		var nxtOfs uint64
		if i < len(builder.packOrder)-1 {
			nxtOfs = builder.packOrder[i+1]
		} else {
			nxtOfs = uint64(builder.fileInfo.Size() - 20)
		}

		result := builder.resolveObject(node, offset, nxtOfs)

		if err := fn(result); err != nil {
			return err
		}
	}
	return nil
}

func (builder *ObjectBuilder) Index() map[uint64]PackNode {
	return builder.packIndex
}

func (builder *ObjectBuilder) resolveObject(n PackNode, currHeaderOfs, nxtHeaderOfs uint64) *ObjectResult {
	resolvedObject := builder.buildObject(n)
	return &ObjectResult{
		SHA1:       resolvedObject.SHA1,
		Type:       resolvedObject.Type,
		Size:       n.ObjectSize(),
		PackSize:   nxtHeaderOfs - currHeaderOfs,
		Offset:     currHeaderOfs,
		ParentSHA1: resolvedObject.ParentSHA1,
		Depth:      resolvedObject.Depth,
	}
}

func (builder *ObjectBuilder) resolveDelta(n *DeltaNode) *ResolvedObject {
	parentResolvedObject := builder.buildObject(builder.Index()[n.ParentOffset()])
	depth := parentResolvedObject.Depth + 1
	baseType := parentResolvedObject.Type
	dstBuf := builder.lp.Get(int(n.dstBufSize))
	defer builder.lp.Put(dstBuf)
	applyDeltaInto(n, parentResolvedObject.Data, *dstBuf)
	data := make([]byte, n.dstBufSize)
	copy(data, *dstBuf)
	hash := builder.ReturnObjectSHA(data, int64(len(data)), baseType)
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: parentResolvedObject.SHA1,
		Type:       baseType,
		Data:       data,
	}
	return res
}

func (builder *ObjectBuilder) ReturnObjectSHA(data []byte, size int64, objType uint8) string {
	if builder.h == nil {
		builder.h = sha1.New()
	} else {
		builder.h.Reset()
	}
	builder.h.Write(git.TypeToBytes(objType))
	builder.h.Write([]byte(" "))
	builder.h.Write([]byte(strconv.FormatInt(int64(len(data)), 10)))
	builder.h.Write([]byte{0})
	builder.h.Write(data)

	sha := hex.EncodeToString(builder.h.Sum(nil))
	return sha
}

func (builder *ObjectBuilder) resolveBase(n *ObjectNode) *ResolvedObject {
	buf := builder.lp.Get(int(n.objSize))
	defer builder.lp.Put(buf)
	builder.readObjectDataInto(n, *buf)
	data := make([]byte, n.objSize)
	copy(data, *buf)
	hash := builder.ReturnObjectSHA(data, int64(n.objSize), n.objType)
	objType := n.Type()
	depth := 0
	res := &ResolvedObject{
		SHA1:       hash,
		Depth:      depth,
		ParentSHA1: "",
		Type:       objType,
		Data:       data,
	}
	return res
}

func (builder *ObjectBuilder) buildObject(n PackNode) *ResolvedObject {
	if cached, ok := builder.lookupCache[n.Offset()]; ok {
		return cached
	}

	var res *ResolvedObject

	switch Type := n.Type(); Type {
	case 6:
		res = builder.resolveDelta(n.(*DeltaNode))
	default:
		res = builder.resolveBase(n.(*ObjectNode))
	}
	builder.lookupCache[n.Offset()] = res

	return res
}

func (builder *ObjectBuilder) readObjectDataInto(n *ObjectNode, buf []byte) {
	f := builder.file
	br := builder.br
	f.Seek(int64(n.dataOffset), 0)
	br.Reset(f)
	if builder.zr == nil {
		builder.zr, _ = zlib.NewReader(builder.br)
	} else {
		if reseter, ok := builder.zr.(zlib.Resetter); ok {
			reseter.Reset(builder.br, nil)
		}
	}
	io.ReadFull(builder.zr, buf)
}

type ObjectResult struct {
	SHA1       string
	Type       uint8
	Size       uint64 // The uncompressed size of the object payload
	PackSize   uint64
	Offset     uint64
	Depth      int
	ParentSHA1 string
}

func (res *ObjectResult) TypeName() []byte {
	return git.TypeToBytes(res.Type)
}

type ResolvedObject struct {
	SHA1       string
	Type       uint8
	Depth      int
	ParentSHA1 string
	Data       []byte
}

func applyDeltaInto(d *DeltaNode, srcBuf []byte, dstBuf []byte) {
	if len(srcBuf) != int(d.srcBufSize) {
		fmt.Fprintf(os.Stderr, "Unexepected src buffer size")
		os.Exit(1)
	}
	var cursor uint64
	for _, op := range d.ops {
		if op.kind() == 1 {
			copyOp, ok := op.(CopyOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			// Copy from the given offset into the dst buffer
			copy(dstBuf[cursor:], srcBuf[copyOp.Offset:copyOp.Offset+copyOp.Size])
			cursor += copyOp.Size
		} else {
			insertOp, ok := op.(InsertOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			copy(dstBuf[cursor:], insertOp.Payload)
			cursor += uint64(len(insertOp.Payload))
		}

	}
}

func applyDelta(d *DeltaNode, srcBuf []byte) []byte {
	if len(srcBuf) != int(d.srcBufSize) {
		fmt.Fprintf(os.Stderr, "Unexepected src buffer size")
		os.Exit(1)
	}
	dstBuf := make([]byte, d.dstBufSize)
	var cursor uint64
	for _, op := range d.ops {
		if op.kind() == 1 {
			copyOp, ok := op.(CopyOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			// Copy from the given offset into the dst buffer
			copy(dstBuf[cursor:], srcBuf[copyOp.Offset:copyOp.Offset+copyOp.Size])
			cursor += copyOp.Size
		} else {
			insertOp, ok := op.(InsertOp)
			if !ok {
				fmt.Fprintf(os.Stderr, "Something is very wrong")
				os.Exit(1)
			}
			copy(dstBuf[cursor:], insertOp.Payload)
			cursor += uint64(len(insertOp.Payload))
		}

	}
	return dstBuf
}

type ObjectStats struct {
	NonDeltaCount int
	ChainCounts   map[int]int
}

func (s *ObjectStats) ProcessResult(res *ObjectResult) error {
	printResult(res) // Print as we go
	s.Observe(res)   // Update counters
	return nil
}

func (s *ObjectStats) Observe(res *ObjectResult) {
	if res.Depth == 0 {
		s.NonDeltaCount++
	} else {
		s.ChainCounts[res.Depth]++
	}
}

func (s *ObjectStats) PrintSummary() {
	fmt.Printf("non delta: %d objects\n", s.NonDeltaCount)

	depths := make([]int, 0, len(s.ChainCounts))
	for d := range s.ChainCounts {
		depths = append(depths, d)
	}
	sort.Ints(depths)

	for _, d := range depths {
		count := s.ChainCounts[d]
		fmt.Printf("chain length = %d: %d objects\n", d, count)
	}
}

func printResult(res *ObjectResult) {
	if res.Depth > 0 {
		// Format for deltas: SHA1 TYPE SIZE PACKSIZE OFFSET DEPTH PARENT_SHA1
		fmt.Printf("%-10s %-7s %7d %7d %7d %d %s\n",
			res.SHA1,
			git.TypeToBytes(res.Type),
			res.Size,
			res.PackSize,
			res.Offset,
			res.Depth,
			res.ParentSHA1,
		)
	} else {
		// Format for non-deltas: SHA1 TYPE SIZE PACKSIZE OFFSET
		fmt.Printf("%-10s %-7s %7d %7d %7d\n",
			res.SHA1,
			git.TypeToBytes(res.Type),
			res.Size,
			res.PackSize,
			res.Offset,
		)
	}
}

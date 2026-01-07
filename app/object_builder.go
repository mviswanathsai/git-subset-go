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
)

type objectBuilder struct {
	packIndex   map[uint64]PackNode
	lookupCache map[uint64]*ResolvedObject
	hashMap     map[string]*ResolvedObject
	packOrder   []uint64
	file        *os.File
	fileInfo    os.FileInfo
	br          *bufio.Reader
	zr          io.ReadCloser
	hasher      hash.Hash
	packLength  uint32
}

// TODO: add verification. This can only be run when some info about the pack is already known.
func (builder *objectBuilder) ForEachObject(fn func(res *ResolvedObject) error) error {
	for _, offset := range builder.packOrder {
		node := builder.packIndex[offset]
		result := builder.buildObject(node)
		if err := fn(result); err != nil {
			return err
		}
	}
	return nil
}

func (builder *objectBuilder) ClearCache() {
	builder.lookupCache = nil
}

func (builder *objectBuilder) ForEachObjectResult(fn func(res *ObjectResult) error) error {
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

func (builder *objectBuilder) Index() map[uint64]PackNode {
	return builder.packIndex
}

func (builder *objectBuilder) resolveObject(n PackNode, currHeaderOfs, nxtHeaderOfs uint64) *ObjectResult {
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

func (builder *objectBuilder) resolveDelta(n *DeltaNode) *ResolvedObject {
	parentResolvedObject := builder.buildObject(builder.Index()[n.ParentOffset()])
	depth := parentResolvedObject.Depth + 1
	baseType := parentResolvedObject.Type
	data := applyDelta(n, parentResolvedObject.Data)
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

func (builder *objectBuilder) ReturnObjectSHA(data []byte, size int64, objType uint8) string {
	if builder.hasher == nil {
		builder.hasher = sha1.New()
	} else {
		builder.hasher.Reset()
	}
	sha := sha1.New()
	sha.Write(TypeToBytes(objType))
	sha.Write([]byte(" "))
	sha.Write([]byte(strconv.FormatInt(int64(len(data)), 10)))
	sha.Write([]byte{0})
	sha.Write(data)

	h := hex.EncodeToString(sha.Sum(nil))
	return h
}

func (builder *objectBuilder) resolveBase(n *ObjectNode) *ResolvedObject {
	data := builder.readObjectData(n)
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

func (builder *objectBuilder) buildObject(n PackNode) *ResolvedObject {
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

func (builder *objectBuilder) readObjectData(n *ObjectNode) []byte {
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
	buf := make([]byte, n.ObjectSize())
	io.ReadFull(builder.zr, buf)
	return buf
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
	return TypeToBytes(res.Type)
}

type ResolvedObject struct {
	SHA1       string
	Type       uint8
	Depth      int
	ParentSHA1 string
	Data       []byte
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

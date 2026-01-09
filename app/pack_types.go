package main

import "github.com/codecrafters-io/git-starter-go/internal/git"

const (
	OP_CODE_COPY   = 1
	OP_CODE_INSERT = 0
)

type PackNode interface {
	Type() uint8
	ParentOffset() uint64
	ObjectSize() uint64
	Offset() uint64
}

type ObjectNode struct {
	objSize    uint64
	headerOfs  uint64
	dataOffset uint64 // The offset from the headerOfs to find the data, can be a maximum of 64 bits -> 8 bytes -> can be stored in uint8
	objType    uint8
}

func (n *ObjectNode) Type() uint8 {
	return n.objType
}

func (n *ObjectNode) ParentOffset() uint64 {
	return 0
}

func (n *ObjectNode) Offset() uint64 {
	return n.headerOfs
}

func (n *ObjectNode) ObjectSize() uint64 {
	return n.objSize
}

type DeltaNode struct {
	srcBufSize uint64
	dstBufSize uint64
	parentOfs  uint64
	objSize    uint64
	headerOfs  uint64
	ops        []DeltaOps
}

func (n *DeltaNode) Type() uint8 {
	return git.OBJ_OFS_DELTA
}

func (n *DeltaNode) ParentOffset() uint64 {
	return n.parentOfs
}

func (n *DeltaNode) ObjectSize() uint64 {
	return n.objSize
}

func (n *DeltaNode) Offset() uint64 {
	return n.headerOfs
}

type DeltaOps interface {
	kind() byte
}

type CopyOp struct {
	Offset uint64
	Size   uint64
}

func (CopyOp) kind() byte {
	return OP_CODE_COPY
}

type InsertOp struct {
	Payload     []byte
	PayloadSize uint8
}

func (InsertOp) kind() byte {
	return OP_CODE_INSERT
}

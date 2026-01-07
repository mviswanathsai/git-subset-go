package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/codecrafters-io/git-starter-go/internal/git"
)

type PackStreamer struct {
	f     *os.File
	fInfo os.FileInfo
	h     hash.Hash
	br    *bufio.Reader
	zr    io.ReadCloser
	zbr   *bufio.Reader
}

func (streamer *PackStreamer) getZlibReader() *bufio.Reader {
	if streamer.zr == nil {
		streamer.zr, _ = zlib.NewReader(streamer.br)
		streamer.zbr = bufio.NewReader(streamer.zr)
	} else {
		streamer.zr.(zlib.Resetter).Reset(streamer.br, nil)
		streamer.zbr.Reset(streamer.zr)
	}
	return streamer.zbr
}

func (streamer *PackStreamer) ReadPackAndBuildIndex() (packOrder []uint64, packIndex map[uint64]PackNode, err error) {
	_, objCount, err := streamer.ReadPackHeader()
	if err != nil {
		return nil, nil, fmt.Errorf("Pack header invalid: %v\n", err)
	}

	packOrder, packIndex, err = streamer.BuildPackIndex(objCount)
	if err != nil {
		return nil, nil, fmt.Errorf("Error building pack index: %v\n", err)
	}
	return packOrder, packIndex, err
}

func (streamer *PackStreamer) ReadPackHeader() (version, objectCount uint32, err error) {
	streamer.br.Reset(streamer.f)
	buf := make([]byte, 12)

	io.ReadFull(streamer.br, buf)

	if string(buf[:4]) != "PACK" {
		return 0, 0, fmt.Errorf("not a valid pack file")
	}
	// Read the version and the nobjects
	version = binary.BigEndian.Uint32(buf[4:8])
	objectCount = binary.BigEndian.Uint32(buf[8:12])
	return version, objectCount, nil
}

func (streamer *PackStreamer) VerifyPackTrailer() ([]byte, error) {
	defer streamer.h.Reset()
	defer streamer.f.Seek(0, io.SeekStart)

	if streamer.fInfo.Size() < 32 {
		return nil, fmt.Errorf("packfile is too small to be valid")
	}

	expected := make([]byte, 20)
	_, err := streamer.f.ReadAt(expected, streamer.fInfo.Size()-20)
	if err != nil {
		return nil, fmt.Errorf("failed to read expected trailer: %w", err)
	}

	if _, err := streamer.f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek file: %w", err)
	}

	lr := io.LimitReader(streamer.f, streamer.fInfo.Size()-20)

	if _, err := io.Copy(streamer.h, lr); err != nil {
		return nil, fmt.Errorf("failed to hash pack content: %w", err)
	}

	actual := streamer.h.Sum(nil)
	if !bytes.Equal(expected, actual) {
		return nil, fmt.Errorf("invalid packfile: checksum doesn't match")
	}
	return actual, nil
}

func (streamer *PackStreamer) BuildPackIndex(objectCount uint32) ([]uint64, map[uint64]PackNode, error) {
	packOrder := make([]uint64, 0, objectCount)
	packIndex := make(map[uint64]PackNode)
	for i := 1; uint32(i) <= objectCount; i++ {
		headerOfs, err := currentOffset(streamer.f, streamer.br)
		if err != nil {
			return nil, nil, fmt.Errorf("Error getting reader's current cursor position: %w", err)
		}
		objType, objSize, err := readObjHeader(streamer.br)
		if err != nil {
			return nil, nil, fmt.Errorf("Error reading object header: %w", err)
		}
		packOrder = append(packOrder, headerOfs)

		var parentOfs uint64
		if objType == 6 {
			// The required negative offet brom the type byte
			negOfs, err := readDeltaNegOfs(streamer.br)
			if err != nil {
				return nil, nil, fmt.Errorf("Error reading delta object's negative offset: %w", err)
			}
			parentOfs = uint64(headerOfs) - negOfs
		}

		dataOfs, err := currentOffset(streamer.f, streamer.br)
		if err != nil {
			return nil, nil, fmt.Errorf("Error getting reader's current cursor position: %w", err)
		}

		if objType == 6 {
			srcBufSize, dstBufSize, ops, err := streamer.parseDeltaObj()
			if err != nil {
				return nil, nil, fmt.Errorf("Error parsing delta object: %w", err)
			}
			packIndex[headerOfs] = &DeltaNode{
				srcBufSize: srcBufSize,
				dstBufSize: dstBufSize,
				parentOfs:  parentOfs,
				ops:        ops,
				objSize:    objSize,
				headerOfs:  headerOfs,
			}
		} else {
			if _, err := io.Copy(io.Discard, streamer.getZlibReader()); err != nil {
				return nil, nil, err
			}
			packIndex[headerOfs] = &ObjectNode{
				objType:    objType,
				objSize:    objSize,
				headerOfs:  headerOfs,
				dataOffset: dataOfs,
			}
		}
	}
	return packOrder, packIndex, nil
}

func (streamer *PackStreamer) parseDeltaObj() (srcBufSize, dstBufSize uint64, ops []DeltaOps, err error) {
	zbr := streamer.getZlibReader()
	srcSize, dstSize := readDeltaHeader(zbr)
	for {
		b, err := zbr.ReadByte()
		if err != nil && errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return 0, 0, nil, err
		}
		if b&0x80 != 0 {
			copyOfsFlags := (b & git.CopyOffsetFlagsMask)
			copySizeFlags := (b & git.CopySizeFlagsMask) >> git.CopySizeFlagsShift
			ofs, err := readDeltaCopyOffset(copyOfsFlags, zbr)
			if err != nil {
				return 0, 0, nil, err
			}
			size, err := readDeltaCopySize(copySizeFlags, zbr)
			if err != nil {
				return 0, 0, nil, err
			}
			ops = append(ops, CopyOp{Offset: ofs, Size: size})
		} else {
			payloadSize := (b & git.InsertSizeMask)
			insertPayloadBuf := make([]byte, payloadSize)
			if _, err := io.ReadFull(zbr, insertPayloadBuf); err != nil {
				return 0, 0, nil, err
			}
			ops = append(ops, InsertOp{PayloadSize: payloadSize, Payload: insertPayloadBuf})
		}
	}
	return srcSize, dstSize, ops, nil
}

func currentOffset(f *os.File, br *bufio.Reader) (uint64, error) {
	fofs, err := f.Seek(0, 1)
	if err != nil {
		return 0, err
	}
	currOfs := fofs - int64(br.Buffered())

	return uint64(currOfs), nil
}

func readObjHeader(br *bufio.Reader) (byte, uint64, error) {
	var i int
	var objSize uint64
	var objType byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		if i == 0 {
			objSize = uint64(b & 0b00001111)
			objType = (b & 0b01110000) >> 4
		} else {
			objSize = uint64(b&0b01111111)<<(4+(i-1)*7) | uint64(objSize)
		}
		if b&0b10000000>>7 == 0 {
			break
		}
		i++
	}
	return objType, objSize, nil
}

// There are two sizes to read
func readDeltaHeader(r *bufio.Reader) (srcSize uint64, dstSize uint64) {
	srcSize = readDeltaSize(r)
	dstSize = readDeltaSize(r)
	return srcSize, dstSize
}

func readDeltaSize(r *bufio.Reader) uint64 {
	var i uint64
	var size uint64
	i++
	for {
		b, _ := r.ReadByte()
		size = uint64(b&0b01111111)<<((i-1)*7) | size
		if b&0x80 == 0 {
			break
		}
		i++
	}
	return size
}

func readDeltaCopySize(sizeFlags byte, br *bufio.Reader) (size uint64, err error) {
	for i := range git.CopySizeFlagsLen {
		if (0b00000001 & (sizeFlags >> i)) == 1 {
			b, err := br.ReadByte()
			if err != nil {
				return 0, err
			}
			size |= uint64(b) << (8 * i)
		}
	}
	if size == 0 {
		return git.CopySizeZero, nil
	}
	return size, nil
}

func readDeltaNegOfs(br *bufio.Reader) (uint64, error) {
	var i uint64
	var size uint64

	for {
		if i != 0 {
			// The +1 rule
			size++
		}
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		size = size<<7 | uint64(b&0b01111111)
		if b&0x80 == 0 {
			break
		}
		i++
	}
	return size, nil
}

func readDeltaCopyOffset(ofsFlags byte, br *bufio.Reader) (ofs uint64, err error) {
	for i := range git.CopyOffsetFlagsLen {
		if (0b00000001 & (ofsFlags >> i)) == 1 {
			b, err := br.ReadByte()
			if err != nil {
				return 0, err
			}
			ofs |= uint64(b) << (8 * i)
		}
	}
	return ofs, nil
}

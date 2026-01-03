package git

import "io/fs"

const (
	GitObjDir           = ".git/objects"
	ObjHeaderDelim      = 0
	GitExModeOct        = fs.FileMode(0111)
	GitDirMode          = "40000"
	GitRegMode          = "100644"
	GitExMode           = "100755"
	OBJ_COMMIT          = 1
	OBJ_TREE            = 2
	OBJ_BLOB            = 3
	OBJ_TAG             = 4
	OBJ_OFS_DELTA       = 6
	OBJ_REF_DELTA       = 7
	CopyOffsetFlagsMask = 0b00001111
	CopySizeFlagsMask   = 0b01110000
	CopySizeFlagsLen    = 3
	CopyOffsetFlagsLen  = 4
	CopySizeFlagsShift  = 4
	CopySizeZero        = 0x10000
	InsertSizeMask      = 0b01111111
)

var (
	GIT_COMMIT    = []byte("commit")
	GIT_TREE      = []byte("tree")
	GIT_BLOB      = []byte("blob")
	GIT_TAG       = []byte("tag")
	GIT_OFS_DELTA = []byte("ofs_delta")
	GIT_REF_DELTA = []byte("ref_delta")
	C_CAPS        = map[string]struct{}{
		"multi_ack_detailed": {},
		"side-band-64k":      {},
		"ofs-delta":          {},
	}
)

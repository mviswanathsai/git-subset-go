package git

func TypeToBytes(input uint8) []byte {
	switch input {
	case OBJ_COMMIT:
		return GIT_COMMIT
	case OBJ_TREE:
		return GIT_TREE
	case OBJ_BLOB:
		return GIT_BLOB
	case OBJ_TAG:
		return GIT_TAG
	case OBJ_OFS_DELTA:
		return GIT_OFS_DELTA
	case OBJ_REF_DELTA:
		return GIT_REF_DELTA
	default:
		return nil
	}
}

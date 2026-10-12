package httpclient

import "sync"

const SSEScannerBuf64KSize = 64 * 1024

var SSEScannerBuf64KPool = sync.Pool{
	New: func() any {
		return new(SSEScannerBuf64K)
	},
}

type SSEScannerBuf64K [SSEScannerBuf64KSize]byte

// GetSSEScannerBuf64K borrows a buffer whose contents may come from an earlier use.
// Return it with PutSSEScannerBuf64K after the scanner has finished using it.
func GetSSEScannerBuf64K() *SSEScannerBuf64K {
	v := SSEScannerBuf64KPool.Get()
	buf, ok := v.(*SSEScannerBuf64K)
	if !ok || buf == nil {
		return new(SSEScannerBuf64K)
	}
	return buf
}

// PutSSEScannerBuf64K returns a borrowed buffer. The caller must stop using it
// and return it at most once; nil is ignored.
func PutSSEScannerBuf64K(buf *SSEScannerBuf64K) {
	if buf == nil {
		return
	}
	SSEScannerBuf64KPool.Put(buf)
}

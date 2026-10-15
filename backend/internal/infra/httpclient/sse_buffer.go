package httpclient

import "sync"

// sse_buffer.go supplies reusable initial buffers for SSE line scanners.
// Streaming parsers repeatedly need an initial byte buffer while reading SSE
// lines. A shared pool reuses fixed 64 KiB storage; borrowers retain that
// storage until their scanner finishes, then return it with its previous
// contents still present.

// SSEScannerBuf64KSize sets the initial scanner storage capacity used by SSE
// consumers; each scanner separately chooses its maximum accepted token size.
const SSEScannerBuf64KSize = 64 * 1024

// SSEScannerBuf64KPool reuses fixed scanner storage; borrowers return it after
// their scanner stops accessing the bytes and leave contents uncleared.
var SSEScannerBuf64KPool = sync.Pool{
	New: func() any {
		return new(SSEScannerBuf64K)
	},
}

// SSEScannerBuf64K is fixed byte storage borrowed by streaming SSE scanners.
type SSEScannerBuf64K [SSEScannerBuf64KSize]byte

// GetSSEScannerBuf64K borrows fixed storage whose bytes may come from an
// earlier use. Return it after the scanner finishes, then stop accessing it;
// an unusable pool value produces fresh storage.
func GetSSEScannerBuf64K() *SSEScannerBuf64K {
	v := SSEScannerBuf64KPool.Get()
	buf, ok := v.(*SSEScannerBuf64K)
	if !ok || buf == nil {
		// Pool values are externally accessible. Replace an unusable entry with
		// valid fixed storage so scanner callers still receive a usable buffer.
		return new(SSEScannerBuf64K)
	}
	return buf
}

// PutSSEScannerBuf64K returns buf for another scanner to borrow, ignoring
// nil. Callers return each borrowed buffer at most once and stop accessing
// its bytes before this call makes the storage reusable.
func PutSSEScannerBuf64K(buf *SSEScannerBuf64K) {
	if buf == nil {
		return
	}
	SSEScannerBuf64KPool.Put(buf)
}

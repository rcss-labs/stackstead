package updatehost

import (
	"bytes"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"strconv"
	"strings"
)

// Validate record types before archive/tar can hide GNU/PAX extension records.
// Ordinary GNU headers emitted by pinned nfpm remain supported; every byte of
// headers, file data, alignment and termination traverses the outer budget.
type payloadTarRecords struct {
	source    io.Reader
	header    []byte
	remaining int64
	ended     bool
}

func (r *payloadTarRecords) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if len(r.header) > 0 {
		n := copy(b, r.header)
		r.header = r.header[n:]
		return n, nil
	}
	if r.remaining > 0 {
		if int64(len(b)) > r.remaining {
			b = b[:r.remaining]
		}
		n, e := r.source.Read(b)
		r.remaining -= int64(n)
		if e == io.EOF && r.remaining > 0 {
			e = io.ErrUnexpectedEOF
		}
		return n, e
	}
	var block [512]byte
	n, e := io.ReadFull(r.source, block[:])
	if e != nil {
		if e == io.EOF && n == 0 {
			return 0, io.EOF
		}
		return 0, e
	}
	zero := bytes.Equal(block[:], make([]byte, 512))
	if zero {
		r.ended = true
	} else {
		if r.ended {
			return 0, update.ErrIncompatible
		}
		typ := block[156]
		if typ != 0 && typ != '0' && typ != '5' {
			return 0, update.ErrIncompatible
		}
		sizeField := strings.Trim(string(block[124:136]), "\x00 ")
		if sizeField == "" {
			sizeField = "0"
		}
		for _, c := range sizeField {
			if c < '0' || c > '7' {
				return 0, update.ErrIncompatible
			}
		}
		size, e := strconv.ParseInt(sizeField, 8, 64)
		if e != nil || size < 0 || size > childArtifactLimit || (typ == '5' && size != 0) {
			return 0, update.ErrIncompatible
		}
		r.remaining = (size + 511) / 512 * 512
	}
	r.header = append(r.header, block[:]...)
	return r.Read(b)
}

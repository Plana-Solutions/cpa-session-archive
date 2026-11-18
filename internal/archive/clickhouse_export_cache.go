package archive

import (
	"time"
	"unicode/utf8"
)

const exportCacheLimit = 64 << 20

type exportRecordKey struct {
	Format, Session, Source, From, To, Version string
	Offset                                     int
	Started                                    int64
}

type serializedExport struct {
	data       string
	offsets    []int
	characters int
}

func newSerializedExport(data string) *serializedExport {
	r := &serializedExport{data: data}
	for at := range data {
		if r.characters%rawChunk == 0 {
			r.offsets = append(r.offsets, at)
		}
		r.characters++
	}
	return r
}

func (r *serializedExport) byteOffset(character int) int {
	if character == r.characters {
		return len(r.data)
	}
	block := character / rawChunk
	at := r.offsets[block]
	for n := block * rawChunk; n < character; n++ {
		_, size := utf8.DecodeRuneInString(r.data[at:])
		at += size
	}
	return at
}

func (r *serializedExport) chunk(part int) (string, int, error) {
	if part > r.characters {
		return "", 0, badQuery("export cursor exceeds record")
	}
	end := min(part+rawChunk, r.characters)
	return r.data[r.byteOffset(part):r.byteOffset(end)], end, nil
}

type exportCacheEntry struct {
	key     exportRecordKey
	record  *serializedExport
	expires time.Time
}

func (c *clickhouseReader) cachedExport(key exportRecordKey) *serializedExport {
	c.exportMu.Lock()
	defer c.exportMu.Unlock()
	if c.exportCache == nil {
		return nil
	}
	now := time.Now()
	if !now.Before(c.exportCache.expires) {
		c.exportCache = nil
		return nil
	}
	if c.exportCache.key != key {
		return nil
	}
	c.exportCache.expires = now.Add(time.Minute)
	return c.exportCache.record
}

func (c *clickhouseReader) rememberExport(key exportRecordKey, r *serializedExport) {
	if len(r.data) > exportCacheLimit {
		return
	}
	c.exportMu.Lock()
	// shortcut: one record per reader; use a byte-budgeted LRU if parallel exports thrash it.
	c.exportCache = &exportCacheEntry{key: key, record: r, expires: time.Now().Add(time.Minute)}
	c.exportMu.Unlock()
}

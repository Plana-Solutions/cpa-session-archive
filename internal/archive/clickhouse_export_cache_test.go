package archive

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSerializedExportUnicodeChunks(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("ab", rawChunk+13), strings.Repeat("界🙂", rawChunk+9)} {
		r := newSerializedExport(text)
		expected := []rune(text)
		if r.characters != len(expected) {
			t.Fatal("incorrect character count")
		}
		var whole strings.Builder
		for part := 0; ; {
			chunk, end, err := r.chunk(part)
			if err != nil {
				t.Fatal(err)
			}
			whole.WriteString(chunk)
			if end == r.characters {
				break
			}
			part = end
		}
		if whole.String() != text {
			t.Fatal("export chunks changed UTF-8 bytes")
		}
		for _, part := range []int{0, 1, rawChunk - 1, rawChunk + 3, len(expected)} {
			if part > len(expected) {
				continue
			}
			got, end, err := r.chunk(part)
			if err != nil || got != string(expected[part:min(part+rawChunk, len(expected))]) || end != min(part+rawChunk, len(expected)) {
				t.Fatal("random character cursor changed content")
			}
		}
		if _, _, err := r.chunk(len(expected) + 1); err == nil {
			t.Fatal("accepted cursor beyond record")
		}
	}
}

func TestSerializedExportCacheLimitAndExpiry(t *testing.T) {
	c := &clickhouseReader{}
	key := exportRecordKey{Format: "archive", Session: "session", Source: "source", Version: "v1"}
	r := newSerializedExport("small")
	c.rememberExport(key, r)
	if got := c.cachedExport(key); got != r {
		t.Fatal("record not reused")
	}
	changed := key
	changed.Version = "v2"
	if c.cachedExport(changed) != nil {
		t.Fatal("changed source version reused stale data")
	}
	tooLarge := newSerializedExport(strings.Repeat("x", exportCacheLimit+1))
	c.rememberExport(changed, tooLarge)
	if c.cachedExport(changed) != nil {
		t.Fatal("retained data above the cache budget")
	}
	c.exportMu.Lock()
	c.exportCache.expires = time.Now().Add(-time.Second)
	c.exportMu.Unlock()
	if c.cachedExport(key) != nil {
		t.Fatal("reused expired data")
	}
}

func TestExportCacheConcurrency(t *testing.T) {
	c := &clickhouseReader{}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			key := exportRecordKey{Session: string(rune('a' + worker%2))}
			text := strings.Repeat(key.Session+"界🙂", 1000)
			r := newSerializedExport(text)
			for n := 0; n < 100; n++ {
				c.rememberExport(key, r)
				if got := c.cachedExport(key); got != nil {
					chunk, _, err := got.chunk(0)
					if err != nil || chunk != text {
						t.Error("concurrent cache returned another source's content")
						return
					}
				}
			}
		}(worker)
	}
	wg.Wait()
}

package chimport

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
)

type bodyItems struct {
	lineage uint64
	sk      bodystore.Skeleton
	blobs   map[bodystore.Hash][]byte
	ts      time.Time
}

func splitBody(row *requestRow, session string) bodyItems {
	body := []byte(row.RequestBody)
	sk, blobs := bodystore.Split(body)
	lineage := bodystore.Lineage(row.RequestHeaders, body)
	if lineage == 0 && session != "" {
		lineage = bodystore.LineageKey("archive:" + session)
	}
	ts, _ := time.Parse(timeLayout, row.StartedAt)
	// Keep the redacted inline body because the plana.9 deriver reads request_body directly.
	return bodyItems{lineage, sk, blobs, ts}
}

func (c *client) writeBodies(ctx context.Context, items []bodyItems) (int64, error) {
	known := map[bodystore.Key]bool{}
	for _, item := range items {
		if item.lineage == 0 {
			continue
		}
		sentinel := bodystore.Key{Lineage: item.lineage}
		if known[sentinel] {
			continue
		}
		refs := []string{}
		unique := map[bodystore.Hash]bool{}
		for _, other := range items {
			if other.lineage == item.lineage {
				for h := range other.blobs {
					if !unique[h] {
						refs = append(refs, "unhex('"+hex.EncodeToString(h[:])+"')")
						unique[h] = true
					}
				}
			}
		}
		if len(refs) == 0 {
			known[sentinel] = true
			continue
		}
		sort.Strings(refs)
		for start := 0; start < len(refs); start += 256 {
			end := min(start+256, len(refs))
			b, err := c.call(ctx, `SELECT hex(hash) AS hash_hex FROM cliproxy.body_blobs WHERE lineage={lineage:UInt64} AND hash IN (`+strings.Join(refs[start:end], ",")+`) FORMAT JSONEachRow`, url.Values{"param_lineage": {strconv.FormatUint(item.lineage, 10)}}, nil, false)
			if err != nil {
				return 0, fmt.Errorf("blob preload failed: %s", err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			for {
				var row struct {
					Hash string `json:"hash_hex"`
				}
				err := dec.Decode(&row)
				if err == io.EOF {
					break
				}
				if err != nil {
					return 0, err
				}
				h, err := hex.DecodeString(row.Hash)
				if err != nil || len(h) != 16 {
					return 0, bodystore.ErrIncomplete
				}
				known[bodystore.Key{Lineage: item.lineage, Hash: bodystore.Hash(h)}] = true
			}
		}
		known[sentinel] = true
	}
	var rows [][]byte
	for _, item := range items {
		hashes := make([]bodystore.Hash, 0, len(item.blobs))
		for h := range item.blobs {
			hashes = append(hashes, h)
		}
		sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
		for _, h := range hashes {
			key := bodystore.Key{Lineage: item.lineage, Hash: h}
			if !known[key] {
				rows = append(rows, bodystore.EncodeBlob(item.lineage, h, item.blobs[h], item.ts))
				known[key] = true
			}
		}
	}
	return c.insert(ctx, "body_blobs", rows)
}

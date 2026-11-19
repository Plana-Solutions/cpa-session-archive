package archive

import (
	"context"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
)

const rawChunk = 256 << 10
const maxRawBody = 64 << 20

func (c *clickhouseReader) hasBodyStorage(ctx context.Context) (bool, error) {
	var rows []struct {
		N int `json:"n"`
	}
	err := c.query(ctx, `SELECT count() AS n FROM system.columns WHERE database={database:String} AND table='requests' AND name='body_sha256' LIMIT 1`, url.Values{"database": {c.config.Database}}, &rows)
	return len(rows) > 0 && rows[0].N > 0, err
}

// Slice before base64 encoding so even large legacy rows fit the reader's result limit.
func (c *clickhouseReader) rawBytes(ctx context.Context, w readWindow, expr, from string) ([]byte, error) {
	var out []byte
	for offset := 0; ; offset += rawChunk {
		w.params.Set("body_offset", strconv.Itoa(offset+1))
		var rows []struct {
			Data []byte `json:"data_b64"`
			N    int    `json:"n"`
		}
		err := c.query(ctx, `SELECT base64Encode(substring(`+expr+`,{body_offset:UInt32},262144)) AS data_b64,length(`+expr+`) AS n`+from+` LIMIT 1 SETTINGS max_threads=1,max_block_size=1`, w.params, &rows)
		if err != nil {
			return nil, err
		}
		if len(rows) != 1 {
			return nil, bodystore.ErrIncomplete
		}
		r := rows[0]
		if r.N > maxRawBody || r.N < 0 || len(out)+len(r.Data) > r.N {
			return nil, bodystore.ErrIncomplete
		}
		out = append(out, r.Data...)
		if len(out) == r.N {
			return out, nil
		}
		if len(r.Data) != rawChunk {
			return nil, bodystore.ErrIncomplete
		}
	}
}

func (c *clickhouseReader) bodyBlobs(ctx context.Context, w readWindow, body bodystore.Body) (map[bodystore.Key][]byte, error) {
	blobs := map[bodystore.Key][]byte{}
	refs := []string{}
	seen := map[string]bool{}
	for _, ref := range body.Refs {
		b, err := hex.DecodeString(ref)
		if err != nil || len(b) != 16 {
			return nil, bodystore.ErrIncomplete
		}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	w.params.Set("lineage", strconv.FormatUint(body.Lineage, 10))
	total := 0
	for start := 0; start < len(refs); start += 16 {
		batch := refs[start:min(start+16, len(refs))]
		w.params.Set("hashes", "['"+strings.Join(batch, "','")+"']")
		for offset := 0; ; offset += rawChunk {
			w.params.Set("body_offset", strconv.Itoa(offset+1))
			var rows []struct {
				Hash string `json:"hash_hex"`
				Data []byte `json:"data_b64"`
				N    int    `json:"n"`
			}
			err := c.query(ctx, `SELECT hex(hash) AS hash_hex,base64Encode(substring(data,{body_offset:UInt32},262144)) AS data_b64,length(data) AS n FROM {db:Identifier}.body_blobs WHERE lineage={lineage:UInt64} AND hash IN (SELECT arrayJoin(arrayMap(x->unhex(x),{hashes:Array(String)}))) LIMIT 1 BY hash LIMIT 16 SETTINGS max_threads=1,max_block_size=16`, w.params, &rows)
			if err != nil {
				return nil, err
			}
			if len(rows) != len(batch) {
				return nil, bodystore.ErrIncomplete
			}
			more := false
			for _, r := range rows {
				h, err := hex.DecodeString(r.Hash)
				if err != nil || len(h) != 16 || !seen[r.Hash] || r.N > maxRawBody {
					return nil, bodystore.ErrIncomplete
				}
				key := bodystore.Key{Lineage: body.Lineage, Hash: bodystore.Hash(h)}
				blobs[key] = append(blobs[key], r.Data...)
				total += len(r.Data)
				if total > maxRawBody || len(blobs[key]) > r.N {
					return nil, bodystore.ErrIncomplete
				}
				if offset+rawChunk < r.N {
					more = true
				}
			}
			if !more {
				break
			}
		}
	}
	return blobs, nil
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, x := range v {
		out[k] = append([]string(nil), x...)
	}
	return out
}

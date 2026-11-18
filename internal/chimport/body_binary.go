package chimport

import (
	"bytes"
	"encoding/binary"
	"sort"
	"time"

	"github.com/Plana-Solutions/cpa-clickhouse-sink/bodystore"
)

type binaryRow struct{ bytes.Buffer }

func (w *binaryRow) u(v uint64) { var b [8]byte; binary.LittleEndian.PutUint64(b[:], v); w.Write(b[:]) }
func (w *binaryRow) n(v int)    { var b [10]byte; n := binary.PutUvarint(b[:], uint64(v)); w.Write(b[:n]) }
func (w *binaryRow) s(v string) { w.n(len(v)); w.WriteString(v) }
func (w *binaryRow) u32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.Write(b[:])
}
func (w *binaryRow) strs(v []string) {
	w.n(len(v))
	for _, s := range v {
		w.s(s)
	}
}
func (w *binaryRow) m(v map[string]string) {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w.n(len(keys))
	for _, k := range keys {
		w.s(k)
		w.s(v[k])
	}
}
func (w *binaryRow) ts(v string) {
	t, _ := time.Parse("2006-01-02 15:04:05.000000000", v)
	w.u(uint64(t.UnixNano()))
}

const bodyRequestColumns = `request_id,trace_id,started_at,completed_at,path,source_format,model,requested_model,stream,outcome,status_code,error,request_headers,request_body,response_headers,response_body,response_chunks,redaction_count,redaction_rules,lineage,body_len,body_sha256,body_residual,body_ref_offsets,body_refs`

func encodeBodyRequest(row requestRow, lineage uint64, sk bodystore.Skeleton) []byte {
	w := new(binaryRow)
	w.s(row.RequestID)
	w.s(row.TraceID)
	w.ts(row.StartedAt)
	w.ts(row.CompletedAt)
	w.s(row.Path)
	w.s(row.SourceFormat)
	w.s(row.Model)
	w.s(row.RequestedModel)
	if row.Stream {
		w.WriteByte(1)
	} else {
		w.WriteByte(0)
	}
	w.s(row.Outcome)
	w.WriteByte(byte(row.StatusCode))
	w.WriteByte(byte(row.StatusCode >> 8))
	w.s(row.Error)
	w.m(row.RequestHeaders)
	w.s(row.RequestBody)
	w.m(row.ResponseHeaders)
	w.s(row.ResponseBody)
	w.strs(row.ResponseChunks)
	w.u32(uint32(row.RedactionCount))
	w.strs(row.RedactionRules)
	w.Write(bodystore.EncodeSkeleton(lineage, sk))
	return w.Bytes()
}

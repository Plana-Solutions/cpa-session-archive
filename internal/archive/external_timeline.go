package archive

import (
	"encoding/json"
	"fmt"
)

func externalTimeline(record Record) (RequestTimelineView, error) {
	var payload struct {
		Event *ExternalEvent `json:"external_event"`
	}
	if err := json.Unmarshal(record.OriginalRequest, &payload); err != nil {
		return RequestTimelineView{}, fmt.Errorf("decode external event: %w", err)
	}
	if payload.Event == nil {
		return RequestTimelineView{}, fmt.Errorf("external event missing for %s", record.RequestID)
	}
	event := payload.Event
	text := event.Text
	if text == "" && len(event.Data) > 0 {
		text = string(event.Data)
	}
	return RequestTimelineView{
		RequestID: record.RequestID, SessionID: record.SessionID,
		Summary: record.Summary, StartedAt: record.StartedAt, CompletedAt: record.CompletedAt,
		Facets: record.Facets, Metadata: record.Metadata, Outcome: record.Outcome, Kind: "native_event", InputItems: 1,
		Entries: []TimelineEntry{{
			EventID: event.ID, ParentID: event.ParentID, Timestamp: &event.Timestamp,
			Role: event.Role, Text: text, Label: event.Name, CallID: event.CallID,
		}},
	}, nil
}

package archive

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

func (s *Store) externalTurnGroups(ctx context.Context, sessionID string) ([]turnGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT request_id,COALESCE(summary,''),started_at,completed_at,COALESCE(facets_json,'{}')
		FROM records WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []turnGroup{}
	for rows.Next() {
		var id, text, started, completed, facetsJSON string
		if err = rows.Scan(&id, &text, &started, &completed, &facetsJSON); err != nil {
			return nil, err
		}
		facets := map[string][]string{}
		if err = json.Unmarshal([]byte(facetsJSON), &facets); err != nil {
			return nil, err
		}
		role := firstFacet(facets, "source.role")
		start, _ := time.Parse(time.RFC3339Nano, started)
		end, _ := time.Parse(time.RFC3339Nano, completed)
		if len(groups) == 0 || role == "user" {
			groups = append(groups, turnGroup{summary: TurnSummary{
				TurnID: derivedTurnID(sessionID, id), SessionID: sessionID, FirstAt: start,
				LastAt: end, Outcome: "imported", ToolNames: []string{},
			}, toolNames: map[string]struct{}{}})
		}
		group := &groups[len(groups)-1]
		group.requestIDs = append(group.requestIDs, id)
		group.summary.Requests++
		group.summary.LastAt = end
		if role == "user" {
			group.summary.UserText = text
		}
		if role == "assistant" {
			group.summary.FinalText = text
		}
		for _, tool := range facets["tool.name"] {
			if _, exists := group.toolNames[tool]; !exists {
				group.toolNames[tool] = struct{}{}
				group.summary.ToolNames = append(group.summary.ToolNames, tool)
			}
		}
		if role == "compaction" {
			group.summary.HasCompaction = true
		}
	}
	for i := range groups {
		sort.Strings(groups[i].summary.ToolNames)
	}
	return groups, rows.Err()
}

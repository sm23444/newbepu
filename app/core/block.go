package core

import (
	"encoding/json"
	"fmt"
)

type BoundaryResp struct {
	Start *int64 `json:"start"`
	End   *int64 `json:"end"`
}

func (a Api) GetBoundaryHeights(start, end int64, network string) (int64, int64, error) {
	url := fmt.Sprintf("%s/api/block/get_boundary_heights?network=%s&start=%d&end=%d",
		a.api, network, start, end)

	body, err := a.get(url)
	if err != nil {
		return 0, 0, err
	}

	var response BoundaryResp
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, 0, fmt.Errorf("decode boundary heights: %w", err)
	}
	if response.Start == nil || response.End == nil {
		return 0, 0, fmt.Errorf("boundary heights response is missing start or end")
	}
	if *response.Start < 0 || *response.End < *response.Start {
		return 0, 0, fmt.Errorf("invalid boundary heights: start=%d end=%d", *response.Start, *response.End)
	}

	return *response.Start, *response.End, nil
}

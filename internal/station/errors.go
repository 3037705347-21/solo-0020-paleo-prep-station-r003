package station

import "example.com/paleo-prep-station/internal/contracts"

func notFoundSpecimen(id string) error {
	return contracts.NotFound("specimen_not_found", "未找到标本 "+id)
}

func notFoundPlan(id string) error {
	return contracts.NotFound("plan_not_found", "未找到修复方案 "+id)
}

package domain

import (
	"fmt"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

type ProgressEntry struct {
	ID         string    `json:"id"`
	PlanID     string    `json:"plan_id"`
	Sequence   int       `json:"sequence"`
	Stage      string    `json:"stage"`
	Percent    int       `json:"percent"`
	Note       string    `json:"note"`
	RecordedBy string    `json:"recorded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

func NewProgressEntry(
	id string,
	plan PrepPlan,
	previous []ProgressEntry,
	stage string,
	percent int,
	note string,
	recordedBy string,
	now time.Time,
) (ProgressEntry, error) {
	if id == "" {
		return ProgressEntry{}, contracts.Internal("missing_generated_id", "进度标识未生成", nil)
	}
	if plan.Status != PlanApproved && plan.Status != PlanActive {
		return ProgressEntry{}, contracts.Conflict("plan_not_recordable", "当前方案状态不能追加制备进度")
	}
	if err := validatePercent(percent); err != nil {
		return ProgressEntry{}, err
	}
	stageValue, err := requiredText("阶段名称", stage, 80)
	if err != nil {
		return ProgressEntry{}, err
	}
	noteValue, err := optionalText("进度说明", note, 500)
	if err != nil {
		return ProgressEntry{}, err
	}
	recorder, err := requiredText("记录人", recordedBy, 80)
	if err != nil {
		return ProgressEntry{}, err
	}
	if len(previous) > 0 && percent < previous[len(previous)-1].Percent {
		return ProgressEntry{}, contracts.Conflict("progress_regressed", "新的进度比例不能低于上一条记录")
	}
	return ProgressEntry{
		ID:         id,
		PlanID:     plan.ID,
		Sequence:   len(previous) + 1,
		Stage:      stageValue,
		Percent:    percent,
		Note:       noteValue,
		RecordedBy: recorder,
		CreatedAt:  now.UTC(),
	}, nil
}

func (p ProgressEntry) String() string {
	return fmt.Sprintf("%s#%d(%d%%)", p.PlanID, p.Sequence, p.Percent)
}

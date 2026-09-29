package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) CreatePlan(
	ctx context.Context,
	specimenID string,
	request contracts.CreatePlanRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var created domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		specimen, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		if m.HasOpenPlan(specimenID) {
			return contracts.Conflict("open_plan_exists", "该标本已存在未结束的修复方案")
		}
		plan, err := domain.NewPrepPlan(
			s.nextID("plan"),
			specimen,
			request.Objective,
			request.Method,
			request.Adhesive,
			request.TargetExposure,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := specimen.MarkPlanned(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		created = plan
		return nil
	})
	return created, err
}

func (s *Service) ApprovePlan(
	ctx context.Context,
	planID string,
	request contracts.ApprovalRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var approved domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		if err := plan.Approve(request.ApprovedBy, s.now()); err != nil {
			return err
		}
		if err := specimen.MarkPreparing(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		approved = plan
		return nil
	})
	return approved, err
}

func (s *Service) GetPlan(ctx context.Context, planID string) (PlanSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return PlanSnapshot{}, err
	}
	var snapshot PlanSnapshot
	err := s.repo.Read(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		snapshot = PlanSnapshot{
			Plan:     plan,
			Progress: m.ProgressForPlan(planID),
		}
		return nil
	})
	return snapshot, err
}

func (s *Service) ListPlansForSpecimen(
	ctx context.Context,
	specimenID string,
) ([]domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.PrepPlan
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		values = m.ListPlansForSpecimen(specimenID)
		return nil
	})
	return values, err
}

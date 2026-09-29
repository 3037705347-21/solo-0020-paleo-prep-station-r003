package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) RegisterSpecimen(
	ctx context.Context,
	request contracts.RegisterSpecimenRequest,
) (domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return domain.Specimen{}, err
	}

	var created domain.Specimen
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		value, err := domain.NewSpecimen(
			s.nextID("specimen"),
			request.CatalogNumber,
			request.FieldLabel,
			request.TaxonHint,
			request.MatrixKind,
			request.JacketNotes,
			s.now(),
		)
		if err != nil {
			return err
		}
		if _, exists := m.FindSpecimenByCatalog(value.CatalogNumber); exists {
			return contracts.Conflict("duplicate_catalog_number", "目录号已存在，不能覆盖原标本")
		}
		m.PutSpecimen(value)
		created = value
		return nil
	})
	return created, err
}

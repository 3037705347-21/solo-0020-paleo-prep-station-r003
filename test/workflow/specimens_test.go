package workflow_test

import (
	"net/http"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
)

// TestCatalogNumberIsNormalizedToUpperCase: the declared rule treats the
// catalog number as a case-insensitive identity, persisted in normalized form.
func TestCatalogNumberIsNormalizedToUpperCase(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	specimen := registerSpecimen(c, "abc-2026-007")

	if specimen.CatalogNumber != "ABC-2026-007" {
		t.Fatalf("catalog number should be normalized to upper case: got %q", specimen.CatalogNumber)
	}
	if specimen.Status != "registered" || specimen.Version != 1 {
		t.Fatalf("new specimen should be registered at version 1, got status=%q version=%d",
			specimen.Status, specimen.Version)
	}
	fetched := getSpecimen(c, specimen.ID)
	if fetched.CatalogNumber != "ABC-2026-007" {
		t.Fatalf("normalization should be durable, fetched %q", fetched.CatalogNumber)
	}
}

// TestCatalogNumberUniquenessIsCaseInsensitive: the same catalog number in
// different cases must identify the same specimen and be rejected on the second
// registration. The original specimen must never be overwritten.
func TestCatalogNumberUniquenessIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	first := registerSpecimen(c, "PPS-2026-777")
	duplicate := contracts.RegisterSpecimenRequest{
		CatalogNumber: "pps-2026-777",
		FieldLabel:    "被替换的野外标签",
		TaxonHint:     "其他分类",
		MatrixKind:    "灰岩",
		JacketNotes:   "第二次登记的夹克备注",
	}
	c.expectError(http.StatusConflict, "duplicate_catalog_number", http.MethodPost, "/specimens", duplicate)

	stored := getSpecimen(c, first.ID)
	if stored.FieldLabel != "北坡-07 野外标签" || stored.MatrixKind != "粉砂岩" {
		t.Fatalf("rejected duplicate registration must not overwrite the original specimen, got %+v", stored)
	}
	if stored.Version != 1 {
		t.Fatalf("rejected duplicate registration must not bump version, got %d", stored.Version)
	}
	items := listSpecimens(c)
	if len(items) != 1 {
		t.Fatalf("rejected duplicate registration must not create a specimen, list length=%d", len(items))
	}
}

// TestCatalogNumberSurroundingWhitespaceIsTrimmedBeforeUniquenessCheck.
func TestCatalogNumberTrimsWhitespaceBeforeNormalizing(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	specimen := registerSpecimen(c, "  pps-2026-101 ")
	if specimen.CatalogNumber != "PPS-2026-101" {
		t.Fatalf("catalog number should be trimmed then upper-cased, got %q", specimen.CatalogNumber)
	}
}

func TestRejectedRegistrationLeavesNoStateBehind(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		name string
		body contracts.RegisterSpecimenRequest
		code string
	}{
		{
			name: "missing catalog number",
			body: contracts.RegisterSpecimenRequest{
				CatalogNumber: "",
				FieldLabel:    "北坡-07",
				MatrixKind:    "粉砂岩",
			},
			code: "required_field",
		},
		{
			name: "catalog number with illegal character",
			body: contracts.RegisterSpecimenRequest{
				CatalogNumber: "PPS/2026",
				FieldLabel:    "北坡-07",
				MatrixKind:    "粉砂岩",
			},
			code: "invalid_catalog_number",
		},
		{
			name: "missing field label",
			body: contracts.RegisterSpecimenRequest{
				CatalogNumber: "PPS-2026-202",
				FieldLabel:    "   ",
				MatrixKind:    "粉砂岩",
			},
			code: "required_field",
		},
		{
			name: "missing matrix kind",
			body: contracts.RegisterSpecimenRequest{
				CatalogNumber: "PPS-2026-203",
				FieldLabel:    "北坡-07",
				MatrixKind:    "",
			},
			code: "required_field",
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newServer(t)
			c.expectError(http.StatusBadRequest, tc.code, http.MethodPost, "/specimens", tc.body)
			if items := listSpecimens(c); len(items) != 0 {
				t.Fatalf("failed registration must leave no specimens, got %d", len(items))
			}
		})
	}
}

func TestMalformedRegistrationJSONIsRejected(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	status, body := c.sendRaw(http.MethodPost, "/specimens", `{"catalog_number":`)
	if status != http.StatusBadRequest {
		t.Fatalf("malformed JSON should yield 400, got %d body=%v", status, body)
	}
	if items := listSpecimens(c); len(items) != 0 {
		t.Fatalf("malformed JSON must leave no specimens, got %d", len(items))
	}
}

func TestUnknownSpecimenReturns404(t *testing.T) {
	t.Parallel()
	c := newServer(t)

	c.expectError(http.StatusNotFound, "specimen_not_found",
		http.MethodGet, "/specimens/does-not-exist", nil)
}

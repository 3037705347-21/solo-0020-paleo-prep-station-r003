package domain

import (
	"regexp"
	"strings"

	"example.com/paleo-prep-station/internal/contracts"
)

var catalogPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,63}$`)

func requiredText(field, value string, max int) (string, error) {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return "", contracts.Invalid("required_field", field+"不能为空")
	}
	if len([]rune(cleaned)) > max {
		return "", contracts.Invalid("field_too_long", field+"长度超出限制")
	}
	return cleaned, nil
}

func optionalText(field, value string, max int) (string, error) {
	cleaned := strings.TrimSpace(value)
	if len([]rune(cleaned)) > max {
		return "", contracts.Invalid("field_too_long", field+"长度超出限制")
	}
	return cleaned, nil
}

func validateCatalogNumber(value string) (string, error) {
	cleaned, err := requiredText("目录号", value, 64)
	if err != nil {
		return "", err
	}
	if !catalogPattern.MatchString(cleaned) {
		return "", contracts.Invalid(
			"invalid_catalog_number",
			"目录号只能包含字母、数字、点、下划线和连字符",
		)
	}
	return strings.ToUpper(cleaned), nil
}

func validatePercent(value int) error {
	if value < 0 || value > 100 {
		return contracts.Invalid("invalid_percent", "进度比例必须位于 0 到 100 之间")
	}
	return nil
}

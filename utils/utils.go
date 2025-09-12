package utils

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const maxResourceNameLength = 63

func TruncateNameIfNeeded(name string) string {
	if errors := validation.IsQualifiedName(name); len(errors) != 0 {
		truncatedName := name[:maxResourceNameLength]
		truncatedName = strings.TrimRight(truncatedName, "-")
		return truncatedName
	} else {
		return name
	}
}

package utils

import (
	"regexp"
	"strings"
)

// SanitizeString removes basic HTML tags and trims whitespace to prevent XSS
// in user-provided strings like names or locations.
func SanitizeString(s string) string {
	s = strings.TrimSpace(s)
	// Basic HTML-stripping regex (removes <tag>...)
	re := regexp.MustCompile("<[^>]*>")
	return re.ReplaceAllString(s, "")
}

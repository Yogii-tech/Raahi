package utils

import "testing"

func TestSanitizeString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Normal string",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "String with leading/trailing whitespaces",
			input:    "   Hello World   ",
			expected: "Hello World",
		},
		{
			name:     "String with basic HTML tags",
			input:    "<b>Hello</b> World",
			expected: "Hello World",
		},
		{
			name:     "String with malicious script tags",
			input:    "<script>alert('XSS')</script>SafeText",
			expected: "alert('XSS')SafeText",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeString(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeString(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

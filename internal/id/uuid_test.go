package id

import (
	"regexp"
	"testing"
)

func TestNewReturnsRFC4122Version4UUID(t *testing.T) {
	value := New()
	pattern := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	if !pattern.MatchString(value) {
		t.Fatalf("New() = %q, want a lowercase UUID v4", value)
	}
}

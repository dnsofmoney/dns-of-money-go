package dnsofmoney

import (
	"fmt"
	"regexp"
)

// PayURIPattern is the FAS-1 pay: grammar: pay:<label>(.<label>)*. A label is
// lowercase alphanumeric plus hyphen, 1 to 63 characters, with no leading hyphen.
var PayURIPattern = regexp.MustCompile(`^pay:[a-z0-9][a-z0-9\-]{0,62}(\.[a-z0-9][a-z0-9\-]{0,62})*$`)

// MaxPayURILength caps the total length of a pay: URI.
const MaxPayURILength = 128

// IsValidPayURI reports whether value is a syntactically valid pay: URI.
func IsValidPayURI(value string) bool {
	return len(value) <= MaxPayURILength && PayURIPattern.MatchString(value)
}

// AssertValidPayURI returns value if it is valid, else an error.
func AssertValidPayURI(value string) (string, error) {
	if !IsValidPayURI(value) {
		return "", fmt.Errorf("dnsofmoney: invalid pay: URI format: %s", value)
	}
	return value, nil
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

var errSearchSyntax = errors.New("invalid tag search")

// Plain words remain literal fragments. tag:value and tag:"multiple words"
// are exact tags, combined with each other and plain words using AND.
func parseSearch(input string) (string, []string, error) {
	r := []rune(input)
	words, tags := []string{}, []string{}
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		start := i
		if i+4 <= len(r) && strings.EqualFold(string(r[i:i+4]), "tag:") {
			i += 4
			var tag string
			if i < len(r) && r[i] == '"' {
				quoted := i
				i++
				closed := false
				for i < len(r) {
					if r[i] == '\\' {
						i += 2
						continue
					}
					if r[i] == '"' {
						i++
						closed = true
						break
					}
					i++
				}
				if !closed || i > len(r) || json.Unmarshal([]byte(string(r[quoted:i])), &tag) != nil {
					return "", nil, fmt.Errorf("%w: close the tag's double quotes, for example tag:\"Family holiday\"", errSearchSyntax)
				}
				if i < len(r) && !unicode.IsSpace(r[i]) {
					return "", nil, fmt.Errorf("%w: put a space after a quoted tag", errSearchSyntax)
				}
			} else {
				value := i
				for i < len(r) && !unicode.IsSpace(r[i]) {
					i++
				}
				tag = string(r[value:i])
			}
			if strings.TrimSpace(tag) == "" {
				return "", nil, fmt.Errorf("%w: choose a suggestion or enter a tag after tag:", errSearchSyntax)
			}
			tags = append(tags, tag)
		} else {
			for i < len(r) && !unicode.IsSpace(r[i]) {
				i++
			}
			words = append(words, string(r[start:i]))
		}
	}
	return strings.Join(words, " "), cleanTags(tags), nil
}

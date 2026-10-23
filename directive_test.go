package pqmigrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasNoTxDirective(t *testing.T) {
	tests := []struct {
		name, contents string
		want           bool
	}{
		{"exact match", "-- pqmigrate:no-transaction\nselect 1;", true},
		{"directive only", "-- pqmigrate:no-transaction", true},
		{"trailing whitespace", "-- pqmigrate:no-transaction \t\nselect 1;", true},
		{"CRLF", "-- pqmigrate:no-transaction\r\nselect 1;", true},
		{"directive on line 2", "-- comment\n-- pqmigrate:no-transaction\nselect 1;", false},
		{"leading blank line", "\n-- pqmigrate:no-transaction\nselect 1;", false},
		{"leading BOM", "\xef\xbb\xbf-- pqmigrate:no-transaction\nselect 1;", false},
		{"different case", "-- PQMIGRATE:NO-TRANSACTION\nselect 1;", false},
		{"leading whitespace", " -- pqmigrate:no-transaction\nselect 1;", false},
		{"plain sql", "select 1;", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasNoTxDirective(tt.contents))
		})
	}
}

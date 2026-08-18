package retrieval

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsStopword(t *testing.T) {
	// Common English
	assert.True(t, IsStopword("the"))
	assert.True(t, IsStopword("update"))
	// FTS5/search-domain terms
	assert.True(t, IsStopword("porter"))
	assert.True(t, IsStopword("trigram"))
	assert.True(t, IsStopword("prose"))
	// Common data field names
	assert.True(t, IsStopword("name"))
	assert.True(t, IsStopword("title"))
	assert.True(t, IsStopword("id"))
	// Real terms should not be stopwords
	assert.False(t, IsStopword("authentication"))
	assert.False(t, IsStopword("middleware"))
}

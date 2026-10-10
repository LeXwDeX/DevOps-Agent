package core

import (
	"regexp"
	"testing"
)

func TestSchemaIDs(t *testing.T) {
	re := regexp.MustCompile(`^devops\.[a-z.]+/v[0-9]+$`)
	seen := map[string]bool{}
	for _, id := range []string{ExecutionRequestV1, TaskRunV1, EventV1} {
		if !re.MatchString(id) || seen[id] {
			t.Errorf("bad or duplicate schema id %q", id)
		}
		seen[id] = true
	}
}

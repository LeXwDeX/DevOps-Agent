package core

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSchemaIDs(t *testing.T) {
	re := regexp.MustCompile(`^devops\.[a-z.]+/v[0-9]+$`)
	seen := map[string]bool{}
	for _, id := range []string{ExecutionRequestV1, TaskRunV1, TaskDefinitionV1, EventV1, AuditRecordV1} {
		if !re.MatchString(id) || seen[id] {
			t.Errorf("bad or duplicate schema id %q", id)
		}
		seen[id] = true
	}
}

// 文档结构的每个字段都有显式的 snake_case JSON 名，且不用 omitempty。
func TestDocJSONTags(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, v := range []any{
		RequestDocV1{}, RunDocV1{}, TaskDefinitionDocV1{}, EventDocV1{}, AuditRecordDocV1{},
	} {
		checkTags(t, reflect.TypeOf(v), snake, map[reflect.Type]bool{})
	}
}

func checkTags(t *testing.T, typ reflect.Type, snake *regexp.Regexp, seen map[reflect.Type]bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || typ.PkgPath() == "time" || seen[typ] {
		return
	}
	seen[typ] = true
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag, ok := f.Tag.Lookup("json")
		if !ok || strings.Contains(tag, ",") || !snake.MatchString(tag) {
			t.Errorf("%s.%s: json tag %q must be a plain snake_case name", typ.Name(), f.Name, tag)
		}
		checkTags(t, f.Type, snake, seen)
	}
}

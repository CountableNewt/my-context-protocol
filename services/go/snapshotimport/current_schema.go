package snapshotimport

import (
	"errors"
	"sort"
	"strings"
)

const CurrentFormat = "mycontext-sqlite-snapshot-v2"

func checkpointObject(name string) bool {
	return name == "go_schema_migrations" || name == runTable || name == progressTable
}

// currentDefinitions accepts only exact shipped objects and preserves every
// present application table. Migration history and import checkpoints are
// reconstructed by the target tooling rather than copied as live state.
func currentDefinitions(actual map[string]string) (map[string]string, map[string]tableSpec, error) {
	if err := checkOwnedSchema(actual); err != nil {
		return nil, nil, err
	}
	schema := map[string]string{}
	specs := map[string]tableSpec{}
	for name, sql := range actual {
		if checkpointObject(name) {
			continue
		}
		schema[name] = normalizedCreate(sql)
		parts := createPattern.FindStringSubmatch(sql)
		if len(parts) != 3 {
			return nil, nil, errors.New("invalid current schema object")
		}
		if strings.EqualFold(parts[1], "TABLE") {
			columns, err := columnsOf(sql)
			if err != nil {
				return nil, nil, err
			}
			dependencies := []string{}
			for _, ref := range referencePattern.FindAllStringSubmatch(sql, -1) {
				dependencies = append(dependencies, strings.Trim(ref[1], `"`))
			}
			specs[name] = tableSpec{columns, dependencies}
		}
	}
	return schema, specs, nil
}

func snapshotDefinitions(format string, actual map[string]string) (map[string]string, map[string]tableSpec, error) {
	if format == Format {
		schema, tables, err := baseline()
		if err != nil {
			return nil, nil, err
		}
		if err = matchSchema(actual, schema); err != nil {
			return nil, nil, err
		}
		return schema, tables, nil
	}
	if format != CurrentFormat {
		return nil, nil, errors.New("unsupported snapshot format")
	}
	for name := range actual {
		if checkpointObject(name) {
			return nil, nil, errors.New("current snapshot contains private restore checkpoints")
		}
	}
	base, _, err := baseline()
	if err != nil {
		return nil, nil, err
	}
	if len(actual) <= len(base) {
		return nil, nil, errors.New("current snapshot must include additive application schema")
	}
	return currentDefinitions(actual)
}

func schemaScript(schema map[string]string) string {
	// Foreign-key targets may appear later; SQLite accepts forward DDL references.
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	var tables, indexes []string
	for _, name := range names {
		sql := schema[name]
		parts := createPattern.FindStringSubmatch(sql)
		if len(parts) == 3 && strings.EqualFold(parts[1], "TABLE") {
			tables = append(tables, sql)
		} else {
			indexes = append(indexes, sql)
		}
	}
	return strings.Join(append(tables, indexes...), ";\n") + ";"
}

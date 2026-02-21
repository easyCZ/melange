package sqlgen

import (
	"fmt"
	"slices"
	"strings"
)

// formatSQLStringList formats a list of strings as a SQL-safe list.
// For example, ["user", "org"] becomes "'user', 'org'".
// Returns empty string if the list is empty.
func formatSQLStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("'%s'", item)
	}
	return strings.Join(quoted, ", ")
}

func buildTupleLookupRelations(a RelationAnalysis) []string {
	// Build relation list from self + simple closure relations.
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	return relations
}

// generateHelperFunctions generates shared data helper functions that contain
// closure and userset model data. Other generated functions reference these
// helpers instead of embedding the data inline, reducing total SQL size.
func generateHelperFunctions(inline InlineSQLData) []string {
	var helpers []string

	// melange_closure_data(): returns closure rows as a table
	closureTable := InlineClosureTable(inline.ClosureRows, "t")
	closureFn := SqlFunction{
		Name:    ClosureDataFuncName,
		Returns: "TABLE(object_type TEXT, relation TEXT, satisfying_relation TEXT)",
		Body:    Raw("SELECT t.object_type, t.relation, t.satisfying_relation FROM " + closureTable.TableSQL()),
		Header:  []string{"Shared closure data for all generated functions"},
		Volatility: "IMMUTABLE",
	}
	helpers = append(helpers, closureFn.SQL()+"\n")

	// melange_userset_data(): returns userset rows as a table
	usersetTable := InlineUsersetTable(inline.UsersetRows, "t")
	usersetFn := SqlFunction{
		Name:    UsersetDataFuncName,
		Returns: "TABLE(object_type TEXT, relation TEXT, subject_type TEXT, subject_relation TEXT)",
		Body:    Raw("SELECT t.object_type, t.relation, t.subject_type, t.subject_relation FROM " + usersetTable.TableSQL()),
		Header:  []string{"Shared userset data for all generated functions"},
		Volatility: "IMMUTABLE",
	}
	helpers = append(helpers, usersetFn.SQL()+"\n")

	return helpers
}

// GeneratedSQL contains all SQL generated for a schema.
// This is applied atomically during migration to ensure consistent state.
type GeneratedSQL struct {
	// HelperFunctions contains CREATE OR REPLACE FUNCTION statements for
	// shared data helper functions (melange_closure_data, melange_userset_data).
	// These must be applied before any other functions that reference them.
	HelperFunctions []string

	// Functions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized check function (check_{type}_{relation}).
	// Each function accepts p_no_wildcard BOOLEAN to toggle wildcard matching.
	Functions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes requests to specialized functions based on object type and relation.
	// Accepts p_no_wildcard BOOLEAN to propagate wildcard behavior.
	Dispatcher string

	// BulkDispatcher contains the check_permission_bulk function that evaluates
	// multiple permission checks in a single SQL call using UNION ALL branches.
	BulkDispatcher string
}

// GenerateSQL generates specialized SQL functions for all relations in the schema.
//
// For each relation, it generates:
//   - A specialized check function that evaluates permission checks efficiently
//   - A no-wildcard variant for scenarios where wildcards are disallowed
//   - Dispatcher functions that route to the appropriate specialized function
//
// The inline parameter provides precomputed closure and userset data that is
// inlined into the generated functions as VALUES clauses, eliminating runtime
// table joins for this metadata.
//
// Returns an error if any function fails to generate, though this is rare
// as the analysis phase validates generation feasibility.
func GenerateSQL(analyses []RelationAnalysis, inline InlineSQLData) (GeneratedSQL, error) {
	var result GeneratedSQL

	// Generate shared data helper functions first (other functions reference these)
	result.HelperFunctions = generateHelperFunctions(inline)

	// Generate specialized function for each relation
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		fn, err := generateCheckFunction(a, inline)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating check function: %w", err)
		}
		result.Functions = append(result.Functions, fn)
	}

	// Generate dispatchers
	var err error
	result.Dispatcher, err = generateDispatcher(analyses)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating dispatcher: %w", err)
	}

	// Generate bulk dispatcher
	result.BulkDispatcher = generateBulkDispatcher(analyses)

	return result, nil
}

// functionName returns the name for a specialized check function.
func functionName(objectType, relation string) string {
	return fmt.Sprintf("check_%s_%s", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// sanitizeIdentifier converts a type/relation name to a valid SQL identifier.
// Delegates to the canonical implementation in sqldsl.
func sanitizeIdentifier(s string) string {
	return Ident(s)
}

// computeHasStandaloneAccess determines if the relation has access paths outside of intersections.
func computeHasStandaloneAccess(a RelationAnalysis) bool {
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Implied and recursive are always standalone, regardless of intersection.
	if a.Features.HasImplied || a.Features.HasRecursive {
		return true
	}

	// Check if any intersection group has a "This" part, meaning direct/userset access
	// is constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := slices.ContainsFunc(a.IntersectionGroups, func(g IntersectionGroupInfo) bool {
		return slices.ContainsFunc(g.Parts, func(p IntersectionPart) bool {
			return p.IsThis
		})
	})

	// Direct and userset are standalone only if not inside an intersection.
	return (a.Features.HasDirect || a.Features.HasUserset) && !hasIntersectionWithThis
}

// DispatcherData contains data for rendering the dispatcher template.
type DispatcherData struct {
	FunctionName            string
	HasSpecializedFunctions bool
	Cases                   []DispatcherCase
}

// DispatcherCase represents a single CASE WHEN branch in the dispatcher.
// Each case routes a specific (object_type, relation) pair to its specialized function.
type DispatcherCase struct {
	ObjectType          string
	Relation            string
	CheckFunctionName   string
	Inlineable          bool     // true if simple direct-assignment only (bulk dispatcher can inline EXISTS)
	DirectSubjectTypes  []string // subject types allowed for direct tuples (used in inline)
	SatisfyingRelations []string // relations in closure that satisfy this one (used in inline userset check)
}

// CollectFunctionNames returns all function names that will be generated for the given analyses.
// This is used for migration tracking and orphan detection to identify stale functions
// that need to be dropped when the schema changes.
//
// The returned list includes:
//   - Helper data functions: melange_closure_data, melange_userset_data
//   - Specialized check functions: check_{type}_{relation}
//   - Specialized list functions: list_{type}_{relation}_objects, list_{type}_{relation}_subjects
//   - Dispatcher functions (always included): check_permission, list_accessible_objects, etc.
func CollectFunctionNames(analyses []RelationAnalysis) []string {
	var names []string

	// Helper data functions are always generated
	names = append(names, ClosureDataFuncName, UsersetDataFuncName)

	for _, a := range analyses {
		if a.Capabilities.CheckAllowed {
			names = append(names,
				functionName(a.ObjectType, a.Relation),
			)
		}
		if a.Capabilities.ListAllowed {
			names = append(names,
				listObjectsFunctionName(a.ObjectType, a.Relation),
				listSubjectsFunctionName(a.ObjectType, a.Relation),
			)
		}
	}

	// Dispatchers are always generated
	names = append(names,
		"check_permission",
		"check_permission_internal",
		"check_permission_bulk",
		"list_accessible_objects",
		"list_accessible_subjects",
	)

	return names
}

package sqldsl

// Userset operations for extracting components from userset identifiers.
// Format: "object_id#relation" (e.g., "group:1#member")

// UsersetObjectID extracts the object ID: "group:1#member" -> "group:1"
type UsersetObjectID struct {
	Source Expr
}

func (u UsersetObjectID) SQL() string {
	return "split_part(" + u.Source.SQL() + ", '#', 1)"
}

// UsersetRelation extracts the relation: "group:1#member" -> "member"
type UsersetRelation struct {
	Source Expr
}

func (u UsersetRelation) SQL() string {
	return "split_part(" + u.Source.SQL() + ", '#', 2)"
}

// HasUserset checks if an expression contains a userset marker (#).
type HasUserset struct {
	Source Expr
}

func (h HasUserset) SQL() string {
	return hashPosition(h.Source) + " > 0"
}

// NoUserset checks if an expression does NOT contain a userset marker (#).
type NoUserset struct {
	Source Expr
}

func (n NoUserset) SQL() string {
	return hashPosition(n.Source) + " = 0"
}

// SubstringUsersetRelation extracts the relation portion after the '#' marker.
// Used when the input contains a userset marker and we need just the relation.
type SubstringUsersetRelation struct {
	Source Expr
}

func (s SubstringUsersetRelation) SQL() string {
	src := s.Source.SQL()
	return "substring(" + src + " from position('#' in " + src + ") + 1)"
}

// IsWildcard checks if an expression equals the wildcard value "*".
type IsWildcard struct {
	Source Expr
}

func (w IsWildcard) SQL() string {
	return w.Source.SQL() + " = '*'"
}

// hashPosition returns the SQL for finding '#' position in an expression.
func hashPosition(expr Expr) string {
	return "position('#' in " + expr.SQL() + ")"
}

// SubjectIDMatch creates a condition for matching subject IDs.
// When allowWildcard is true, generates a dynamic expression that respects p_no_wildcard:
//
//	(subject_id = p_subject_id OR (subject_id = '*' AND NOT p_no_wildcard))
//
// When allowWildcard is false, matches exact ID only (no wildcard support in the model).
func SubjectIDMatch(column, subjectID Expr, allowWildcard bool, noWildcardExpr ...Expr) Expr {
	exactMatch := Eq{Left: column, Right: subjectID}
	if !allowWildcard {
		return exactMatch
	}

	var flag Expr
	if len(noWildcardExpr) > 0 && noWildcardExpr[0] != nil {
		flag = noWildcardExpr[0]
	} else {
		flag = Param("p_no_wildcard")
	}

	if b, ok := flag.(Bool); ok {
		if bool(b) {
			return exactMatch
		}
		return Or(exactMatch, IsWildcard{Source: column})
	}

	return Or(exactMatch, And(IsWildcard{Source: column}, Not(flag)))
}

// UsersetNormalized replaces the relation in a userset with a new relation.
// Example: "group:1#admin" with relation "member" -> "group:1#member"
type UsersetNormalized struct {
	Source   Expr
	Relation Expr
}

func (u UsersetNormalized) SQL() string {
	src := u.Source.SQL()
	pos := hashPosition(u.Source)
	objectID := "substring(" + src + " from 1 for " + pos + " - 1)"
	return objectID + " || '#' || " + u.Relation.SQL()
}

// NormalizedUsersetSubject combines the object_id from a userset with a new relation.
// Example: split_part(subject_id, '#', 1) || '#' || v_filter_relation
func NormalizedUsersetSubject(subjectID, relation Expr) Expr {
	return Concat{Parts: []Expr{
		UsersetObjectID{Source: subjectID},
		Lit("#"),
		relation,
	}}
}

package sqlgen

// CheckPermission represents a call to check_permission_internal.
type CheckPermission struct {
	Subject     SubjectRef
	Relation    string
	Object      ObjectRef
	Visited     Expr // nil uses empty array
	ExpectAllow bool // true compares "= 1", false compares "= 0"
	NoWildcard  Expr // nil uses p_no_wildcard parameter passthrough
}

func (c CheckPermission) SQL() string {
	visited := c.Visited
	if visited == nil {
		visited = EmptyArray{}
	}
	noWildcard := c.NoWildcard
	if noWildcard == nil {
		noWildcard = Param("p_no_wildcard")
	}
	return FuncCallEq{
		FuncName: "check_permission_internal",
		Args: []Expr{
			c.Subject.Type,
			c.Subject.ID,
			Lit(c.Relation),
			c.Object.Type,
			c.Object.ID,
			visited,
			noWildcard,
		},
		Value: expectValue(c.ExpectAllow),
	}.SQL()
}

// CheckAccess creates a CheckPermission that expects access to be allowed.
func CheckAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: true,
	}
}

// CheckNoAccess creates a CheckPermission that expects access to be denied.
func CheckNoAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: false,
	}
}

// CheckPermissionCall represents a call to a permission check function via the dispatcher.
// Passes p_no_wildcard to control wildcard matching behavior.
type CheckPermissionCall struct {
	FunctionName string
	Subject      SubjectRef
	Relation     string
	Object       ObjectRef
	ExpectAllow  bool
	NoWildcard   Expr // nil uses Bool(false) to match original check_permission default
}

func (c CheckPermissionCall) SQL() string {
	noWildcard := c.NoWildcard
	if noWildcard == nil {
		noWildcard = Bool(false)
	}
	return FuncCallEq{
		FuncName: c.FunctionName,
		Args: []Expr{
			c.Subject.Type,
			c.Subject.ID,
			Lit(c.Relation),
			c.Object.Type,
			c.Object.ID,
			noWildcard,
		},
		Value: expectValue(c.ExpectAllow),
	}.SQL()
}

// expectValue returns Int(1) for allow, Int(0) for deny.
func expectValue(allow bool) Expr {
	if allow {
		return Int(1)
	}
	return Int(0)
}

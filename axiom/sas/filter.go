package sas

import "github.com/axiomhq/axiom-go/axiom/querylegacy"

// filter is like `Filter` but with different, smaller, two letter struct tags
// to reduce the size of its JSON representation.
type filter struct {
	Op            querylegacy.FilterOp `json:"op"`
	Field         string               `json:"fd"`
	Value         any                  `json:"vl"`
	CaseSensitive bool                 `json:"cs"`
	Children      []filter             `json:"ch,omitempty"`
}

// filterFromQueryFilter creates a `filter` from the values of a
// `querylegacy.Filter`. This function calls itself recursively to handle nested
// filters.
func filterFromQueryFilter(qf querylegacy.Filter) filter {
	f := filter{
		Op:            qf.Op,
		Field:         qf.Field,
		Value:         qf.Value,
		CaseSensitive: qf.CaseSensitive,
	}

	for _, child := range qf.Children {
		f.Children = append(f.Children, filterFromQueryFilter(child))
	}

	return f
}

// toQueryFilter creates a `querylegacy.Filter` from the `filter`. This function
// calls itself recursively to handle nested filters.
func (f filter) toQueryFilter() querylegacy.Filter {
	qf := querylegacy.Filter{
		Op:            f.Op,
		Field:         f.Field,
		Value:         f.Value,
		CaseSensitive: f.CaseSensitive,
	}

	for _, child := range f.Children {
		qf.Children = append(qf.Children, child.toQueryFilter())
	}

	return qf
}

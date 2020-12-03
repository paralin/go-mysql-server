package plan

import (
	"io"
	"reflect"

	"github.com/opentracing/opentracing-go"

	"github.com/dolthub/go-mysql-server/sql"
)

// An IndexedJoin is a join that uses index lookups for the secondary table.
type IndexedJoin struct {
	// The primary and secondary table nodes. The normal meanings of Left and
	// Right in BinaryNode aren't necessarily meaningful here -- the Left node is always the primary table, and the Right
	// node is always the secondary. These may or may not correspond to the left and right tables in the written query.
	BinaryNode
	// The join condition.
	Cond sql.Expression
	// The type of join. Left and right refer to the lexical position in the written query, not primary / secondary. In
	// the case of a right join, the right table will always be the primary.
	joinType JoinType
}

// JoinType returns the join type for this indexed join
func (ij *IndexedJoin) JoinType() JoinType {
	return ij.joinType
}

func NewIndexedJoin(left, right sql.Node, joinType JoinType, cond sql.Expression) *IndexedJoin {
	return &IndexedJoin{
		BinaryNode:       BinaryNode{left, right},
		joinType:         joinType,
		Cond:             cond,
	}
}

func (ij *IndexedJoin) String() string {
	pr := sql.NewTreePrinter()
	joinType := ""
	switch ij.joinType {
	case JoinTypeLeft:
		joinType = "Left"
	case JoinTypeRight:
		joinType = "Right"
	}
	_ = pr.WriteNode("%sIndexedJoin(%s)", joinType, ij.Cond)
	_ = pr.WriteChildren(ij.left.String(), ij.right.String())
	return pr.String()
}

func (ij *IndexedJoin) DebugString() string {
	pr := sql.NewTreePrinter()
	joinType := ""
	switch ij.joinType {
	case JoinTypeLeft:
		joinType = "Left"
	case JoinTypeRight:
		joinType = "Right"
	}
	_ = pr.WriteNode("%sIndexedJoin(%s)", joinType, sql.DebugString(ij.Cond))
	_ = pr.WriteChildren(sql.DebugString(ij.left), sql.DebugString(ij.right))
	return pr.String()
}

func (ij *IndexedJoin) Schema() sql.Schema {
	return append(ij.left.Schema(), ij.right.Schema()...)
}

func (ij *IndexedJoin) RowIter(ctx *sql.Context, row sql.Row) (sql.RowIter, error) {
	return indexedJoinRowIter(ctx, row, ij.left, ij.right, ij.Cond, ij.joinType)
}

func (ij *IndexedJoin) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 2 {
		return nil, sql.ErrInvalidChildrenNumber.New(ij, len(children), 2)
	}
	return NewIndexedJoin(children[0], children[1], ij.joinType, ij.Cond), nil
}

func indexedJoinRowIter(
		ctx *sql.Context,
		parentRow sql.Row,
		left sql.Node,
		right sql.Node,
		cond sql.Expression,
		joinType JoinType,
) (sql.RowIter, error) {
	var leftName, rightName string
	if leftTable, ok := left.(sql.Nameable); ok {
		leftName = leftTable.Name()
	} else {
		leftName = reflect.TypeOf(left).String()
	}

	if rightTable, ok := right.(sql.Nameable); ok {
		rightName = rightTable.Name()
	} else {
		rightName = reflect.TypeOf(right).String()
	}

	span, ctx := ctx.Span("plan.indexedJoin", opentracing.Tags{
		"left":  leftName,
		"right": rightName,
	})

	l, err := left.RowIter(ctx, parentRow)
	if err != nil {
		span.Finish()
		return nil, err
	}
	return sql.NewSpanIter(span, &indexedJoinIter{
		parentRow:         parentRow,
		primary:           l,
		secondaryProvider: right,
		ctx:               ctx,
		cond:              cond,
		joinType:          joinType,
		rowSize:           len(parentRow) + len(left.Schema()) + len(right.Schema()),
	}), nil
}

// indexedJoinIter is an iterator that iterates over every row in the primary table and performs an index lookup in
// the secondary table for each value
type indexedJoinIter struct {
	parentRow            sql.Row
	primary              sql.RowIter
	primaryRow           sql.Row
	secondaryProvider    sql.Node
	secondary            sql.RowIter
	cond                 sql.Expression
	joinType             JoinType

	ctx        *sql.Context
	foundMatch bool
	rowSize    int
}

func (i *indexedJoinIter) loadPrimary() error {
	if i.primaryRow == nil {
		r, err := i.primary.Next()
		if err != nil {
			return err
		}

		i.primaryRow = i.parentRow.Append(r)
		i.foundMatch = false
	}

	return nil
}

func (i *indexedJoinIter) loadSecondary() (sql.Row, error) {
	if i.secondary == nil {
		rowIter, err := i.secondaryProvider.RowIter(i.ctx, i.primaryRow)
		if err != nil {
			return nil, err
		}

		i.secondary = rowIter
	}

	secondaryRow, err := i.secondary.Next()
	if err != nil {
		if err == io.EOF {
			i.secondary = nil
			i.primaryRow = nil
			return nil, io.EOF
		}
		return nil, err
	}

	return secondaryRow, nil
}

func (i *indexedJoinIter) Next() (sql.Row, error) {
	for {
		if err := i.loadPrimary(); err != nil {
			return nil, err
		}

		primary := i.primaryRow
		secondary, err := i.loadSecondary()
		if err != nil {
			if err == io.EOF {
				if !i.foundMatch && (i.joinType == JoinTypeLeft || i.joinType == JoinTypeRight) {
					row := i.buildRow(primary, nil)
					return row[len(i.parentRow):], nil
				}
				continue
			}
			return nil, err
		}

		row := i.buildRow(primary, secondary)
		matches, err := conditionIsTrue(i.ctx, row, i.cond)
		if err != nil {
			return nil, err
		}

		if !matches {
			continue
		}

		i.foundMatch = true
		return row[len(i.parentRow):], nil
	}
}

func conditionIsTrue(ctx *sql.Context, row sql.Row, cond sql.Expression) (bool, error) {
	v, err := cond.Eval(ctx, row)
	if err != nil {
		return false, err
	}

	// Expressions containing nil evaluate to nil, not false
	return v == true, nil
}

// buildRow builds the result set row using the rows from the primary and secondary tables
func (i *indexedJoinIter) buildRow(primary, secondary sql.Row) sql.Row {
	row := make(sql.Row, i.rowSize)

	copy(row, primary)
	copy(row[len(primary):], secondary)

	return row
}

func (i *indexedJoinIter) Close() (err error) {
	if i.primary != nil {
		if err = i.primary.Close(); err != nil {
			if i.secondary != nil {
				_ = i.secondary.Close()
			}
			return err
		}
	}

	if i.secondary != nil {
		err = i.secondary.Close()
		i.secondary = nil
	}

	return err
}

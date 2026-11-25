// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import (
	"fmt"
	"internal/pkgbits"
	"slices"
)

// LogosCtSyntaxFields handles syntax fields whose representation is owned by
// the caller. On read, the callbacks fill their arguments; on write, they only
// inspect them.
type LogosCtSyntaxFields struct {
	Pos    func(*Pos)
	Pragma func(*Pragma)
}

// WriteLogosCtSyntax serializes a generic declaration as a graph of syntax
// nodes. Node IDs preserve parser sharing (e.g., grouped parameter types) and
// back edges (branch targets). Type-checker state is not part of the graph.
func WriteLogosCtSyntax(w *pkgbits.Encoder, decl *FuncDecl, fields LogosCtSyntaxFields) {
	c := ctSyntaxCodec{w: w, fields: fields, nodeIDs: make(map[Node]int)}
	ctSyntaxNode(&c, &decl)
}

// ReadLogosCtSyntax reconstructs a declaration with no type-checker state.
func ReadLogosCtSyntax(r *pkgbits.Decoder, fields LogosCtSyntaxFields) *FuncDecl {
	c := ctSyntaxCodec{r: r, fields: fields}
	var decl *FuncDecl
	ctSyntaxNode(&c, &decl)
	return decl
}

// CloneLogosCtSyntax copies structural syntax for a fresh type-check. Compiler
// pragmas and positions are immutable inputs shared by the copies.
func CloneLogosCtSyntax(decl *FuncDecl) *FuncDecl {
	c := ctSyntaxCodec{
		nodeCopies: make(map[Node]Node),
		fields: LogosCtSyntaxFields{
			Pos:    func(*Pos) {},
			Pragma: func(*Pragma) {},
		},
	}
	ctSyntaxNode(&c, &decl)
	return decl
}

type ctSyntaxCodec struct {
	w      *pkgbits.Encoder
	r      *pkgbits.Decoder
	fields LogosCtSyntaxFields

	nodeIDs    map[Node]int
	nodes      []Node
	groupIDs   map[*Group]int
	groups     []*Group
	nodeCopies map[Node]Node
}

func (c *ctSyntaxCodec) transfer(node Node) Node {
	if c.w != nil {
		if node == nil {
			c.w.Len(0)
			return nil
		}
		if id := c.nodeIDs[node]; id != 0 {
			c.w.Len(id)
			return node
		}
		id := len(c.nodeIDs) + 1
		c.nodeIDs[node] = id
		c.w.Len(id)
		ctSyntaxDescribe(c, node)
	} else if c.r != nil {
		id := c.r.Len()
		if id == 0 {
			return nil
		}
		if id <= len(c.nodes) {
			return c.nodes[id-1]
		}
		if id != len(c.nodes)+1 {
			panic("invalid comptime syntax node reference")
		}
		tag := c.r.Len()
		if tag >= len(ctSyntaxNodes) {
			panic("invalid comptime syntax node tag")
		}
		node = ctSyntaxNodes[tag]()
		c.nodes = append(c.nodes, node)
	} else {
		if node == nil {
			return nil
		}
		if existing := c.nodeCopies[node]; existing != nil {
			return existing
		}
		original := node
		node = ctSyntaxDescribe(c, node)
		c.nodeCopies[original] = node
		if expr, ok := node.(Expr); ok {
			expr.SetTypeInfo(TypeAndValue{})
		}
	}

	pos := node.Pos()
	c.fields.Pos(&pos)
	if c.r != nil {
		node.SetPos(pos)
	}
	c.nodeFields(node)
	return node
}

func ctSyntaxNode[N Node](c *ctSyntaxCodec, slot *N) {
	var node Node
	var zero N
	if c.r == nil && any(*slot) != any(zero) {
		node = *slot
	}
	node = c.transfer(node)
	if c.w == nil && node != nil {
		*slot = node.(N)
	}
}

func ctSyntaxSlice[T any](c *ctSyntaxCodec, slice *[]T, item func(*ctSyntaxCodec, *T)) {
	if c.w != nil {
		if *slice == nil {
			c.w.Len(0)
			return
		}
		c.w.Len(len(*slice) + 1)
	} else if c.r != nil {
		n := c.r.Len()
		if n == 0 {
			return
		}
		*slice = make([]T, n-1)
	} else {
		*slice = slices.Clone(*slice)
	}
	for i := range *slice {
		item(c, &(*slice)[i])
	}
}

func ctSyntaxNumber[T ~int | ~uint | ~uint8](c *ctSyntaxCodec, n *T) {
	if c.w != nil {
		c.w.Int(int(*n))
	} else if c.r != nil {
		*n = T(c.r.Int())
	}
}

func (c *ctSyntaxCodec) boolean(value *bool) {
	if c.w != nil {
		c.w.Bool(*value)
	} else if c.r != nil {
		*value = c.r.Bool()
	}
}

func (c *ctSyntaxCodec) text(value *string) {
	if c.w != nil {
		c.w.String(*value)
	} else if c.r != nil {
		*value = c.r.String()
	}
}

func (c *ctSyntaxCodec) group(group **Group) {
	if c.w != nil {
		if *group == nil {
			c.w.Len(0)
			return
		}
		id := c.groupIDs[*group]
		if id == 0 {
			if c.groupIDs == nil {
				c.groupIDs = make(map[*Group]int)
			}
			id = len(c.groupIDs) + 1
			c.groupIDs[*group] = id
		}
		c.w.Len(id)
		return
	}
	if c.r == nil {
		return
	}
	id := c.r.Len()
	if id == 0 {
		return
	}
	if id > len(c.groups)+1 {
		panic("invalid comptime syntax group reference")
	}
	if id == len(c.groups)+1 {
		c.groups = append(c.groups, new(Group))
	}
	*group = c.groups[id-1]
}

func ctSyntaxSwitchCase(c *ctSyntaxCodec, slot **ConstSwitchCase) {
	if c.r != nil {
		*slot = new(ConstSwitchCase)
	} else if c.w == nil {
		copy := **slot
		*slot = &copy
	}
	n := *slot
	ctSyntaxNode(c, &n.Type)
	c.boolean(&n.Tilde)
	c.fields.Pos(&n.TildePos)
	ctSyntaxSlice(c, &n.Body, ctSyntaxNode[Stmt])
	c.fields.Pos(&n.Case)
	c.fields.Pos(&n.Colon)
}

// The tags are local to the comptime template payload, not Go export data.
const (
	ctSyntaxImportDecl = iota
	ctSyntaxConstDecl
	ctSyntaxTypeDecl
	ctSyntaxVarDecl
	ctSyntaxFuncDecl
	ctSyntaxName
	ctSyntaxBasicLit
	ctSyntaxCompositeLit
	ctSyntaxKeyValueExpr
	ctSyntaxFuncLit
	ctSyntaxParenExpr
	ctSyntaxSelectorExpr
	ctSyntaxIndexExpr
	ctSyntaxSliceExpr
	ctSyntaxAssertExpr
	ctSyntaxTypeSwitchGuard
	ctSyntaxOperation
	ctSyntaxCallExpr
	ctSyntaxListExpr
	ctSyntaxArrayType
	ctSyntaxSliceType
	ctSyntaxDotsType
	ctSyntaxStructType
	ctSyntaxField
	ctSyntaxInterfaceType
	ctSyntaxFuncType
	ctSyntaxMapType
	ctSyntaxChanType
	ctSyntaxEmptyStmt
	ctSyntaxLabeledStmt
	ctSyntaxBlockStmt
	ctSyntaxExprStmt
	ctSyntaxSendStmt
	ctSyntaxDeclStmt
	ctSyntaxAssignStmt
	ctSyntaxBranchStmt
	ctSyntaxCallStmt
	ctSyntaxReturnStmt
	ctSyntaxIfStmt
	ctSyntaxForStmt
	ctSyntaxSwitchStmt
	ctSyntaxSelectStmt
	ctSyntaxRangeClause
	ctSyntaxCaseClause
	ctSyntaxCommClause
	ctSyntaxTryExpr
	ctSyntaxErrdeferStmt
	ctSyntaxSwitchExpr
	ctSyntaxArmClause
	ctSyntaxAssertedStruct
	ctSyntaxPropertyType
	ctSyntaxOptionType
	ctSyntaxCoalesceExpr
	ctSyntaxOptSelectExpr
	ctSyntaxConstType
	ctSyntaxConstFuncDecl
	ctSyntaxYieldStmt
	ctSyntaxScopeDeferStmt
	ctSyntaxFieldDefault
	ctSyntaxSpreadExpr
	ctSyntaxInterpExpr
	ctSyntaxTupleType
	ctSyntaxTupleLit
	ctSyntaxLambdaExpr
	ctSyntaxConstIfStmt
	ctSyntaxConstSwitchStmt
	ctSyntaxConstForStmt
	ctSyntaxFieldProjExpr
	ctSyntaxGroupStmt
	ctSyntaxGroupChild
)

func ctSyntaxDescribe(c *ctSyntaxCodec, node Node) Node {
	switch n := node.(type) {
	case *ImportDecl:
		return ctSyntaxShape(c, n, ctSyntaxImportDecl)
	case *ConstDecl:
		return ctSyntaxShape(c, n, ctSyntaxConstDecl)
	case *TypeDecl:
		return ctSyntaxShape(c, n, ctSyntaxTypeDecl)
	case *VarDecl:
		return ctSyntaxShape(c, n, ctSyntaxVarDecl)
	case *FuncDecl:
		return ctSyntaxShape(c, n, ctSyntaxFuncDecl)
	case *Name:
		return ctSyntaxShape(c, n, ctSyntaxName)
	case *BasicLit:
		return ctSyntaxShape(c, n, ctSyntaxBasicLit)
	case *CompositeLit:
		return ctSyntaxShape(c, n, ctSyntaxCompositeLit)
	case *KeyValueExpr:
		return ctSyntaxShape(c, n, ctSyntaxKeyValueExpr)
	case *FuncLit:
		return ctSyntaxShape(c, n, ctSyntaxFuncLit)
	case *ParenExpr:
		return ctSyntaxShape(c, n, ctSyntaxParenExpr)
	case *SelectorExpr:
		return ctSyntaxShape(c, n, ctSyntaxSelectorExpr)
	case *IndexExpr:
		return ctSyntaxShape(c, n, ctSyntaxIndexExpr)
	case *SliceExpr:
		return ctSyntaxShape(c, n, ctSyntaxSliceExpr)
	case *AssertExpr:
		return ctSyntaxShape(c, n, ctSyntaxAssertExpr)
	case *TypeSwitchGuard:
		return ctSyntaxShape(c, n, ctSyntaxTypeSwitchGuard)
	case *Operation:
		return ctSyntaxShape(c, n, ctSyntaxOperation)
	case *CallExpr:
		return ctSyntaxShape(c, n, ctSyntaxCallExpr)
	case *ListExpr:
		return ctSyntaxShape(c, n, ctSyntaxListExpr)
	case *ArrayType:
		return ctSyntaxShape(c, n, ctSyntaxArrayType)
	case *SliceType:
		return ctSyntaxShape(c, n, ctSyntaxSliceType)
	case *DotsType:
		return ctSyntaxShape(c, n, ctSyntaxDotsType)
	case *StructType:
		return ctSyntaxShape(c, n, ctSyntaxStructType)
	case *Field:
		return ctSyntaxShape(c, n, ctSyntaxField)
	case *InterfaceType:
		return ctSyntaxShape(c, n, ctSyntaxInterfaceType)
	case *FuncType:
		return ctSyntaxShape(c, n, ctSyntaxFuncType)
	case *MapType:
		return ctSyntaxShape(c, n, ctSyntaxMapType)
	case *ChanType:
		return ctSyntaxShape(c, n, ctSyntaxChanType)
	case *EmptyStmt:
		return ctSyntaxShape(c, n, ctSyntaxEmptyStmt)
	case *LabeledStmt:
		return ctSyntaxShape(c, n, ctSyntaxLabeledStmt)
	case *BlockStmt:
		return ctSyntaxShape(c, n, ctSyntaxBlockStmt)
	case *ExprStmt:
		return ctSyntaxShape(c, n, ctSyntaxExprStmt)
	case *SendStmt:
		return ctSyntaxShape(c, n, ctSyntaxSendStmt)
	case *DeclStmt:
		return ctSyntaxShape(c, n, ctSyntaxDeclStmt)
	case *AssignStmt:
		return ctSyntaxShape(c, n, ctSyntaxAssignStmt)
	case *BranchStmt:
		return ctSyntaxShape(c, n, ctSyntaxBranchStmt)
	case *CallStmt:
		return ctSyntaxShape(c, n, ctSyntaxCallStmt)
	case *ReturnStmt:
		return ctSyntaxShape(c, n, ctSyntaxReturnStmt)
	case *IfStmt:
		return ctSyntaxShape(c, n, ctSyntaxIfStmt)
	case *ForStmt:
		return ctSyntaxShape(c, n, ctSyntaxForStmt)
	case *SwitchStmt:
		return ctSyntaxShape(c, n, ctSyntaxSwitchStmt)
	case *SelectStmt:
		return ctSyntaxShape(c, n, ctSyntaxSelectStmt)
	case *RangeClause:
		return ctSyntaxShape(c, n, ctSyntaxRangeClause)
	case *CaseClause:
		return ctSyntaxShape(c, n, ctSyntaxCaseClause)
	case *CommClause:
		return ctSyntaxShape(c, n, ctSyntaxCommClause)
	case *TryExpr:
		return ctSyntaxShape(c, n, ctSyntaxTryExpr)
	case *ErrdeferStmt:
		return ctSyntaxShape(c, n, ctSyntaxErrdeferStmt)
	case *SwitchExpr:
		return ctSyntaxShape(c, n, ctSyntaxSwitchExpr)
	case *ArmClause:
		return ctSyntaxShape(c, n, ctSyntaxArmClause)
	case *AssertedStruct:
		return ctSyntaxShape(c, n, ctSyntaxAssertedStruct)
	case *PropertyType:
		return ctSyntaxShape(c, n, ctSyntaxPropertyType)
	case *OptionType:
		return ctSyntaxShape(c, n, ctSyntaxOptionType)
	case *CoalesceExpr:
		return ctSyntaxShape(c, n, ctSyntaxCoalesceExpr)
	case *OptSelectExpr:
		return ctSyntaxShape(c, n, ctSyntaxOptSelectExpr)
	case *ConstType:
		return ctSyntaxShape(c, n, ctSyntaxConstType)
	case *ConstFuncDecl:
		return ctSyntaxShape(c, n, ctSyntaxConstFuncDecl)
	case *YieldStmt:
		return ctSyntaxShape(c, n, ctSyntaxYieldStmt)
	case *ScopeDeferStmt:
		return ctSyntaxShape(c, n, ctSyntaxScopeDeferStmt)
	case *FieldDefault:
		return ctSyntaxShape(c, n, ctSyntaxFieldDefault)
	case *SpreadExpr:
		return ctSyntaxShape(c, n, ctSyntaxSpreadExpr)
	case *InterpExpr:
		return ctSyntaxShape(c, n, ctSyntaxInterpExpr)
	case *TupleType:
		return ctSyntaxShape(c, n, ctSyntaxTupleType)
	case *TupleLit:
		return ctSyntaxShape(c, n, ctSyntaxTupleLit)
	case *LambdaExpr:
		return ctSyntaxShape(c, n, ctSyntaxLambdaExpr)
	case *ConstIfStmt:
		return ctSyntaxShape(c, n, ctSyntaxConstIfStmt)
	case *ConstSwitchStmt:
		return ctSyntaxShape(c, n, ctSyntaxConstSwitchStmt)
	case *ConstForStmt:
		return ctSyntaxShape(c, n, ctSyntaxConstForStmt)
	case *FieldProjExpr:
		return ctSyntaxShape(c, n, ctSyntaxFieldProjExpr)
	case *GroupStmt:
		return ctSyntaxShape(c, n, ctSyntaxGroupStmt)
	case *GroupChild:
		return ctSyntaxShape(c, n, ctSyntaxGroupChild)
	}
	panic(fmt.Sprintf("unsupported comptime syntax node %T", node))
}

func ctSyntaxShape[T any, P interface {
	*T
	Node
}](c *ctSyntaxCodec, node P, tag int) Node {
	if c.w != nil {
		c.w.Len(tag)
		return node
	}
	copy := *node
	return P(&copy)
}

func ctSyntaxNew[T any, P interface {
	*T
	Node
}]() Node {
	return P(new(T))
}

var ctSyntaxNodes = [...]func() Node{
	ctSyntaxImportDecl:      ctSyntaxNew[ImportDecl, *ImportDecl],
	ctSyntaxConstDecl:       ctSyntaxNew[ConstDecl, *ConstDecl],
	ctSyntaxTypeDecl:        ctSyntaxNew[TypeDecl, *TypeDecl],
	ctSyntaxVarDecl:         ctSyntaxNew[VarDecl, *VarDecl],
	ctSyntaxFuncDecl:        ctSyntaxNew[FuncDecl, *FuncDecl],
	ctSyntaxName:            ctSyntaxNew[Name, *Name],
	ctSyntaxBasicLit:        ctSyntaxNew[BasicLit, *BasicLit],
	ctSyntaxCompositeLit:    ctSyntaxNew[CompositeLit, *CompositeLit],
	ctSyntaxKeyValueExpr:    ctSyntaxNew[KeyValueExpr, *KeyValueExpr],
	ctSyntaxFuncLit:         ctSyntaxNew[FuncLit, *FuncLit],
	ctSyntaxParenExpr:       ctSyntaxNew[ParenExpr, *ParenExpr],
	ctSyntaxSelectorExpr:    ctSyntaxNew[SelectorExpr, *SelectorExpr],
	ctSyntaxIndexExpr:       ctSyntaxNew[IndexExpr, *IndexExpr],
	ctSyntaxSliceExpr:       ctSyntaxNew[SliceExpr, *SliceExpr],
	ctSyntaxAssertExpr:      ctSyntaxNew[AssertExpr, *AssertExpr],
	ctSyntaxTypeSwitchGuard: ctSyntaxNew[TypeSwitchGuard, *TypeSwitchGuard],
	ctSyntaxOperation:       ctSyntaxNew[Operation, *Operation],
	ctSyntaxCallExpr:        ctSyntaxNew[CallExpr, *CallExpr],
	ctSyntaxListExpr:        ctSyntaxNew[ListExpr, *ListExpr],
	ctSyntaxArrayType:       ctSyntaxNew[ArrayType, *ArrayType],
	ctSyntaxSliceType:       ctSyntaxNew[SliceType, *SliceType],
	ctSyntaxDotsType:        ctSyntaxNew[DotsType, *DotsType],
	ctSyntaxStructType:      ctSyntaxNew[StructType, *StructType],
	ctSyntaxField:           ctSyntaxNew[Field, *Field],
	ctSyntaxInterfaceType:   ctSyntaxNew[InterfaceType, *InterfaceType],
	ctSyntaxFuncType:        ctSyntaxNew[FuncType, *FuncType],
	ctSyntaxMapType:         ctSyntaxNew[MapType, *MapType],
	ctSyntaxChanType:        ctSyntaxNew[ChanType, *ChanType],
	ctSyntaxEmptyStmt:       ctSyntaxNew[EmptyStmt, *EmptyStmt],
	ctSyntaxLabeledStmt:     ctSyntaxNew[LabeledStmt, *LabeledStmt],
	ctSyntaxBlockStmt:       ctSyntaxNew[BlockStmt, *BlockStmt],
	ctSyntaxExprStmt:        ctSyntaxNew[ExprStmt, *ExprStmt],
	ctSyntaxSendStmt:        ctSyntaxNew[SendStmt, *SendStmt],
	ctSyntaxDeclStmt:        ctSyntaxNew[DeclStmt, *DeclStmt],
	ctSyntaxAssignStmt:      ctSyntaxNew[AssignStmt, *AssignStmt],
	ctSyntaxBranchStmt:      ctSyntaxNew[BranchStmt, *BranchStmt],
	ctSyntaxCallStmt:        ctSyntaxNew[CallStmt, *CallStmt],
	ctSyntaxReturnStmt:      ctSyntaxNew[ReturnStmt, *ReturnStmt],
	ctSyntaxIfStmt:          ctSyntaxNew[IfStmt, *IfStmt],
	ctSyntaxForStmt:         ctSyntaxNew[ForStmt, *ForStmt],
	ctSyntaxSwitchStmt:      ctSyntaxNew[SwitchStmt, *SwitchStmt],
	ctSyntaxSelectStmt:      ctSyntaxNew[SelectStmt, *SelectStmt],
	ctSyntaxRangeClause:     ctSyntaxNew[RangeClause, *RangeClause],
	ctSyntaxCaseClause:      ctSyntaxNew[CaseClause, *CaseClause],
	ctSyntaxCommClause:      ctSyntaxNew[CommClause, *CommClause],
	ctSyntaxTryExpr:         ctSyntaxNew[TryExpr, *TryExpr],
	ctSyntaxErrdeferStmt:    ctSyntaxNew[ErrdeferStmt, *ErrdeferStmt],
	ctSyntaxSwitchExpr:      ctSyntaxNew[SwitchExpr, *SwitchExpr],
	ctSyntaxArmClause:       ctSyntaxNew[ArmClause, *ArmClause],
	ctSyntaxAssertedStruct:  ctSyntaxNew[AssertedStruct, *AssertedStruct],
	ctSyntaxPropertyType:    ctSyntaxNew[PropertyType, *PropertyType],
	ctSyntaxOptionType:      ctSyntaxNew[OptionType, *OptionType],
	ctSyntaxCoalesceExpr:    ctSyntaxNew[CoalesceExpr, *CoalesceExpr],
	ctSyntaxOptSelectExpr:   ctSyntaxNew[OptSelectExpr, *OptSelectExpr],
	ctSyntaxConstType:       ctSyntaxNew[ConstType, *ConstType],
	ctSyntaxConstFuncDecl:   ctSyntaxNew[ConstFuncDecl, *ConstFuncDecl],
	ctSyntaxYieldStmt:       ctSyntaxNew[YieldStmt, *YieldStmt],
	ctSyntaxScopeDeferStmt:  ctSyntaxNew[ScopeDeferStmt, *ScopeDeferStmt],
	ctSyntaxFieldDefault:    ctSyntaxNew[FieldDefault, *FieldDefault],
	ctSyntaxSpreadExpr:      ctSyntaxNew[SpreadExpr, *SpreadExpr],
	ctSyntaxInterpExpr:      ctSyntaxNew[InterpExpr, *InterpExpr],
	ctSyntaxTupleType:       ctSyntaxNew[TupleType, *TupleType],
	ctSyntaxTupleLit:        ctSyntaxNew[TupleLit, *TupleLit],
	ctSyntaxLambdaExpr:      ctSyntaxNew[LambdaExpr, *LambdaExpr],
	ctSyntaxConstIfStmt:     ctSyntaxNew[ConstIfStmt, *ConstIfStmt],
	ctSyntaxConstSwitchStmt: ctSyntaxNew[ConstSwitchStmt, *ConstSwitchStmt],
	ctSyntaxConstForStmt:    ctSyntaxNew[ConstForStmt, *ConstForStmt],
	ctSyntaxFieldProjExpr:   ctSyntaxNew[FieldProjExpr, *FieldProjExpr],
	ctSyntaxGroupStmt:       ctSyntaxNew[GroupStmt, *GroupStmt],
	ctSyntaxGroupChild:      ctSyntaxNew[GroupChild, *GroupChild],
}

func (c *ctSyntaxCodec) nodeFields(node Node) {
	switch n := node.(type) {
	case *ImportDecl:
		c.group(&n.Group)
		c.fields.Pragma(&n.Pragma)
		ctSyntaxNode(c, &n.LocalPkgName)
		ctSyntaxNode(c, &n.Path)
	case *ConstDecl:
		c.group(&n.Group)
		c.fields.Pragma(&n.Pragma)
		ctSyntaxSlice(c, &n.NameList, ctSyntaxNode[*Name])
		ctSyntaxNode(c, &n.Type)
		ctSyntaxNode(c, &n.Values)
	case *TypeDecl:
		c.group(&n.Group)
		c.fields.Pragma(&n.Pragma)
		ctSyntaxNode(c, &n.Name)
		ctSyntaxSlice(c, &n.TParamList, ctSyntaxNode[*Field])
		c.boolean(&n.Alias)
		ctSyntaxNode(c, &n.Type)
	case *VarDecl:
		c.group(&n.Group)
		c.fields.Pragma(&n.Pragma)
		ctSyntaxSlice(c, &n.NameList, ctSyntaxNode[*Name])
		ctSyntaxNode(c, &n.Type)
		ctSyntaxNode(c, &n.Values)
	case *FuncDecl:
		c.fields.Pragma(&n.Pragma)
		ctSyntaxNode(c, &n.Recv)
		ctSyntaxNode(c, &n.Name)
		ctSyntaxSlice(c, &n.TParamList, ctSyntaxNode[*Field])
		ctSyntaxNode(c, &n.Type)
		ctSyntaxNode(c, &n.Body)
	case *Name:
		c.text(&n.Value)
	case *BasicLit:
		c.text(&n.Value)
		ctSyntaxNumber(c, &n.Kind)
		c.boolean(&n.Bad)
	case *CompositeLit:
		ctSyntaxNode(c, &n.Type)
		ctSyntaxSlice(c, &n.ElemList, ctSyntaxNode[Expr])
		ctSyntaxNumber(c, &n.NKeys)
		c.fields.Pos(&n.Rbrace)
	case *KeyValueExpr:
		ctSyntaxNode(c, &n.Key)
		ctSyntaxNode(c, &n.Value)
	case *FuncLit:
		ctSyntaxNode(c, &n.Type)
		ctSyntaxNode(c, &n.Body)
	case *ParenExpr:
		ctSyntaxNode(c, &n.X)
	case *SelectorExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Sel)
	case *IndexExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Index)
		c.boolean(&n.LogosTypeArgs)
	case *SliceExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Index[0])
		ctSyntaxNode(c, &n.Index[1])
		ctSyntaxNode(c, &n.Index[2])
		c.boolean(&n.Full)
	case *AssertExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Type)
	case *TypeSwitchGuard:
		ctSyntaxNode(c, &n.Lhs)
		ctSyntaxNode(c, &n.X)
	case *Operation:
		ctSyntaxNumber(c, &n.Op)
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Y)
	case *CallExpr:
		ctSyntaxNode(c, &n.Fun)
		ctSyntaxSlice(c, &n.ArgList, ctSyntaxNode[Expr])
		c.boolean(&n.HasDots)
	case *ListExpr:
		ctSyntaxSlice(c, &n.ElemList, ctSyntaxNode[Expr])
	case *ArrayType:
		ctSyntaxNode(c, &n.Len)
		ctSyntaxNode(c, &n.Elem)
	case *SliceType:
		ctSyntaxNode(c, &n.Elem)
	case *DotsType:
		ctSyntaxNode(c, &n.Elem)
	case *StructType:
		ctSyntaxSlice(c, &n.FieldList, ctSyntaxNode[*Field])
		ctSyntaxSlice(c, &n.TagList, ctSyntaxNode[*BasicLit])
	case *Field:
		ctSyntaxNode(c, &n.Name)
		ctSyntaxNode(c, &n.Type)
		c.boolean(&n.Const)
	case *InterfaceType:
		ctSyntaxSlice(c, &n.MethodList, ctSyntaxNode[*Field])
	case *FuncType:
		ctSyntaxSlice(c, &n.ParamList, ctSyntaxNode[*Field])
		ctSyntaxSlice(c, &n.ResultList, ctSyntaxNode[*Field])
	case *MapType:
		ctSyntaxNode(c, &n.Key)
		ctSyntaxNode(c, &n.Value)
	case *ChanType:
		ctSyntaxNumber(c, &n.Dir)
		ctSyntaxNode(c, &n.Elem)
	case *EmptyStmt:
	case *LabeledStmt:
		ctSyntaxNode(c, &n.Label)
		ctSyntaxNode(c, &n.Stmt)
	case *BlockStmt:
		ctSyntaxSlice(c, &n.List, ctSyntaxNode[Stmt])
		c.fields.Pos(&n.Rbrace)
	case *ExprStmt:
		ctSyntaxNode(c, &n.X)
	case *SendStmt:
		ctSyntaxNode(c, &n.Chan)
		ctSyntaxNode(c, &n.Value)
	case *DeclStmt:
		ctSyntaxSlice(c, &n.DeclList, ctSyntaxNode[Decl])
	case *AssignStmt:
		ctSyntaxNumber(c, &n.Op)
		ctSyntaxNode(c, &n.Lhs)
		ctSyntaxNode(c, &n.Rhs)
	case *BranchStmt:
		ctSyntaxNumber(c, &n.Tok)
		ctSyntaxNode(c, &n.Label)
		ctSyntaxNode(c, &n.Target)
	case *CallStmt:
		ctSyntaxNumber(c, &n.Tok)
		ctSyntaxNode(c, &n.Call)
		ctSyntaxNode(c, &n.DeferAt)
	case *ReturnStmt:
		ctSyntaxNode(c, &n.Results)
	case *IfStmt:
		ctSyntaxNode(c, &n.Init)
		ctSyntaxNode(c, &n.Cond)
		ctSyntaxNode(c, &n.Then)
		ctSyntaxNode(c, &n.Else)
	case *ForStmt:
		ctSyntaxNode(c, &n.Init)
		ctSyntaxNode(c, &n.Cond)
		ctSyntaxNode(c, &n.Post)
		ctSyntaxNode(c, &n.Body)
	case *SwitchStmt:
		ctSyntaxNode(c, &n.Init)
		ctSyntaxNode(c, &n.Tag)
		ctSyntaxSlice(c, &n.Body, ctSyntaxNode[*CaseClause])
		c.fields.Pos(&n.Rbrace)
	case *SelectStmt:
		ctSyntaxSlice(c, &n.Body, ctSyntaxNode[*CommClause])
		c.fields.Pos(&n.Rbrace)
	case *RangeClause:
		ctSyntaxNode(c, &n.Lhs)
		c.boolean(&n.Def)
		ctSyntaxNode(c, &n.X)
	case *CaseClause:
		ctSyntaxNode(c, &n.Cases)
		ctSyntaxSlice(c, &n.Body, ctSyntaxNode[Stmt])
		c.fields.Pos(&n.Colon)
	case *CommClause:
		ctSyntaxNode(c, &n.Comm)
		ctSyntaxSlice(c, &n.Body, ctSyntaxNode[Stmt])
		c.fields.Pos(&n.Colon)
	case *TryExpr:
		ctSyntaxNode(c, &n.X)
	case *ErrdeferStmt:
		ctSyntaxNode(c, &n.Call)
	case *SwitchExpr:
		ctSyntaxNode(c, &n.Init)
		ctSyntaxNode(c, &n.Tag)
		ctSyntaxSlice(c, &n.Arms, ctSyntaxNode[*ArmClause])
		c.fields.Pos(&n.Lbrace)
		c.fields.Pos(&n.Rbrace)
	case *ArmClause:
		ctSyntaxNode(c, &n.Cases)
		c.fields.Pos(&n.Arrow)
		ctSyntaxNode(c, &n.Body)
	case *AssertedStruct:
		ctSyntaxSlice(c, &n.Asserts, ctSyntaxNode[Expr])
		ctSyntaxNode(c, &n.Struct)
		c.fields.Pos(&n.Colon)
		c.fields.Pos(&n.Lbrace)
	case *PropertyType:
		c.boolean(&n.Const)
		ctSyntaxNode(c, &n.Type)
	case *OptionType:
		ctSyntaxNode(c, &n.Elem)
	case *CoalesceExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Y)
	case *OptSelectExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Sel)
	case *ConstType:
		ctSyntaxNode(c, &n.Elem)
	case *ConstFuncDecl:
		c.fields.Pos(&n.ConstPos)
		ctSyntaxNode(c, &n.Func)
	case *YieldStmt:
		ctSyntaxNode(c, &n.Value)
	case *ScopeDeferStmt:
		ctSyntaxNode(c, &n.Call)
	case *FieldDefault:
		ctSyntaxNode(c, &n.Type)
		ctSyntaxNode(c, &n.Default)
	case *SpreadExpr:
		ctSyntaxNode(c, &n.X)
	case *InterpExpr:
		ctSyntaxSlice(c, &n.Parts, (*ctSyntaxCodec).text)
		ctSyntaxSlice(c, &n.Exprs, ctSyntaxNode[Expr])
		ctSyntaxSlice(c, &n.Opens, ctSyntaxPos)
		ctSyntaxSlice(c, &n.Closes, ctSyntaxPos)
		c.text(&n.Raw)
	case *TupleType:
		ctSyntaxSlice(c, &n.Fields, ctSyntaxNode[*Field])
	case *TupleLit:
		ctSyntaxSlice(c, &n.Elems, ctSyntaxNode[Expr])
	case *LambdaExpr:
		ctSyntaxSlice(c, &n.Params, ctSyntaxNode[*Name])
		ctSyntaxNode(c, &n.Body)
		c.fields.Pos(&n.Arrow)
	case *ConstIfStmt:
		ctSyntaxNode(c, &n.Cond)
		ctSyntaxNode(c, &n.Then)
		ctSyntaxNode(c, &n.Else)
	case *ConstSwitchStmt:
		ctSyntaxNode(c, &n.Targ)
		ctSyntaxSlice(c, &n.Cases, ctSyntaxSwitchCase)
		c.fields.Pos(&n.Lbrace)
		c.fields.Pos(&n.Rbrace)
	case *ConstForStmt:
		ctSyntaxNode(c, &n.Index)
		ctSyntaxNode(c, &n.Field)
		ctSyntaxNode(c, &n.Range)
		ctSyntaxNode(c, &n.Body)
	case *FieldProjExpr:
		ctSyntaxNode(c, &n.X)
		ctSyntaxNode(c, &n.Field)
		c.fields.Pos(&n.Rbrack)
	case *GroupStmt:
		ctSyntaxNode(c, &n.Ctx)
		ctSyntaxNode(c, &n.Limit)
		ctSyntaxNode(c, &n.Body)
	case *GroupChild:
		ctSyntaxNode(c, &n.Body)
	default:
		panic(fmt.Sprintf("unsupported comptime syntax node %T", node))
	}
}

func ctSyntaxPos(c *ctSyntaxCodec, pos *Pos) { c.fields.Pos(pos) }

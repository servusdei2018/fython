package compiler

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/servusdei2018/fython/pkg/vm"
)

// Parser parses Python source code into a flat ASTArena.
type Parser struct {
	interpRaw        bool
	src              []byte
	lexer            *Lexer
	arena            *ASTArena
	cur              Token
	peeked           Token
	hasP             bool
	lastTok          Token
	parsingForTarget bool
	stmts            []StmtID
}

// NewParser creates a new Parser with the provided source buffer and arena.
func NewParser(src []byte, arena *ASTArena) *Parser {
	p := &Parser{
		src:   src,
		lexer: NewLexer(src),
		arena: arena,
		stmts: make([]StmtID, 0, 128),
	}
	p.advance()
	return p
}

// Reset resets the Parser with a new or reused source buffer without reallocation.
func (p *Parser) Reset(src []byte) {
	p.src = src
	p.hasP = false
	p.parsingForTarget = false
	p.stmts = p.stmts[:0]
	p.lexer.Reset(src)
	p.advance()
}

func (p *Parser) advance() Token {
	prev := p.cur
	p.lastTok = prev
	if p.hasP {
		p.cur = p.peeked
		p.hasP = false
	} else {
		p.cur = p.lexer.NextToken()
	}
	return prev
}

func (p *Parser) peek() Token {
	if !p.hasP {
		p.peeked = p.lexer.NextToken()
		p.hasP = true
	}
	return p.peeked
}

func (p *Parser) match(t TokenType) bool {
	if p.cur.Type == t {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) expect(t TokenType) (Token, error) {
	if p.cur.Type == t {
		return p.advance(), nil
	}
	return p.cur, fmt.Errorf("SyntaxError: expected %s, got %s at line %d, col %d", t, p.cur.Type, p.cur.Line, p.cur.Col)
}

func isIdentToken(t TokenType) bool {
	return t == TOKEN_IDENT || t == TOKEN_TYPE || t == TOKEN_MATCH || t == TOKEN_CASE
}

func isAttrToken(t TokenType) bool {
	return t == TOKEN_IDENT || (t >= TOKEN_DEF && t <= TOKEN_DEL)
}

func (p *Parser) expectIdent() (Token, error) {
	if isIdentToken(p.cur.Type) {
		return p.advance(), nil
	}
	return p.cur, fmt.Errorf("SyntaxError: expected IDENT, got %s at line %d, col %d", p.cur.Type, p.cur.Line, p.cur.Col)
}

func (p *Parser) expectAttr() (Token, error) {
	if isAttrToken(p.cur.Type) {
		return p.advance(), nil
	}
	return p.cur, fmt.Errorf("SyntaxError: expected IDENT, got %s at line %d, col %d", p.cur.Type, p.cur.Line, p.cur.Col)
}

func (p *Parser) tokenText(tok Token) string {
	return string(p.src[tok.Start:tok.End])
}

// ParseModule parses the entire source buffer into a top-level slice of StmtIDs.
func (p *Parser) ParseModule() ([]StmtID, error) {
	p.stmts = p.stmts[:0]
	for p.cur.Type != TOKEN_EOF {
		if p.cur.Type == TOKEN_NEWLINE {
			p.advance()
			continue
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		if stmt != StmtNone {
			p.stmts = append(p.stmts, stmt)
		}
	}
	return p.stmts, nil
}

func (p *Parser) parseStmt() (StmtID, error) {
	switch p.cur.Type {
	case TOKEN_DEF:
		return p.parseFuncDef()
	case TOKEN_CLASS:
		return p.parseClassDef()
	case TOKEN_RETURN:
		return p.parseReturnStmt()
	case TOKEN_IF:
		return p.parseIfStmt()
	case TOKEN_WHILE:
		return p.parseWhileStmt()
	case TOKEN_FOR:
		return p.parseForStmt()
	case TOKEN_TRY:
		return p.parseTryStmt()
	case TOKEN_RAISE:
		return p.parseRaiseStmt()
	case TOKEN_WITH:
		return p.parseWithStmt()
	case TOKEN_BREAK:
		tok := p.advance()
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindBreak,
			Start: tok.Start,
			End:   tok.End,
		}), nil
	case TOKEN_CONTINUE:
		tok := p.advance()
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindContinue,
			Start: tok.Start,
			End:   tok.End,
		}), nil
	case TOKEN_PASS:
		tok := p.advance()
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindPass,
			Start: tok.Start,
			End:   tok.End,
		}), nil
	case TOKEN_GLOBAL:
		return p.parseGlobalStmt()
	case TOKEN_NONLOCAL:
		return p.parseNonlocalStmt()
	case TOKEN_IMPORT:
		return p.parseImportStmt()
	case TOKEN_FROM:
		return p.parseFromImportStmt()
	case TOKEN_ASYNC:
		return p.parseAsyncStmt()
	case TOKEN_AT:
		return p.parseDecoratedStmt()
	case TOKEN_MATCH:
		if p.isMatchStatement() {
			return p.parseMatchStmt()
		}
		return p.parseSimpleStmt()
	case TOKEN_TYPE:
		if isIdentToken(p.peek().Type) {
			return p.parseTypeStmt()
		}
		return p.parseSimpleStmt()
	case TOKEN_ASSERT:
		return p.parseAssertStmt()
	case TOKEN_DEL:
		return p.parseDeleteStmt()
	case TOKEN_IDENT:
		if p.tokenText(p.cur) == "lazy" && (p.peek().Type == TOKEN_IMPORT || p.peek().Type == TOKEN_FROM) {
			p.advance()
			if p.cur.Type == TOKEN_IMPORT {
				return p.parseImportStmt(true)
			}
			return p.parseFromImportStmt(true)
		}
		return p.parseSimpleStmt()
	default:
		return p.parseSimpleStmt()
	}
}

func (p *Parser) parseSuite() (uint32, error) {
	var stmts []StmtID
	if p.match(TOKEN_NEWLINE) {
		if _, err := p.expect(TOKEN_INDENT); err != nil {
			return 0, err
		}
		for p.cur.Type != TOKEN_DEDENT && p.cur.Type != TOKEN_EOF {
			if p.cur.Type == TOKEN_NEWLINE {
				p.advance()
				continue
			}
			stmt, err := p.parseStmt()
			if err != nil {
				return 0, err
			}
			if stmt != StmtNone {
				stmts = append(stmts, stmt)
			}
		}
		if _, err := p.expect(TOKEN_DEDENT); err != nil {
			return 0, err
		}
	} else {
		// Single-line suite: e.g. "if x < 2: return x" or "try: a; b".
		for {
			stmt, err := p.parseStmt()
			if err != nil {
				return 0, err
			}
			if stmt != StmtNone {
				stmts = append(stmts, stmt)
			}
			if p.lastTok.Type != TOKEN_SEMI || p.cur.Type == TOKEN_NEWLINE || p.cur.Type == TOKEN_EOF {
				break
			}
		}
	}
	return p.arena.AddStmtSlice(stmts), nil
}

func (p *Parser) parseAsyncStmt() (StmtID, error) {
	asyncTok, _ := p.expect(TOKEN_ASYNC)
	switch p.cur.Type {
	case TOKEN_DEF:
		return p.parseFuncDefInternal(true, asyncTok.Start)
	case TOKEN_FOR:
		return p.parseAsyncForStmt(asyncTok.Start)
	case TOKEN_WITH:
		return p.parseAsyncWithStmt(asyncTok.Start)
	default:
		return StmtNone, fmt.Errorf("SyntaxError: expected def, for, or with after async at line %d, col %d", p.cur.Line, p.cur.Col)
	}
}

func (p *Parser) parseDecoratedStmt() (StmtID, error) {
	var decs []ExprID
	for p.cur.Type == TOKEN_AT {
		p.advance() // '@'
		decExpr, err := p.parsePrimary()
		if err != nil {
			return StmtNone, err
		}
		p.match(TOKEN_NEWLINE)
		decs = append(decs, decExpr)
	}
	decSlice := p.arena.AddExprSlice(decs)
	if p.cur.Type == TOKEN_DEF {
		startTok, _ := p.expect(TOKEN_DEF)
		return p.parseFuncDefFull(false, startTok.Start, decSlice)
	}
	if p.cur.Type == TOKEN_ASYNC {
		asyncTok, _ := p.expect(TOKEN_ASYNC)
		return p.parseFuncDefFull(true, asyncTok.Start, decSlice)
	}
	if p.cur.Type == TOKEN_CLASS {
		startTok, _ := p.expect(TOKEN_CLASS)
		return p.parseClassDefFull(startTok.Start, decSlice)
	}
	return StmtNone, fmt.Errorf("SyntaxError: expected def or class after decorator at line %d, col %d", p.cur.Line, p.cur.Col)
}

func (p *Parser) parseFuncDef() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_DEF)
	return p.parseFuncDefFull(false, startTok.Start, ExprSlice{})
}

func (p *Parser) parseFuncDefInternal(isAsync bool, startPos uint32) (StmtID, error) {
	return p.parseFuncDefFull(isAsync, startPos, ExprSlice{})
}

func (p *Parser) parseFuncDefFull(isAsync bool, startPos uint32, decorators ExprSlice) (StmtID, error) {
	if isAsync {
		if _, err := p.expect(TOKEN_DEF); err != nil {
			return StmtNone, err
		}
	}
	nameTok, err := p.expectIdent()
	if err != nil {
		return StmtNone, err
	}
	name := p.tokenText(nameTok)

	// PEP 695: Inline type parameters [T, *Ts, **P]
	var typeParams []ASTTypeParam
	if p.cur.Type == TOKEN_LBRACKET {
		typeParams, err = p.parseTypeParams()
		if err != nil {
			return StmtNone, err
		}
	}

	if _, err := p.expect(TOKEN_LPAREN); err != nil {
		return StmtNone, err
	}

	var paramNames []string
	var defaults []ExprID
	var kwDefaults []ExprID
	var kwDefaultNames []string
	var paramTypes []ExprID
	posOnlyCount := 0
	kwOnlyCount := 0
	inKWOnly := false

	for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
		// Positional-only separator
		if p.match(TOKEN_SLASH) {
			posOnlyCount = len(paramNames)
			if !p.match(TOKEN_COMMA) {
				break
			}
			continue
		}
		// Keyword-only separator or *args
		if p.match(TOKEN_STAR) {
			inKWOnly = true
			if isIdentToken(p.cur.Type) {
				paramTok, err := p.expectIdent()
				if err != nil {
					return StmtNone, err
				}
				paramNames = append(paramNames, "*"+p.tokenText(paramTok))
				if p.match(TOKEN_COLON) {
					ann, err := p.parseTernary()
					if err != nil {
						return StmtNone, err
					}
					paramTypes = append(paramTypes, ann)
				}
			}
			if !p.match(TOKEN_COMMA) {
				break
			}
			continue
		}
		// **kwargs
		if p.match(TOKEN_STARSTAR) {
			paramTok, err := p.expectIdent()
			if err != nil {
				return StmtNone, err
			}
			paramNames = append(paramNames, "**"+p.tokenText(paramTok))
			if p.match(TOKEN_COLON) {
				ann, err := p.parseTernary()
				if err != nil {
					return StmtNone, err
				}
				paramTypes = append(paramTypes, ann)
			}
			if !p.match(TOKEN_COMMA) {
				break
			}
			continue
		}

		paramTok, err := p.expectIdent()
		if err != nil {
			return StmtNone, err
		}
		paramNames = append(paramNames, p.tokenText(paramTok))
		if inKWOnly {
			kwOnlyCount++
		}

		// Optional type annotation: param: Type
		if p.match(TOKEN_COLON) {
			ann, err := p.parseTernary()
			if err != nil {
				return StmtNone, err
			}
			paramTypes = append(paramTypes, ann)
		}

		// Optional default value: = default
		if p.match(TOKEN_ASSIGN) {
			defExpr, err := p.parseTernary()
			if err != nil {
				return StmtNone, err
			}
			if inKWOnly {
				kwDefaults = append(kwDefaults, defExpr)
				kwDefaultNames = append(kwDefaultNames, p.tokenText(paramTok))
			} else {
				defaults = append(defaults, defExpr)
			}
		}
		if !p.match(TOKEN_COMMA) {
			break
		}
	}

	if _, err := p.expect(TOKEN_RPAREN); err != nil {
		return StmtNone, err
	}

	// Optional return annotation: -> Ret
	returnType := ExprNone
	if p.match(TOKEN_ARROW) {
		returnType, err = p.parseTernary()
		if err != nil {
			return StmtNone, err
		}
	}

	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	bodySlice, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	fnIdx := uint32(len(p.arena.FuncDefs))
	p.arena.FuncDefs = append(p.arena.FuncDefs, ASTFuncDef{
		Name:           name,
		ParamNames:     p.arena.AddIdentSlice(paramNames),
		Defaults:       p.arena.AddExprSlice(defaults),
		KWDefaults:     p.arena.AddExprSlice(kwDefaults),
		KWDefaultNames: p.arena.AddIdentSlice(kwDefaultNames),
		Body:           p.arena.StmtSlices[bodySlice],
		IsAsync:        isAsync,
		TypeParams:     p.arena.AddTypeParamSlice(typeParams),
		Decorators:     decorators,
		Returns:        returnType,
		ParamTypes:     p.arena.AddExprSlice(paramTypes),
		PosOnlyCount:   uint16(posOnlyCount),
		KWOnlyCount:    uint16(kwOnlyCount),
	})

	kind := StmtKindFuncDef
	if isAsync {
		kind = StmtKindAsyncFuncDef
	}

	return p.arena.AllocStmt(ASTStmt{
		Kind:  kind,
		Start: startPos,
		End:   p.arena.GetStmtSlice(bodySlice)[len(p.arena.GetStmtSlice(bodySlice))-1].endInArena(p.arena),
		Extra: fnIdx,
	}), nil
}

func (p *Parser) parseClassDef() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_CLASS)
	return p.parseClassDefFull(startTok.Start, ExprSlice{})
}

func (p *Parser) parseClassDefFull(startPos uint32, decorators ExprSlice) (StmtID, error) {
	nameTok, err := p.expectIdent()
	if err != nil {
		return StmtNone, err
	}
	name := p.tokenText(nameTok)

	// PEP 695: Inline type parameters [T]
	var typeParams []ASTTypeParam
	if p.cur.Type == TOKEN_LBRACKET {
		typeParams, err = p.parseTypeParams()
		if err != nil {
			return StmtNone, err
		}
	}

	var bases []ExprID
	var keywords []ExprID
	var kwNames []string
	var kwArgs []ExprID
	if p.match(TOKEN_LPAREN) {
		for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
			// Check for keyword arg: name = expr
			switch {
			case isIdentToken(p.cur.Type) && p.peek().Type == TOKEN_ASSIGN:
				kwTok, err := p.expectIdent()
				if err != nil {
					return StmtNone, err
				}
				kwName := p.tokenText(kwTok)
				p.advance() // =
				kwVal, err := p.parseTernary()
				if err != nil {
					return StmtNone, err
				}
				keywords = append(keywords, kwVal)
				kwNames = append(kwNames, kwName)
				kwArgs = append(kwArgs, kwVal)
			case p.cur.Type == TOKEN_STARSTAR:
				p.advance() // **
				kwVal, err := p.parseTernary()
				if err != nil {
					return StmtNone, err
				}
				keywords = append(keywords, kwVal)
				kwNames = append(kwNames, "") // empty name indicates **unpack
				kwArgs = append(kwArgs, kwVal)
			case p.cur.Type == TOKEN_STAR:
				startTok := p.cur
				p.advance() // *
				starVal, err := p.parseTernary()
				if err != nil {
					return StmtNone, err
				}
				starredExpr := p.arena.AllocExpr(ASTExpr{
					Kind:  ExprKindStarred,
					Left:  starVal,
					Start: startTok.Start,
				})
				bases = append(bases, starredExpr)
			default:
				baseExpr, err := p.parseTernary()
				if err != nil {
					return StmtNone, err
				}
				bases = append(bases, baseExpr)
			}
			if !p.match(TOKEN_COMMA) {
				break
			}
		}
		if _, err := p.expect(TOKEN_RPAREN); err != nil {
			return StmtNone, err
		}
	}

	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	bodySlice, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	clsIdx := uint32(len(p.arena.ClassDefs))
	p.arena.ClassDefs = append(p.arena.ClassDefs, ASTClassDef{
		Name:       name,
		Bases:      p.arena.AddExprSlice(bases),
		Body:       p.arena.StmtSlices[bodySlice],
		TypeParams: p.arena.AddTypeParamSlice(typeParams),
		Decorators: decorators,
		Keywords:   p.arena.AddExprSlice(keywords),
		KWNames:    p.arena.AddIdentSlice(kwNames),
		KWArgs:     p.arena.AddExprSlice(kwArgs),
	})

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindClassDef,
		Start: startPos,
		End:   p.arena.GetStmtSlice(bodySlice)[len(p.arena.GetStmtSlice(bodySlice))-1].endInArena(p.arena),
		Extra: clsIdx,
	}), nil
}

func (p *Parser) parseReturnStmt() (StmtID, error) {
	tok, _ := p.expect(TOKEN_RETURN)
	var val ExprID
	if p.cur.Type != TOKEN_NEWLINE && p.cur.Type != TOKEN_SEMI && p.cur.Type != TOKEN_EOF {
		var err error
		val, err = p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindReturn,
		Start: tok.Start,
		End:   tok.End,
		Expr1: val,
	}), nil
}

func (p *Parser) parseIfStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_IF)
	cond, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	thenBody, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	var elifs []ASTElif
	var elseBody uint32

	for p.cur.Type == TOKEN_ELIF {
		p.advance()
		elifCond, err := p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elifBody, err := p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
		elifs = append(elifs, ASTElif{
			Cond: elifCond,
			Body: p.arena.StmtSlices[elifBody],
		})
	}

	if p.match(TOKEN_ELSE) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elseBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	elifStart := uint32(len(p.arena.Elifs))
	p.arena.Elifs = append(p.arena.Elifs, elifs...)

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindIf,
		Start: startTok.Start,
		Expr1: cond,
		Body:  thenBody,
		Else:  elseBody,
		Extra: (elifStart << 16) | uint32(len(elifs)),
	}), nil
}

func (p *Parser) parseWhileStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_WHILE)
	cond, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	var elseBody uint32
	if p.match(TOKEN_ELSE) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elseBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindWhile,
		Start: startTok.Start,
		Expr1: cond,
		Body:  body,
		Else:  elseBody,
	}), nil
}

func (p *Parser) parseForStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_FOR)
	p.parsingForTarget = true
	target, err := p.parseExpr()
	p.parsingForTarget = false
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_IN); err != nil {
		return StmtNone, err
	}
	iter, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	var elseBody uint32
	if p.match(TOKEN_ELSE) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elseBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindFor,
		Start: startTok.Start,
		Expr1: target,
		Expr2: iter,
		Body:  body,
		Else:  elseBody,
	}), nil
}

func (p *Parser) parseAsyncForStmt(startPos uint32) (StmtID, error) {
	if _, err := p.expect(TOKEN_FOR); err != nil {
		return StmtNone, err
	}
	p.parsingForTarget = true
	target, err := p.parseExpr()
	p.parsingForTarget = false
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_IN); err != nil {
		return StmtNone, err
	}
	iter, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}

	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	var elseBody uint32
	if p.match(TOKEN_ELSE) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elseBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindAsyncFor,
		Start: startPos,
		Expr1: target,
		Expr2: iter,
		Body:  body,
		Else:  elseBody,
	}), nil
}

func (p *Parser) parseTryStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_TRY)
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}
	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}

	var handlers []ASTExceptHandler
	for p.cur.Type == TOKEN_EXCEPT {
		p.advance() // 'except'
		isStar := false
		if p.match(TOKEN_STAR) {
			isStar = true
		}
		var excType ExprID
		var excName string
		if p.cur.Type != TOKEN_COLON {
			var parseErr error
			excType, parseErr = p.parseExpr()
			if parseErr != nil {
				return StmtNone, parseErr
			}
			if p.cur.Type == TOKEN_AS {
				p.advance() // 'as'
				nameTok, err := p.expectIdent()
				if err != nil {
					return StmtNone, err
				}
				excName = string(p.src[nameTok.Start:nameTok.End])
			}
		}
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		hBody, err := p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
		handlers = append(handlers, ASTExceptHandler{
			Type:   excType,
			Name:   excName,
			Body:   p.arena.StmtSlices[hBody],
			IsStar: isStar,
		})
	}

	var elseBody uint32
	if p.match(TOKEN_ELSE) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		elseBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	var finBody uint32
	if p.match(TOKEN_FINALLY) {
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		finBody, err = p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
	}

	if len(handlers) == 0 && finBody == 0 {
		return StmtNone, errors.New("SyntaxError: expected 'except' or 'finally' block")
	}

	exceptSlice := p.arena.AddExceptSlice(handlers)
	var elseSlice StmtSlice
	if elseBody != 0 {
		elseSlice = p.arena.StmtSlices[elseBody]
	}
	var finSlice StmtSlice
	if finBody != 0 {
		finSlice = p.arena.StmtSlices[finBody]
	}

	tryIdx := p.arena.AllocTry(ASTTry{
		Body:     p.arena.StmtSlices[body],
		Handlers: exceptSlice,
		Else:     elseSlice,
		Finally:  finSlice,
	})

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindTry,
		Start: startTok.Start,
		Extra: tryIdx,
	}), nil
}

func (p *Parser) parseRaiseStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_RAISE)
	var exc ExprID
	var from ExprID
	if p.cur.Type != TOKEN_NEWLINE && p.cur.Type != TOKEN_SEMI && p.cur.Type != TOKEN_EOF {
		var err error
		exc, err = p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		if p.cur.Type == TOKEN_FROM || (p.cur.Type == TOKEN_IDENT && string(p.src[p.cur.Start:p.cur.End]) == "from") {
			p.advance() // 'from'
			from, err = p.parseExpr()
			if err != nil {
				return StmtNone, err
			}
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindRaise,
		Start: startTok.Start,
		Expr1: exc,
		Expr2: from,
	}), nil
}

func (p *Parser) parseWithItems() ([]ASTWithItem, error) {
	grouped := p.match(TOKEN_LPAREN)
	var items []ASTWithItem
	for !grouped || p.cur.Type != TOKEN_RPAREN {
		// Commas separate with-items, so the expression and target must not
		// swallow a following item the way parseExpr's tuple rule would.
		expr, err := p.parseWalrus()
		if err != nil {
			return nil, err
		}
		var target ExprID
		if p.match(TOKEN_AS) {
			target, err = p.parseWalrus()
			if err != nil {
				return nil, err
			}
		}
		items = append(items, ASTWithItem{
			Expr:   expr,
			Target: target,
		})
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	if grouped {
		if _, err := p.expect(TOKEN_RPAREN); err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("SyntaxError: expected context manager expression, got %s at line %d, col %d", p.cur.Type, p.cur.Line, p.cur.Col)
	}
	return items, nil
}

func (p *Parser) parseWithStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_WITH)
	items, err := p.parseWithItems()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}
	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}
	withSlice := p.arena.AddWithSlice(items)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindWith,
		Start: startTok.Start,
		Body:  body,
		Extra: withSlice.Start | (withSlice.Count << 16),
	}), nil
}

func (p *Parser) parseAsyncWithStmt(startPos uint32) (StmtID, error) {
	if _, err := p.expect(TOKEN_WITH); err != nil {
		return StmtNone, err
	}
	items, err := p.parseWithItems()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}
	body, err := p.parseSuite()
	if err != nil {
		return StmtNone, err
	}
	withSlice := p.arena.AddWithSlice(items)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindAsyncWith,
		Start: startPos,
		Body:  body,
		Extra: withSlice.Start | (withSlice.Count << 16),
	}), nil
}

func (p *Parser) parseGlobalStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_GLOBAL)
	var names []string
	for {
		nameTok, err := p.expectIdent()
		if err != nil {
			return StmtNone, err
		}
		names = append(names, p.tokenText(nameTok))
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	p.arena.AddIdentSlice(names)
	sliceIdx := uint32(len(p.arena.IdentSlices) - 1)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindGlobal,
		Start: startTok.Start,
		Extra: sliceIdx,
	}), nil
}

func (p *Parser) parseNonlocalStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_NONLOCAL)
	var names []string
	for {
		nameTok, err := p.expectIdent()
		if err != nil {
			return StmtNone, err
		}
		names = append(names, p.tokenText(nameTok))
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	p.arena.AddIdentSlice(names)
	sliceIdx := uint32(len(p.arena.IdentSlices) - 1)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindNonlocal,
		Start: startTok.Start,
		Extra: sliceIdx,
	}), nil
}

func (p *Parser) parseDottedName() (string, uint32, uint32, error) {
	tok, err := p.expectIdent()
	if err != nil {
		return "", 0, 0, err
	}
	start := tok.Start
	end := tok.End
	var b strings.Builder
	b.WriteString(p.tokenText(tok))
	for p.match(TOKEN_DOT) {
		nextTok, err := p.expectIdent()
		if err != nil {
			return "", 0, 0, err
		}
		end = nextTok.End
		b.WriteByte('.')
		b.WriteString(p.tokenText(nextTok))
	}
	return b.String(), start, end, nil
}

func (p *Parser) parseImportStmt(isLazyOpt ...bool) (StmtID, error) {
	isLazy := len(isLazyOpt) > 0 && isLazyOpt[0]
	startTok, _ := p.expect(TOKEN_IMPORT)
	var aliases []ASTAlias
	for {
		name, _, _, err := p.parseDottedName()
		if err != nil {
			return StmtNone, err
		}
		asName := ""
		if p.match(TOKEN_AS) {
			asTok, err := p.expectIdent()
			if err != nil {
				return StmtNone, err
			}
			asName = p.tokenText(asTok)
		}
		aliases = append(aliases, ASTAlias{Name: name, AsName: asName})
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)

	aliasSlice := p.arena.AddAliasSlice(aliases)
	impIdx := p.arena.AllocImport(ASTImport{Aliases: aliasSlice, IsLazy: isLazy})
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindImport,
		Start: startTok.Start,
		Extra: impIdx,
	}), nil
}

func (p *Parser) parseFromImportStmt(isLazyOpt ...bool) (StmtID, error) {
	isLazy := len(isLazyOpt) > 0 && isLazyOpt[0]
	startTok, _ := p.expect(TOKEN_FROM)
	level := 0
	for p.match(TOKEN_DOT) {
		level++
	}

	module := ""
	if isIdentToken(p.cur.Type) {
		modName, _, _, err := p.parseDottedName()
		if err != nil {
			return StmtNone, err
		}
		module = modName
	}

	if _, err := p.expect(TOKEN_IMPORT); err != nil {
		return StmtNone, err
	}

	if p.match(TOKEN_STAR) {
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		impFromIdx := p.arena.AllocImportFrom(ASTImportFrom{
			Module: module,
			Level:  level,
			IsStar: true,
			IsLazy: isLazy,
		})
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindImportFrom,
			Start: startTok.Start,
			Extra: impFromIdx,
		}), nil
	}

	hasParen := p.match(TOKEN_LPAREN)
	var aliases []ASTAlias

	for {
		if hasParen {
			for p.match(TOKEN_NEWLINE) {
			}
			if p.match(TOKEN_RPAREN) {
				break
			}
		}

		nameTok, err := p.expectIdent()
		if err != nil {
			return StmtNone, err
		}
		name := p.tokenText(nameTok)
		asName := ""
		if p.match(TOKEN_AS) {
			asTok, err := p.expectIdent()
			if err != nil {
				return StmtNone, err
			}
			asName = p.tokenText(asTok)
		}
		aliases = append(aliases, ASTAlias{Name: name, AsName: asName})

		if hasParen {
			for p.match(TOKEN_NEWLINE) {
			}
		}

		if !p.match(TOKEN_COMMA) {
			if hasParen {
				for p.match(TOKEN_NEWLINE) {
				}
				if _, err := p.expect(TOKEN_RPAREN); err != nil {
					return StmtNone, err
				}
			}
			break
		}

		if hasParen {
			for p.match(TOKEN_NEWLINE) {
			}
			if p.match(TOKEN_RPAREN) {
				break
			}
		}
	}

	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)

	aliasSlice := p.arena.AddAliasSlice(aliases)
	impFromIdx := p.arena.AllocImportFrom(ASTImportFrom{
		Module:  module,
		Level:   level,
		Aliases: aliasSlice,
		IsStar:  false,
		IsLazy:  isLazy,
	})
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindImportFrom,
		Start: startTok.Start,
		Extra: impFromIdx,
	}), nil
}

func (p *Parser) parseSimpleStmt() (StmtID, error) {
	expr, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}

	// Annotated assignment: target: Type [= Val]
	if p.match(TOKEN_COLON) {
		ann, err := p.parseTernary()
		if err != nil {
			return StmtNone, err
		}
		val := ExprNone
		if p.match(TOKEN_ASSIGN) {
			val, err = p.parseExpr()
			if err != nil {
				return StmtNone, err
			}
		}
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindAnnAssign,
			Expr1: expr,
			Expr2: val,
			Extra: uint32(ann),
		}), nil
	}

	// Check for assignment: target = expr (and multi-target a = b = c = 1)
	if p.match(TOKEN_ASSIGN) {
		val, err := p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		if p.match(TOKEN_ASSIGN) {
			targets := []ExprID{expr, val}
			val, err = p.parseExpr()
			if err != nil {
				return StmtNone, err
			}
			for p.match(TOKEN_ASSIGN) {
				targets = append(targets, val)
				val, err = p.parseExpr()
				if err != nil {
					return StmtNone, err
				}
			}
			p.match(TOKEN_SEMI)
			p.match(TOKEN_NEWLINE)
			targetSlice := p.arena.AddExprSlice(targets)
			return p.arena.AllocStmt(ASTStmt{
				Kind:  StmtKindAssign,
				Expr1: targets[0],
				Expr2: val,
				Extra: targetSlice.Start | (targetSlice.Count << 16),
			}), nil
		}
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindAssign,
			Expr1: expr,
			Expr2: val,
			Extra: 1 << 16,
		}), nil
	}

	// Augmented assignment: +=, -=, *=, /=, %=, &=, |=, ^=, <<=, >>=, @=, etc.
	if p.isAugAssignOp(p.cur.Type) {
		opTok := p.advance()
		val, err := p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindAugAssign,
			Op:    uint8(opTok.Type),
			Expr1: expr,
			Expr2: val,
		}), nil
	}

	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindExpr,
		Expr1: expr,
	}), nil
}

func (p *Parser) isAugAssignOp(t TokenType) bool {
	return t == TOKEN_PLUSEQ || t == TOKEN_MINUSEQ || t == TOKEN_STAREQ ||
		t == TOKEN_SLASHEQ || t == TOKEN_SLASHSLASHEQ || t == TOKEN_PERCENTEQ || t == TOKEN_STARSTAREQ ||
		t == TOKEN_AMPEQ || t == TOKEN_PIPEEQ || t == TOKEN_CARETEQ ||
		t == TOKEN_LSHIFTEQ || t == TOKEN_RSHIFTEQ || t == TOKEN_ATEQ
}

func (s StmtID) endInArena(arena *ASTArena) uint32 {
	stmt := arena.GetStmt(s)
	if stmt == nil {
		return 0
	}
	return stmt.End
}

func (p *Parser) isMatchStatement() bool {
	next := p.peek()
	switch next.Type {
	case TOKEN_ASSIGN, TOKEN_DOT, TOKEN_COMMA, TOKEN_COLON,
		TOKEN_PLUSEQ, TOKEN_MINUSEQ, TOKEN_STAREQ, TOKEN_SLASHEQ, TOKEN_PERCENTEQ,
		TOKEN_AMPEQ, TOKEN_PIPEEQ, TOKEN_CARETEQ, TOKEN_LSHIFTEQ, TOKEN_RSHIFTEQ,
		TOKEN_STARSTAREQ, TOKEN_SLASHSLASHEQ, TOKEN_ATEQ, TOKEN_SEMI, TOKEN_NEWLINE:
		return false
	}
	depth := 0
	inQuote := byte(0)
	for i := int(p.cur.Start); i < len(p.src); i++ {
		b := p.src[i]
		if inQuote != 0 {
			if b == '\\' {
				i++
				continue
			}
			if b == inQuote {
				inQuote = 0
			}
			continue
		}
		if b == '\'' || b == '"' {
			inQuote = b
			continue
		}
		if b == '#' {
			for i < len(p.src) && p.src[i] != '\n' && p.src[i] != '\r' {
				i++
			}
			continue
		}
		if b == '(' || b == '[' || b == '{' {
			depth++
			continue
		}
		if b == ')' || b == ']' || b == '}' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			if b == ':' {
				return true
			}
			if b == '=' {
				return false
			}
			if b == '\n' || b == '\r' || b == ';' {
				return false
			}
		}
	}
	return false
}

func (p *Parser) parseMatchStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_MATCH)
	subject, err := p.parseTernary()
	if err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_NEWLINE); err != nil {
		return StmtNone, err
	}
	if _, err := p.expect(TOKEN_INDENT); err != nil {
		return StmtNone, err
	}
	var cases []ASTMatchCase
	for p.cur.Type == TOKEN_CASE {
		p.advance() // 'case'
		pat, err := p.parsePattern()
		if err != nil {
			return StmtNone, err
		}
		guard := ExprNone
		if p.match(TOKEN_IF) {
			guard, err = p.parseTernary()
			if err != nil {
				return StmtNone, err
			}
		}
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return StmtNone, err
		}
		caseBody, err := p.parseSuite()
		if err != nil {
			return StmtNone, err
		}
		cases = append(cases, ASTMatchCase{
			Pattern: pat,
			Guard:   guard,
			Body:    p.arena.StmtSlices[caseBody],
		})
	}
	if _, err := p.expect(TOKEN_DEDENT); err != nil {
		return StmtNone, err
	}
	caseSlice := p.arena.AddMatchCaseSlice(cases)
	matchIdx := p.arena.AllocMatchStmt(ASTMatchStmt{
		Subject: subject,
		Cases:   caseSlice,
	})
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindMatch,
		Start: startTok.Start,
		Expr1: subject,
		Extra: matchIdx,
	}), nil
}

func (p *Parser) parsePattern() (PatternID, error) {
	pat, err := p.parsePatternAs()
	if err != nil {
		return PatternNone, err
	}
	if p.match(TOKEN_PIPE) {
		pats := []PatternID{pat}
		for {
			nextPat, err := p.parsePatternAs()
			if err != nil {
				return PatternNone, err
			}
			pats = append(pats, nextPat)
			if !p.match(TOKEN_PIPE) {
				break
			}
		}
		patSlice := p.arena.AddPatternSlice(pats)
		return p.arena.AllocPattern(ASTPattern{
			Kind:  PatternKindOr,
			Extra: patSlice.Start,
			Expr1: ExprID(patSlice.Count),
		}), nil
	}
	return pat, nil
}

func (p *Parser) parsePatternAs() (PatternID, error) {
	pat, err := p.parsePatternAtom()
	if err != nil {
		return PatternNone, err
	}
	if p.match(TOKEN_AS) {
		nameTok, err := p.expectIdent()
		if err != nil {
			return PatternNone, err
		}
		return p.arena.AllocPattern(ASTPattern{
			Kind:  PatternKindAs,
			Expr1: ExprID(pat),
			Name:  p.tokenText(nameTok),
		}), nil
	}
	return pat, nil
}

func (p *Parser) parsePatternAtom() (PatternID, error) {
	tok := p.cur
	switch tok.Type {
	case TOKEN_IDENT, TOKEN_TYPE, TOKEN_MATCH, TOKEN_CASE:
		name := p.tokenText(tok)
		p.advance()
		if name == "_" {
			return p.arena.AllocPattern(ASTPattern{
				Kind: PatternKindWildcard,
			}), nil
		}
		if p.cur.Type == TOKEN_DOT {
			expr, err := p.parseDottedNameExpr(name, tok.Start)
			if err != nil {
				return PatternNone, err
			}
			if p.match(TOKEN_LPAREN) {
				return p.parseClassPattern(expr)
			}
			return p.arena.AllocPattern(ASTPattern{
				Kind:  PatternKindValue,
				Expr1: expr,
			}), nil
		}
		if p.match(TOKEN_LPAREN) {
			clsExpr := p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindName,
				Start: tok.Start,
				End:   tok.End,
			})
			return p.parseClassPattern(clsExpr)
		}
		return p.arena.AllocPattern(ASTPattern{
			Kind: PatternKindCapture,
			Name: name,
		}), nil

	case TOKEN_INT, TOKEN_FLOAT, TOKEN_IMAG, TOKEN_STRING:
		atom, err := p.parseAtom()
		if err != nil {
			return PatternNone, err
		}
		return p.arena.AllocPattern(ASTPattern{
			Kind:  PatternKindValue,
			Expr1: atom,
		}), nil

	case TOKEN_MINUS:
		p.advance()
		num, err := p.parseAtom()
		if err != nil {
			return PatternNone, err
		}
		neg := p.arena.AllocExpr(ASTExpr{
			Kind: ExprKindUnary,
			Op:   uint8(TOKEN_MINUS),
			Left: num,
		})
		return p.arena.AllocPattern(ASTPattern{
			Kind:  PatternKindValue,
			Expr1: neg,
		}), nil

	case TOKEN_NONE, TOKEN_TRUE, TOKEN_FALSE:
		atom, err := p.parseAtom()
		if err != nil {
			return PatternNone, err
		}
		return p.arena.AllocPattern(ASTPattern{
			Kind:  PatternKindSingleton,
			Expr1: atom,
		}), nil

	case TOKEN_LBRACKET:
		return p.parseSequencePattern(TOKEN_RBRACKET)

	case TOKEN_LPAREN:
		return p.parseSequencePattern(TOKEN_RPAREN)

	case TOKEN_LBRACE:
		return p.parseMappingPattern()

	default:
		return PatternNone, fmt.Errorf("SyntaxError: invalid pattern at line %d, col %d", tok.Line, tok.Col)
	}
}

func (p *Parser) parseSequencePattern(closeTok TokenType) (PatternID, error) {
	p.advance() // '[' or '('
	var pats []PatternID
	starIdx := int32(-1)
	for p.cur.Type != closeTok && p.cur.Type != TOKEN_EOF {
		if p.match(TOKEN_STAR) {
			if starIdx >= 0 {
				return PatternNone, fmt.Errorf("SyntaxError: multiple starred subpatterns at line %d", p.cur.Line)
			}
			starIdx = int32(len(pats))
			nameTok, err := p.expectIdent()
			if err != nil {
				return PatternNone, err
			}
			capturePat := p.arena.AllocPattern(ASTPattern{
				Kind: PatternKindCapture,
				Name: p.tokenText(nameTok),
			})
			pats = append(pats, capturePat)
		} else {
			sub, err := p.parsePattern()
			if err != nil {
				return PatternNone, err
			}
			pats = append(pats, sub)
		}
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	if _, err := p.expect(closeTok); err != nil {
		return PatternNone, err
	}
	patSlice := p.arena.AddPatternSlice(pats)
	return p.arena.AllocPattern(ASTPattern{
		Kind:    PatternKindSequence,
		Extra:   patSlice.Start,
		Expr1:   ExprID(patSlice.Count),
		StarIdx: starIdx,
	}), nil
}

func (p *Parser) parseMappingPattern() (PatternID, error) {
	p.advance() // '{'
	var keys []ExprID
	var pats []PatternID
	restName := ""
	for p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF {
		if p.match(TOKEN_STARSTAR) {
			nameTok, err := p.expectIdent()
			if err != nil {
				return PatternNone, err
			}
			restName = p.tokenText(nameTok)
			p.match(TOKEN_COMMA)
			break
		}
		keyExpr, err := p.parseTernary()
		if err != nil {
			return PatternNone, err
		}
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return PatternNone, err
		}
		subPat, err := p.parsePattern()
		if err != nil {
			return PatternNone, err
		}
		keys = append(keys, keyExpr)
		pats = append(pats, subPat)
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	if _, err := p.expect(TOKEN_RBRACE); err != nil {
		return PatternNone, err
	}
	keySlice := p.arena.AddExprSlice(keys)
	patSlice := p.arena.AddPatternSlice(pats)
	return p.arena.AllocPattern(ASTPattern{
		Kind:  PatternKindMapping,
		Expr1: ExprID(keySlice.Start),
		Expr2: ExprID(keySlice.Count),
		Extra: patSlice.Start,
		Name:  restName,
	}), nil
}

func (p *Parser) parseClassPattern(clsExpr ExprID) (PatternID, error) {
	var posPats []PatternID
	var kwNames []string
	var kwPats []PatternID
	parsingKW := false
	for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
		if isIdentToken(p.cur.Type) && p.peek().Type == TOKEN_ASSIGN {
			parsingKW = true
			nameTok := p.advance()
			p.advance() // '='
			valPat, err := p.parsePattern()
			if err != nil {
				return PatternNone, err
			}
			kwNames = append(kwNames, p.tokenText(nameTok))
			kwPats = append(kwPats, valPat)
		} else {
			if parsingKW {
				return PatternNone, fmt.Errorf("SyntaxError: positional subpattern follows keyword at line %d", p.cur.Line)
			}
			posPat, err := p.parsePattern()
			if err != nil {
				return PatternNone, err
			}
			posPats = append(posPats, posPat)
		}
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	if _, err := p.expect(TOKEN_RPAREN); err != nil {
		return PatternNone, err
	}
	posSlice := p.arena.AddPatternSlice(posPats)
	kwSlice := p.arena.AddPatternSlice(kwPats)
	kwIdentSlice := p.arena.AddIdentSlice(kwNames)
	return p.arena.AllocPattern(ASTPattern{
		Kind:    PatternKindClass,
		Expr1:   clsExpr,
		Extra:   posSlice.Start,
		Expr2:   ExprID((posSlice.Count << 16) | kwSlice.Count),
		Name:    "",
		StarIdx: int32(kwIdentSlice.Start),
	}), nil
}

func (p *Parser) parseDottedNameExpr(name string, startPos uint32) (ExprID, error) {
	expr := p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindName,
		Start: startPos,
		End:   p.cur.Start,
	})
	for p.match(TOKEN_DOT) {
		attrTok, err := p.expectAttr()
		if err != nil {
			return ExprNone, err
		}
		expr = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindAttribute,
			Left:  expr,
			Start: attrTok.Start,
			End:   attrTok.End,
		})
	}
	return expr, nil
}

func (p *Parser) parseTypeStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_TYPE)
	nameTok, err := p.expectIdent()
	if err != nil {
		return StmtNone, err
	}
	name := p.tokenText(nameTok)
	var typeParams []ASTTypeParam
	if p.cur.Type == TOKEN_LBRACKET {
		typeParams, err = p.parseTypeParams()
		if err != nil {
			return StmtNone, err
		}
	}
	if _, err := p.expect(TOKEN_ASSIGN); err != nil {
		return StmtNone, err
	}
	valExpr, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	taIdx := p.arena.AllocTypeAlias(ASTTypeAlias{
		Name:       name,
		TypeParams: p.arena.AddTypeParamSlice(typeParams),
		Value:      valExpr,
	})
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindTypeAlias,
		Start: startTok.Start,
		Expr1: valExpr,
		Extra: taIdx,
	}), nil
}

func (p *Parser) parseTypeParams() ([]ASTTypeParam, error) {
	if _, err := p.expect(TOKEN_LBRACKET); err != nil {
		return nil, err
	}
	var tps []ASTTypeParam
	for p.cur.Type != TOKEN_RBRACKET && p.cur.Type != TOKEN_EOF {
		var kind TypeParamKind
		switch {
		case p.match(TOKEN_STARSTAR):
			kind = TypeParamParamSpec
		case p.match(TOKEN_STAR):
			kind = TypeParamTypeVarTuple
		default:
			kind = TypeParamTypeVar
		}
		nameTok, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		bound := ExprNone
		if kind == TypeParamTypeVar && p.match(TOKEN_COLON) {
			bound, err = p.parseTernary()
			if err != nil {
				return nil, err
			}
		}
		tps = append(tps, ASTTypeParam{
			Kind:  kind,
			Name:  p.tokenText(nameTok),
			Bound: bound,
		})
		if !p.match(TOKEN_COMMA) {
			break
		}
	}
	if _, err := p.expect(TOKEN_RBRACKET); err != nil {
		return nil, err
	}
	return tps, nil
}

func (p *Parser) parseAssertStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_ASSERT)
	cond, err := p.parseTernary()
	if err != nil {
		return StmtNone, err
	}
	msg := ExprNone
	if p.match(TOKEN_COMMA) {
		msg, err = p.parseTernary()
		if err != nil {
			return StmtNone, err
		}
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindAssert,
		Start: startTok.Start,
		Expr1: cond,
		Expr2: msg,
	}), nil
}

func (p *Parser) parseDeleteStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_DEL)
	target, err := p.parseExpr()
	if err != nil {
		return StmtNone, err
	}
	p.match(TOKEN_SEMI)
	p.match(TOKEN_NEWLINE)
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindDelete,
		Start: startTok.Start,
		Expr1: target,
	}), nil
}

func (p *Parser) parseFString() (ExprID, error) {
	return p.parseInterpolated(false)
}

func (p *Parser) parseTString() (ExprID, error) {
	return p.parseInterpolated(true)
}

func (p *Parser) errMixedTemplate() error {
	return fmt.Errorf("SyntaxError: cannot mix t-string literals with string or f-string literals at line %d, col %d", p.cur.Line, p.cur.Col)
}

func (p *Parser) parseInterpolated(template bool) (ExprID, error) {
	startType := TOKEN_FSTRING_START
	if template {
		startType = TOKEN_TSTRING_START
	}
	startTok, err := p.expect(startType)
	if err != nil {
		return ExprNone, err
	}
	raw := strings.ContainsAny(string(p.src[startTok.Start:startTok.End]), "rR")
	prevRaw := p.interpRaw
	p.interpRaw = raw
	defer func() { p.interpRaw = prevRaw }()
	var pieces []ExprID
	for p.cur.Type != TOKEN_FSTRING_END && p.cur.Type != TOKEN_EOF {
		switch {
		case p.cur.Type == TOKEN_FSTRING_MIDDLE:
			midTok := p.advance()
			pieces = append(pieces, p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindString,
				Op:    p.partOp(),
				Start: midTok.Start,
				End:   midTok.End,
			}))
		case p.match(TOKEN_LBRACE):
			fv, err := p.parseFormattedValue(template)
			if err != nil {
				return ExprNone, err
			}
			pieces = append(pieces, fv)
		default:
			goto donePieces
		}
	}
donePieces:
	if _, err := p.expect(TOKEN_FSTRING_END); err != nil {
		return ExprNone, err
	}
	fsIdx := p.arena.AllocFString(ASTFString{
		Pieces: p.arena.AddExprSlice(pieces),
		Raw:    raw,
	})
	kind := ExprKindJoinedStr
	if template {
		kind = ExprKindTemplateStr
	}
	return p.arena.AllocExpr(ASTExpr{
		Kind:  kind,
		Start: startTok.Start,
		Extra: fsIdx,
	}), nil
}

func (p *Parser) partOp() uint8 {
	if p.interpRaw {
		return 3
	}
	return 1
}

func (p *Parser) parseFormattedValue(template bool) (ExprID, error) {
	exprStart := p.cur.Start
	valExpr, err := p.parseTernary()
	if err != nil {
		return ExprNone, err
	}
	expression := strings.TrimRight(string(p.src[exprStart:p.cur.Start]), " \t")
	debugText := ""
	if p.match(TOKEN_ASSIGN) {
		debugText = string(p.src[exprStart:p.cur.Start])
	}
	conversion := uint8(0)
	if p.match(TOKEN_EXCLAIM) {
		if p.cur.Type == TOKEN_IDENT {
			identTok := p.advance()
			identStr := string(p.src[identTok.Start:identTok.End])
			if len(identStr) > 0 {
				conversion = identStr[0]
			}
		}
	}
	if template && debugText != "" && conversion == 0 {
		conversion = 'r'
	}
	formatSpec := ExprNone
	if p.match(TOKEN_COLON) {
		formatSpec, err = p.parseFormatSpec(template)
		if err != nil {
			return ExprNone, err
		}
	}
	if _, err := p.expect(TOKEN_RBRACE); err != nil {
		return ExprNone, err
	}
	fvIdx := p.arena.AllocFormattedValue(ASTFormattedValue{
		Value:      valExpr,
		Conversion: conversion,
		FormatSpec: formatSpec,
		DebugText:  debugText,
		Expression: expression,
	})
	return p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindFormattedValue,
		Left:  valExpr,
		Right: formatSpec,
		Extra: fvIdx,
	}), nil
}

func (p *Parser) parseFormatSpec(template bool) (ExprID, error) {
	if p.cur.Type == TOKEN_RBRACE || p.cur.Type == TOKEN_EOF || p.cur.Type == TOKEN_FSTRING_END {
		return ExprNone, nil
	}
	origStart := p.cur.Start
	var pieces []ExprID
	literalStart := p.cur.Start
	lastEnd := p.cur.End
	for p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF && p.cur.Type != TOKEN_FSTRING_END {
		if p.cur.Type == TOKEN_LBRACE {
			if p.cur.Start > literalStart {
				pieces = append(pieces, p.arena.AllocExpr(ASTExpr{
					Kind:  ExprKindString,
					Op:    p.partOp(),
					Start: literalStart,
					End:   p.cur.Start,
				}))
			}
			p.advance()
			fv, err := p.parseFormattedValue(template)
			if err != nil {
				return ExprNone, err
			}
			pieces = append(pieces, fv)
			literalStart = p.cur.Start
			lastEnd = p.cur.End
			continue
		}
		lastEnd = p.cur.End
		p.advance()
	}

	if len(pieces) == 0 {
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindString,
			Op:    p.partOp(),
			Start: origStart,
			End:   lastEnd,
		}), nil
	}

	if p.cur.Start > literalStart {
		pieces = append(pieces, p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindString,
			Op:    p.partOp(),
			Start: literalStart,
			End:   p.cur.Start,
		}))
	}

	fsIdx := p.arena.AllocFString(ASTFString{
		Pieces: p.arena.AddExprSlice(pieces),
		Raw:    p.interpRaw,
	})
	return p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindJoinedStr,
		Start: origStart,
		Extra: fsIdx,
	}), nil
}

func (p *Parser) parseLambda() (ExprID, error) {
	startTok, _ := p.expect(TOKEN_LAMBDA)
	var paramNames []string
	var defaults []ExprID
	if p.cur.Type != TOKEN_COLON {
		for p.cur.Type != TOKEN_COLON && p.cur.Type != TOKEN_EOF {
			switch {
			case p.match(TOKEN_STARSTAR):
				pTok, err := p.expectIdent()
				if err != nil {
					return ExprNone, err
				}
				paramNames = append(paramNames, "**"+p.tokenText(pTok))
			case p.match(TOKEN_STAR):
				if isIdentToken(p.cur.Type) {
					pTok, _ := p.expectIdent()
					paramNames = append(paramNames, "*"+p.tokenText(pTok))
				}
			default:
				pTok, err := p.expectIdent()
				if err != nil {
					return ExprNone, err
				}
				paramNames = append(paramNames, p.tokenText(pTok))
				if p.match(TOKEN_ASSIGN) {
					defVal, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					defaults = append(defaults, defVal)
				}
			}
			if !p.match(TOKEN_COMMA) {
				break
			}
		}
	}
	if _, err := p.expect(TOKEN_COLON); err != nil {
		return ExprNone, err
	}
	bodyExpr, err := p.parseTernary()
	if err != nil {
		return ExprNone, err
	}
	retStmt := p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindReturn,
		Expr1: bodyExpr,
	})
	bodySlice := p.arena.AddStmtSlice([]StmtID{retStmt})
	fnIdx := uint32(len(p.arena.FuncDefs))
	p.arena.FuncDefs = append(p.arena.FuncDefs, ASTFuncDef{
		Name:       "<lambda>",
		ParamNames: p.arena.AddIdentSlice(paramNames),
		Defaults:   p.arena.AddExprSlice(defaults),
		Body:       p.arena.StmtSlices[bodySlice],
	})
	return p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindLambda,
		Start: startTok.Start,
		Extra: fnIdx,
	}), nil
}

func (p *Parser) parseWalrus() (ExprID, error) {
	expr, err := p.parseTernary()
	if err != nil {
		return ExprNone, err
	}
	if p.match(TOKEN_WALRUS) {
		val, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindNamedExpr,
			Left:  expr,
			Right: val,
		}), nil
	}
	return expr, nil
}

// Expression Parsing (Pratt Precedence)

func (p *Parser) parseExpr() (ExprID, error) {
	if p.cur.Type == TOKEN_YIELD {
		return p.parseYield()
	}
	if p.cur.Type == TOKEN_LAMBDA {
		return p.parseLambda()
	}
	first, err := p.parseWalrus()
	if err != nil {
		return ExprNone, err
	}
	if p.cur.Type == TOKEN_COMMA {
		var items []ExprID
		items = append(items, first)
		for p.match(TOKEN_COMMA) {
			if p.cur.Type == TOKEN_NEWLINE || p.cur.Type == TOKEN_SEMI || p.cur.Type == TOKEN_EOF ||
				p.cur.Type == TOKEN_ASSIGN || p.cur.Type == TOKEN_RPAREN || p.cur.Type == TOKEN_RBRACKET ||
				p.cur.Type == TOKEN_RBRACE || p.cur.Type == TOKEN_COLON ||
				(p.parsingForTarget && p.cur.Type == TOKEN_IN) {
				break
			}
			item, err := p.parseWalrus()
			if err != nil {
				return ExprNone, err
			}
			items = append(items, item)
		}
		p.arena.AddExprSlice(items)
		sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindTuple,
			Extra: sliceIdx,
			Op:    uint8(len(items)),
		}), nil
	}
	return first, nil
}

func (p *Parser) parseTernary() (ExprID, error) {
	if p.cur.Type == TOKEN_YIELD {
		return p.parseYield()
	}
	if p.cur.Type == TOKEN_LAMBDA {
		return p.parseLambda()
	}
	left, err := p.parseOr()
	if err != nil {
		return ExprNone, err
	}
	if p.match(TOKEN_IF) {
		cond, err := p.parseOr()
		if err != nil {
			return ExprNone, err
		}
		if _, err := p.expect(TOKEN_ELSE); err != nil {
			return ExprNone, err
		}
		elseExpr, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindIfExp,
			Left:  left,
			Right: elseExpr,
			Extra: uint32(cond),
		}), nil
	}
	return left, nil
}

func (p *Parser) parseOr() (ExprID, error) {
	left, err := p.parseAnd()
	if err != nil {
		return ExprNone, err
	}
	for p.match(TOKEN_OR) {
		right, err := p.parseAnd()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_OR),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseAnd() (ExprID, error) {
	left, err := p.parseNot()
	if err != nil {
		return ExprNone, err
	}
	for p.match(TOKEN_AND) {
		right, err := p.parseNot()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_AND),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseNot() (ExprID, error) {
	if p.match(TOKEN_NOT) {
		operand, err := p.parseNot()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind: ExprKindUnary,
			Op:   uint8(TOKEN_NOT),
			Left: operand,
		}), nil
	}
	return p.parseComparison()
}

func (p *Parser) parseComparison() (ExprID, error) {
	left, err := p.parseBitOr()
	if err != nil {
		return ExprNone, err
	}

	type cmpPart struct {
		op    uint8
		right ExprID
	}
	var parts []cmpPart

	for {
		var cmpOp uint8
		matched := true

		switch {
		case p.match(TOKEN_LT):
			cmpOp = vm.CMP_LT
		case p.match(TOKEN_LTE):
			cmpOp = vm.CMP_LTE
		case p.match(TOKEN_EQ):
			cmpOp = vm.CMP_EQ
		case p.match(TOKEN_NEQ):
			cmpOp = vm.CMP_NEQ
		case p.match(TOKEN_GT):
			cmpOp = vm.CMP_GT
		case p.match(TOKEN_GTE):
			cmpOp = vm.CMP_GTE
		case !p.parsingForTarget && p.match(TOKEN_IN):
			cmpOp = vm.CMP_IN
		case !p.parsingForTarget && p.cur.Type == TOKEN_NOT && p.peek().Type == TOKEN_IN:
			p.advance()
			p.advance()
			cmpOp = vm.CMP_NOT_IN
		case p.match(TOKEN_IS):
			if p.match(TOKEN_NOT) {
				cmpOp = vm.CMP_IS_NOT
			} else {
				cmpOp = vm.CMP_IS
			}
		default:
			matched = false
		}

		if !matched {
			break
		}

		right, err := p.parseBitOr()
		if err != nil {
			return ExprNone, err
		}
		parts = append(parts, cmpPart{op: cmpOp, right: right})
	}

	if len(parts) == 0 {
		return left, nil
	}

	if len(parts) == 1 {
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindCompare,
			Op:    parts[0].op,
			Left:  left,
			Right: parts[0].right,
		}), nil
	}

	// Chained comparison: a cmp1 b cmp2 c ... -> (a cmp1 b) and (b cmp2 c) ...
	var result ExprID
	prev := left
	for _, part := range parts {
		cmpExpr := p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindCompare,
			Op:    part.op,
			Left:  prev,
			Right: part.right,
		})
		if result == ExprNone {
			result = cmpExpr
		} else {
			result = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindBinary,
				Op:    uint8(TOKEN_AND),
				Left:  result,
				Right: cmpExpr,
			})
		}
		prev = part.right
	}
	return result, nil
}

func (p *Parser) parseBitOr() (ExprID, error) {
	left, err := p.parseBitXor()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_PIPE {
		p.advance()
		right, err := p.parseBitXor()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_PIPE),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseBitXor() (ExprID, error) {
	left, err := p.parseBitAnd()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_CARET {
		p.advance()
		right, err := p.parseBitAnd()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_CARET),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseBitAnd() (ExprID, error) {
	left, err := p.parseShift()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_AMP {
		p.advance()
		right, err := p.parseShift()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_AMP),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseShift() (ExprID, error) {
	left, err := p.parseAddSub()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_LSHIFT || p.cur.Type == TOKEN_RSHIFT {
		opTok := p.advance()
		right, err := p.parseAddSub()
		if err != nil {
			return ExprNone, err
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(opTok.Type),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseAddSub() (ExprID, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_PLUS || p.cur.Type == TOKEN_MINUS {
		opTok := p.advance()
		right, err := p.parseMulDiv()
		if err != nil {
			return ExprNone, err
		}
		if rn := p.arena.GetExpr(right); rn != nil && rn.Kind == ExprKindStarred {
			return ExprNone, errors.New("SyntaxError: cannot use starred expression here")
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(opTok.Type),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseMulDiv() (ExprID, error) {
	left, err := p.parseUnary()
	if err != nil {
		return ExprNone, err
	}
	for p.cur.Type == TOKEN_STAR || p.cur.Type == TOKEN_SLASH ||
		p.cur.Type == TOKEN_SLASHSLASH || p.cur.Type == TOKEN_PERCENT ||
		p.cur.Type == TOKEN_AT {
		opTok := p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return ExprNone, err
		}
		if rn := p.arena.GetExpr(right); rn != nil && rn.Kind == ExprKindStarred {
			return ExprNone, errors.New("SyntaxError: cannot use starred expression here")
		}
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(opTok.Type),
			Left:  left,
			Right: right,
		})
	}
	return left, nil
}

func (p *Parser) parseUnary() (ExprID, error) {
	if p.cur.Type == TOKEN_AWAIT {
		opTok := p.advance()
		operand, err := p.parseUnary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindAwait,
			Start: opTok.Start,
			Left:  operand,
		}), nil
	}
	if p.cur.Type == TOKEN_PLUS || p.cur.Type == TOKEN_MINUS || p.cur.Type == TOKEN_TILDE {
		opTok := p.advance()
		operand, err := p.parseUnary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind: ExprKindUnary,
			Op:   uint8(opTok.Type),
			Left: operand,
		}), nil
	}
	return p.parsePower()
}

func (p *Parser) parsePower() (ExprID, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return ExprNone, err
	}
	if p.match(TOKEN_STARSTAR) {
		right, err := p.parseUnary() // Right-associative exponentiation
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBinary,
			Op:    uint8(TOKEN_STARSTAR),
			Left:  left,
			Right: right,
		}), nil
	}
	return left, nil
}

func (p *Parser) matchCompStart() (bool, bool) {
	if p.match(TOKEN_FOR) {
		return true, false
	}
	if p.cur.Type == TOKEN_ASYNC && p.peek().Type == TOKEN_FOR {
		p.advance() // async
		p.advance() // for
		return true, true
	}
	return false, false
}

func (p *Parser) parseCompClauses(firstIsAsync bool) (uint32, error) {
	var firstIdx uint32
	var prevIdx uint32
	first := true

	for {
		var isAsync bool
		if first {
			first = false
			isAsync = firstIsAsync
		} else {
			var ok bool
			ok, isAsync = p.matchCompStart()
			if !ok {
				break
			}
		}

		p.parsingForTarget = true
		target, err := p.parseExpr()
		p.parsingForTarget = false
		if err != nil {
			return 0, err
		}
		if _, err := p.expect(TOKEN_IN); err != nil {
			return 0, err
		}
		iter, err := p.parseOr()
		if err != nil {
			return 0, err
		}
		var ifs []ExprID
		for p.match(TOKEN_IF) {
			ifCond, err := p.parseOr()
			if err != nil {
				return 0, err
			}
			ifs = append(ifs, ifCond)
		}
		idx := uint32(len(p.arena.Comps))
		p.arena.Comps = append(p.arena.Comps, ASTComp{
			Target:  target,
			Iter:    iter,
			IfConds: p.arena.AddExprSlice(ifs),
			IsAsync: isAsync,
		})
		if prevIdx != 0 {
			p.arena.Comps[prevIdx-1].Next = idx + 1
		} else {
			firstIdx = idx
		}
		prevIdx = idx + 1
	}
	return firstIdx, nil
}

func (p *Parser) parsePrimary() (ExprID, error) {
	expr, err := p.parseAtom()
	if err != nil {
		return ExprNone, err
	}

	for {
		if p.match(TOKEN_LPAREN) {
			// Call
			var posArgs []ExprID
			var kwNames []string
			var kwArgs []ExprID

			for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
				switch {
				case p.match(TOKEN_STARSTAR):
					kwVal, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					kwNames = append(kwNames, "") // "" indicates **kwargs
					kwArgs = append(kwArgs, kwVal)
				case p.match(TOKEN_STAR):
					arg, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					starred := p.arena.AllocExpr(ASTExpr{
						Kind: ExprKindStarred,
						Left: arg,
					})
					posArgs = append(posArgs, starred)
				case isIdentToken(p.cur.Type) && p.peek().Type == TOKEN_ASSIGN:
					nameTok, _ := p.expectIdent()
					p.advance() // =
					kwVal, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					kwNames = append(kwNames, p.tokenText(nameTok))
					kwArgs = append(kwArgs, kwVal)
				default:
					arg, err := p.parseWalrus()
					if err != nil {
						return ExprNone, err
					}
					if ok, isAsync := p.matchCompStart(); ok {
						compIdx, err := p.parseCompClauses(isAsync)
						if err != nil {
							return ExprNone, err
						}
						arg = p.arena.AllocExpr(ASTExpr{
							Kind:  ExprKindGeneratorExp,
							Left:  arg,
							Extra: compIdx,
						})
					}
					posArgs = append(posArgs, arg)
				}
				if !p.match(TOKEN_COMMA) {
					break
				}
			}
			if _, err := p.expect(TOKEN_RPAREN); err != nil {
				return ExprNone, err
			}

			callIdx := uint32(len(p.arena.Calls))
			p.arena.Calls = append(p.arena.Calls, ASTCall{
				Func:    expr,
				PosArgs: p.arena.AddExprSlice(posArgs),
				KWNames: p.arena.AddIdentSlice(kwNames),
				KWArgs:  p.arena.AddExprSlice(kwArgs),
			})

			expr = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindCall,
				Left:  expr,
				Extra: callIdx,
			})
			continue
		}

		if p.match(TOKEN_DOT) {
			attrTok, err := p.expectAttr()
			if err != nil {
				return ExprNone, err
			}
			expr = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindAttribute,
				Left:  expr,
				Start: attrTok.Start,
				End:   attrTok.End,
			})
			continue
		}

		if p.match(TOKEN_LBRACKET) {
			subExpr, err := p.parseSubscriptItem()
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_RBRACKET); err != nil {
				return ExprNone, err
			}
			expr = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindSubscript,
				Left:  expr,
				Right: subExpr,
			})
			continue
		}

		break
	}

	return expr, nil
}

func (p *Parser) parseSubscriptItem() (ExprID, error) {
	if p.cur.Type == TOKEN_COLON {
		p.advance()
		var stop, step ExprID
		if p.cur.Type == TOKEN_COLON {
			p.advance()
			if p.cur.Type != TOKEN_RBRACKET {
				var err error
				step, err = p.parseExpr()
				if err != nil {
					return ExprNone, err
				}
			}
		} else if p.cur.Type != TOKEN_RBRACKET {
			var err error
			stop, err = p.parseExpr()
			if err != nil {
				return ExprNone, err
			}
			if p.match(TOKEN_COLON) {
				if p.cur.Type != TOKEN_RBRACKET {
					step, err = p.parseExpr()
					if err != nil {
						return ExprNone, err
					}
				}
			}
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindSlice,
			Left:  ExprNone,
			Right: stop,
			Extra: uint32(step),
		}), nil
	}

	first, err := p.parseExpr()
	if err != nil {
		return ExprNone, err
	}

	if p.match(TOKEN_COLON) {
		var stop, step ExprID
		if p.cur.Type == TOKEN_COLON {
			p.advance()
			if p.cur.Type != TOKEN_RBRACKET {
				step, err = p.parseExpr()
				if err != nil {
					return ExprNone, err
				}
			}
		} else if p.cur.Type != TOKEN_RBRACKET {
			stop, err = p.parseExpr()
			if err != nil {
				return ExprNone, err
			}
			if p.match(TOKEN_COLON) {
				if p.cur.Type != TOKEN_RBRACKET {
					step, err = p.parseExpr()
					if err != nil {
						return ExprNone, err
					}
				}
			}
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindSlice,
			Left:  first,
			Right: stop,
			Extra: uint32(step),
		}), nil
	}

	return first, nil
}

func (p *Parser) parseAtom() (ExprID, error) {
	tok := p.cur

	switch tok.Type {
	case TOKEN_INT:
		p.advance()
		raw := p.src[tok.Start:tok.End]
		var val int64
		var err error
		if bytes.IndexByte(raw, '_') >= 0 {
			val, err = strconv.ParseInt(strings.ReplaceAll(string(raw), "_", ""), 0, 64)
		} else {
			val, err = strconv.ParseInt(string(raw), 0, 64)
		}
		if err != nil {
			clean := strings.ReplaceAll(string(raw), "_", "")
			bi, ok := new(big.Int).SetString(clean, 0)
			if !ok {
				return ExprNone, fmt.Errorf("SyntaxError: invalid integer literal %q", string(raw))
			}
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindBigInt,
				Start: tok.Start,
				End:   tok.End,
				Extra: p.arena.AllocBig(bi),
			}), nil
		}
		idx := p.arena.AllocInt(val)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindInt,
			Start: tok.Start,
			End:   tok.End,
			Extra: idx,
		}), nil

	case TOKEN_FLOAT:
		p.advance()
		raw := p.src[tok.Start:tok.End]
		var val float64
		var err error
		if bytes.IndexByte(raw, '_') >= 0 {
			val, err = strconv.ParseFloat(strings.ReplaceAll(string(raw), "_", ""), 64)
		} else {
			val, err = strconv.ParseFloat(string(raw), 64)
		}
		if err != nil {
			return ExprNone, fmt.Errorf("SyntaxError: invalid float literal %q", string(raw))
		}
		idx := p.arena.AllocFloat(val)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindFloat,
			Start: tok.Start,
			End:   tok.End,
			Extra: idx,
		}), nil

	case TOKEN_IMAG:
		p.advance()
		raw := p.src[tok.Start : tok.End-1]
		var val float64
		var err error
		if bytes.IndexByte(raw, '_') >= 0 {
			val, err = strconv.ParseFloat(strings.ReplaceAll(string(raw), "_", ""), 64)
		} else {
			val, err = strconv.ParseFloat(string(raw), 64)
		}
		if err != nil {
			return ExprNone, fmt.Errorf("SyntaxError: invalid imaginary literal %q", string(p.src[tok.Start:tok.End]))
		}
		idx := p.arena.AllocFloat(val)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindImag,
			Start: tok.Start,
			End:   tok.End,
			Extra: idx,
		}), nil

	case TOKEN_STRING:
		p.advance()
		expr := p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindString,
			Start: tok.Start,
			End:   tok.End,
		})
		if p.cur.Type == TOKEN_TSTRING_START {
			return ExprNone, p.errMixedTemplate()
		}
		for p.cur.Type == TOKEN_STRING || p.cur.Type == TOKEN_FSTRING_START {
			var nextExpr ExprID
			var err error
			if p.cur.Type == TOKEN_STRING {
				nxtTok := p.advance()
				nextExpr = p.arena.AllocExpr(ASTExpr{
					Kind:  ExprKindString,
					Start: nxtTok.Start,
					End:   nxtTok.End,
				})
			} else {
				nextExpr, err = p.parseFString()
				if err != nil {
					return ExprNone, err
				}
			}
			expr = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindBinary,
				Op:    uint8(TOKEN_PLUS),
				Left:  expr,
				Right: nextExpr,
			})
		}
		return expr, nil

	case TOKEN_IDENT, TOKEN_TYPE, TOKEN_MATCH, TOKEN_CASE:
		p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindName,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_TRUE:
		p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBool,
			Op:    1,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_FALSE:
		p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindBool,
			Op:    0,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_NONE:
		p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindNoneLit,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_FSTRING_START:
		expr, err := p.parseFString()
		if err != nil {
			return ExprNone, err
		}
		if p.cur.Type == TOKEN_TSTRING_START {
			return ExprNone, p.errMixedTemplate()
		}
		for p.cur.Type == TOKEN_STRING || p.cur.Type == TOKEN_FSTRING_START {
			var nextExpr ExprID
			if p.cur.Type == TOKEN_STRING {
				nxtTok := p.advance()
				nextExpr = p.arena.AllocExpr(ASTExpr{
					Kind:  ExprKindString,
					Start: nxtTok.Start,
					End:   nxtTok.End,
				})
			} else {
				nextExpr, err = p.parseFString()
				if err != nil {
					return ExprNone, err
				}
			}
			expr = p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindBinary,
				Op:    uint8(TOKEN_PLUS),
				Left:  expr,
				Right: nextExpr,
			})
		}
		return expr, nil

	case TOKEN_TSTRING_START:
		expr, err := p.parseTString()
		if err != nil {
			return ExprNone, err
		}
		if p.cur.Type == TOKEN_STRING || p.cur.Type == TOKEN_FSTRING_START || p.cur.Type == TOKEN_TSTRING_START {
			return ExprNone, p.errMixedTemplate()
		}
		return expr, nil

	case TOKEN_ELLIPSIS:
		tok := p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindEllipsis,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_STAR:
		p.advance()
		sub, err := p.parsePrimary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind: ExprKindStarred,
			Left: sub,
		}), nil

	case TOKEN_LPAREN:
		p.advance()
		if p.match(TOKEN_RPAREN) {
			// Empty tuple ()
			p.arena.AddExprSlice(nil)
			sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindTuple,
				Extra: sliceIdx,
			}), nil
		}
		first, err := p.parseWalrus()
		if err != nil {
			return ExprNone, err
		}
		if ok, isAsync := p.matchCompStart(); ok {
			compIdx, err := p.parseCompClauses(isAsync)
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_RPAREN); err != nil {
				return ExprNone, err
			}
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindGeneratorExp,
				Left:  first,
				Extra: compIdx,
			}), nil
		}
		if p.match(TOKEN_COMMA) {
			// Tuple literal (a, ...)
			var items []ExprID
			items = append(items, first)
			for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
				item, err := p.parseWalrus()
				if err != nil {
					return ExprNone, err
				}
				items = append(items, item)
				if !p.match(TOKEN_COMMA) {
					break
				}
			}
			if _, err := p.expect(TOKEN_RPAREN); err != nil {
				return ExprNone, err
			}
			p.arena.AddExprSlice(items)
			sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindTuple,
				Extra: sliceIdx,
				Op:    uint8(len(items)),
			}), nil
		}
		if _, err := p.expect(TOKEN_RPAREN); err != nil {
			return ExprNone, err
		}
		return first, nil

	case TOKEN_LBRACKET:
		p.advance()
		if p.match(TOKEN_RBRACKET) {
			// Empty list []
			p.arena.AddExprSlice(nil)
			sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindList,
				Extra: sliceIdx,
			}), nil
		}
		first, err := p.parseWalrus()
		if err != nil {
			return ExprNone, err
		}
		// Check for list comprehension: [elt for target in iter if cond]
		if ok, isAsync := p.matchCompStart(); ok {
			compIdx, err := p.parseCompClauses(isAsync)
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_RBRACKET); err != nil {
				return ExprNone, err
			}
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindListComp,
				Left:  first,
				Extra: compIdx,
			}), nil
		}

		// Regular list literal [a, b, ...]
		var items []ExprID
		items = append(items, first)
		for p.match(TOKEN_COMMA) && p.cur.Type != TOKEN_RBRACKET && p.cur.Type != TOKEN_EOF {
			item, err := p.parseWalrus()
			if err != nil {
				return ExprNone, err
			}
			items = append(items, item)
		}
		if _, err := p.expect(TOKEN_RBRACKET); err != nil {
			return ExprNone, err
		}
		p.arena.AddExprSlice(items)
		sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindList,
			Extra: sliceIdx,
			Op:    uint8(len(items)),
		}), nil

	case TOKEN_LBRACE:
		p.advance()
		if p.match(TOKEN_RBRACE) {
			// Empty dict {}
			return p.arena.AllocExpr(ASTExpr{
				Kind: ExprKindDict,
			}), nil
		}
		if p.match(TOKEN_STARSTAR) {
			v, err := p.parseTernary()
			if err != nil {
				return ExprNone, err
			}
			// Collect locally: parsing a nested dict literal appends to the
			// arena, so this literal's pairs are only contiguous once complete.
			pairs := []ASTDictPair{{Key: ExprNone, Value: v}}
			for p.match(TOKEN_COMMA) && p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF {
				if p.match(TOKEN_STARSTAR) {
					v2, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					pairs = append(pairs, ASTDictPair{Key: ExprNone, Value: v2})
				} else {
					k, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					if _, err := p.expect(TOKEN_COLON); err != nil {
						return ExprNone, err
					}
					v2, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					pairs = append(pairs, ASTDictPair{Key: k, Value: v2})
				}
			}
			if _, err := p.expect(TOKEN_RBRACE); err != nil {
				return ExprNone, err
			}
			return p.finishDictLiteral(pairs), nil
		}

		firstKey, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}

		if p.match(TOKEN_COLON) {
			firstVal, err := p.parseTernary()
			if err != nil {
				return ExprNone, err
			}

			// Check for dict comprehension: {k: v for target in iter if cond}
			if ok, isAsync := p.matchCompStart(); ok {
				compIdx, err := p.parseCompClauses(isAsync)
				if err != nil {
					return ExprNone, err
				}
				p.arena.Comps[compIdx].IsDict = true
				if _, err := p.expect(TOKEN_RBRACE); err != nil {
					return ExprNone, err
				}
				return p.arena.AllocExpr(ASTExpr{
					Kind:  ExprKindDictComp,
					Left:  firstKey,
					Right: firstVal,
					Extra: compIdx,
				}), nil
			}

			// Regular dict literal {k: v, ...}
			pairs := []ASTDictPair{{Key: firstKey, Value: firstVal}}

			for p.match(TOKEN_COMMA) && p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF {
				if p.match(TOKEN_STARSTAR) {
					v, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					pairs = append(pairs, ASTDictPair{Key: ExprNone, Value: v})
				} else {
					k, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					if _, err := p.expect(TOKEN_COLON); err != nil {
						return ExprNone, err
					}
					v, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					pairs = append(pairs, ASTDictPair{Key: k, Value: v})
				}
			}
			if _, err := p.expect(TOKEN_RBRACE); err != nil {
				return ExprNone, err
			}
			return p.finishDictLiteral(pairs), nil
		}

		// Check for set comprehension: {elt for target in iter if cond}
		if ok, isAsync := p.matchCompStart(); ok {
			compIdx, err := p.parseCompClauses(isAsync)
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_RBRACE); err != nil {
				return ExprNone, err
			}
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindSetComp,
				Left:  firstKey,
				Extra: compIdx,
			}), nil
		}

		// Regular set literal {a, b, ...}
		var items []ExprID
		items = append(items, firstKey)
		for p.match(TOKEN_COMMA) && p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF {
			item, err := p.parseTernary()
			if err != nil {
				return ExprNone, err
			}
			items = append(items, item)
		}
		if _, err := p.expect(TOKEN_RBRACE); err != nil {
			return ExprNone, err
		}
		p.arena.AddExprSlice(items)
		sliceIdx := uint32(len(p.arena.ExprSlices) - 1)
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindSet,
			Extra: sliceIdx,
			Op:    uint8(len(items)),
		}), nil

	case TOKEN_YIELD:
		return p.parseYield()

	case TOKEN_AWAIT:
		tok := p.advance()
		operand, err := p.parseUnary()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindAwait,
			Start: tok.Start,
			Left:  operand,
		}), nil

	default:
		return ExprNone, fmt.Errorf("SyntaxError: unexpected token %s at line %d, col %d", tok.Type, tok.Line, tok.Col)
	}
}

func (p *Parser) parseYield() (ExprID, error) {
	startTok, _ := p.expect(TOKEN_YIELD)
	if p.cur.Type == TOKEN_FROM || (p.cur.Type == TOKEN_IDENT && string(p.src[p.cur.Start:p.cur.End]) == "from") {
		p.advance() // 'from'
		sub, err := p.parseExpr()
		if err != nil {
			return ExprNone, err
		}
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindYieldFrom,
			Start: startTok.Start,
			Left:  sub,
		}), nil
	}
	var val ExprID
	if p.cur.Type != TOKEN_NEWLINE && p.cur.Type != TOKEN_SEMI && p.cur.Type != TOKEN_EOF &&
		p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_RBRACKET && p.cur.Type != TOKEN_RBRACE &&
		p.cur.Type != TOKEN_COLON && p.cur.Type != TOKEN_COMMA {
		var err error
		val, err = p.parseExpr()
		if err != nil {
			return ExprNone, err
		}
	}
	return p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindYield,
		Start: startTok.Start,
		Left:  val,
	}), nil
}

// finishDictLiteral stores a completed dict display. Pairs are appended in
// one run so Extra..Extra+Right is contiguous; Right carries the pair count
// (Op is a byte and would truncate large literals).
func (p *Parser) finishDictLiteral(pairs []ASTDictPair) ExprID {
	start := uint32(len(p.arena.DictPairs))
	p.arena.DictPairs = append(p.arena.DictPairs, pairs...)
	return p.arena.AllocExpr(ASTExpr{
		Kind:  ExprKindDict,
		Extra: start,
		Right: ExprID(len(pairs)),
	})
}

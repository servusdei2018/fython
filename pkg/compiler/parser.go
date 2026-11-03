package compiler

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/servusdei2018/fython/pkg/vm"
)

// Parser parses Python source code into a flat ASTArena.
type Parser struct {
	src              []byte
	lexer            *Lexer
	arena            *ASTArena
	cur              Token
	peeked           Token
	hasP             bool
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
		// Single-line suite: e.g. "if x < 2: return x"
		stmt, err := p.parseStmt()
		if err != nil {
			return 0, err
		}
		if stmt != StmtNone {
			stmts = append(stmts, stmt)
		}
	}
	return p.arena.AddStmtSlice(stmts), nil
}

func (p *Parser) parseFuncDef() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_DEF)
	nameTok, err := p.expect(TOKEN_IDENT)
	if err != nil {
		return StmtNone, err
	}
	name := p.tokenText(nameTok)

	if _, err := p.expect(TOKEN_LPAREN); err != nil {
		return StmtNone, err
	}

	var paramNames []string
	var defaults []ExprID

	for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
		paramTok, err := p.expect(TOKEN_IDENT)
		if err != nil {
			return StmtNone, err
		}
		paramNames = append(paramNames, p.tokenText(paramTok))
		if p.match(TOKEN_ASSIGN) {
			defExpr, err := p.parseTernary()
			if err != nil {
				return StmtNone, err
			}
			defaults = append(defaults, defExpr)
		}
		if !p.match(TOKEN_COMMA) {
			break
		}
	}

	if _, err := p.expect(TOKEN_RPAREN); err != nil {
		return StmtNone, err
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
		Name:       name,
		ParamNames: p.arena.AddIdentSlice(paramNames),
		Defaults:   p.arena.AddExprSlice(defaults),
		Body:       p.arena.StmtSlices[bodySlice],
	})

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindFuncDef,
		Start: startTok.Start,
		End:   p.arena.GetStmtSlice(bodySlice)[len(p.arena.GetStmtSlice(bodySlice))-1].endInArena(p.arena),
		Extra: fnIdx,
	}), nil
}

func (p *Parser) parseClassDef() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_CLASS)
	nameTok, err := p.expect(TOKEN_IDENT)
	if err != nil {
		return StmtNone, err
	}
	name := p.tokenText(nameTok)

	var bases []ExprID
	if p.match(TOKEN_LPAREN) {
		for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
			baseExpr, err := p.parseTernary()
			if err != nil {
				return StmtNone, err
			}
			bases = append(bases, baseExpr)
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
		Name:  name,
		Bases: p.arena.AddExprSlice(bases),
		Body:  p.arena.StmtSlices[bodySlice],
	})

	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindClassDef,
		Start: startTok.Start,
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
				nameTok, err := p.expect(TOKEN_IDENT)
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
			Type: excType,
			Name: excName,
			Body: p.arena.StmtSlices[hBody],
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
		return StmtNone, fmt.Errorf("SyntaxError: expected 'except' or 'finally' block")
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

func (p *Parser) parseWithStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_WITH)
	var items []ASTWithItem
	for {
		expr, err := p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		var target ExprID
		if p.match(TOKEN_AS) {
			target, err = p.parseExpr()
			if err != nil {
				return StmtNone, err
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

func (p *Parser) parseGlobalStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_GLOBAL)
	var names []string
	for {
		nameTok, err := p.expect(TOKEN_IDENT)
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
		nameTok, err := p.expect(TOKEN_IDENT)
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
	tok, err := p.expect(TOKEN_IDENT)
	if err != nil {
		return "", 0, 0, err
	}
	start := tok.Start
	end := tok.End
	name := p.tokenText(tok)
	for p.match(TOKEN_DOT) {
		nextTok, err := p.expect(TOKEN_IDENT)
		if err != nil {
			return "", 0, 0, err
		}
		end = nextTok.End
		name += "." + p.tokenText(nextTok)
	}
	return name, start, end, nil
}

func (p *Parser) parseImportStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_IMPORT)
	var aliases []ASTAlias
	for {
		name, _, _, err := p.parseDottedName()
		if err != nil {
			return StmtNone, err
		}
		asName := ""
		if p.match(TOKEN_AS) {
			asTok, err := p.expect(TOKEN_IDENT)
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
	impIdx := p.arena.AllocImport(ASTImport{Aliases: aliasSlice})
	return p.arena.AllocStmt(ASTStmt{
		Kind:  StmtKindImport,
		Start: startTok.Start,
		Extra: impIdx,
	}), nil
}

func (p *Parser) parseFromImportStmt() (StmtID, error) {
	startTok, _ := p.expect(TOKEN_FROM)
	level := 0
	for p.match(TOKEN_DOT) {
		level++
	}

	module := ""
	if p.cur.Type == TOKEN_IDENT {
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

		nameTok, err := p.expect(TOKEN_IDENT)
		if err != nil {
			return StmtNone, err
		}
		name := p.tokenText(nameTok)
		asName := ""
		if p.match(TOKEN_AS) {
			asTok, err := p.expect(TOKEN_IDENT)
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

	// Check for assignment: target = expr
	if p.match(TOKEN_ASSIGN) {
		val, err := p.parseExpr()
		if err != nil {
			return StmtNone, err
		}
		p.match(TOKEN_SEMI)
		p.match(TOKEN_NEWLINE)
		return p.arena.AllocStmt(ASTStmt{
			Kind:  StmtKindAssign,
			Expr1: expr,
			Expr2: val,
		}), nil
	}

	// Augmented assignment: +=, -=, *=, /=, %=, etc.
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
		t == TOKEN_SLASHEQ || t == TOKEN_SLASHSLASHEQ || t == TOKEN_PERCENTEQ || t == TOKEN_STARSTAREQ
}

func (s StmtID) endInArena(arena *ASTArena) uint32 {
	stmt := arena.GetStmt(s)
	if stmt == nil {
		return 0
	}
	return stmt.End
}

// Expression Parsing (Pratt Precedence)

func (p *Parser) parseExpr() (ExprID, error) {
	if p.cur.Type == TOKEN_YIELD {
		return p.parseYield()
	}
	first, err := p.parseTernary()
	if err != nil {
		return ExprNone, err
	}
	if p.cur.Type == TOKEN_COMMA {
		var items []ExprID
		items = append(items, first)
		for p.match(TOKEN_COMMA) {
			if p.cur.Type == TOKEN_NEWLINE || p.cur.Type == TOKEN_SEMI || p.cur.Type == TOKEN_EOF ||
				p.cur.Type == TOKEN_ASSIGN || p.cur.Type == TOKEN_RPAREN || p.cur.Type == TOKEN_RBRACKET ||
				p.cur.Type == TOKEN_RBRACE || p.cur.Type == TOKEN_COLON {
				break
			}
			item, err := p.parseTernary()
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
		left = p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindCompare,
			Op:    cmpOp,
			Left:  left,
			Right: right,
		})
	}

	return left, nil
}

func (p *Parser) parseBitOr() (ExprID, error) {
	return p.parseAddSub()
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
		p.cur.Type == TOKEN_SLASHSLASH || p.cur.Type == TOKEN_PERCENT {
		opTok := p.advance()
		right, err := p.parseUnary()
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

func (p *Parser) parseUnary() (ExprID, error) {
	if p.cur.Type == TOKEN_PLUS || p.cur.Type == TOKEN_MINUS {
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
				if p.cur.Type == TOKEN_IDENT && p.peek().Type == TOKEN_ASSIGN {
					nameTok := p.advance()
					p.advance() // =
					kwVal, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
					}
					kwNames = append(kwNames, p.tokenText(nameTok))
					kwArgs = append(kwArgs, kwVal)
				} else {
					arg, err := p.parseTernary()
					if err != nil {
						return ExprNone, err
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
			attrTok, err := p.expect(TOKEN_IDENT)
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
			subExpr, err := p.parseExpr()
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
			return ExprNone, fmt.Errorf("SyntaxError: invalid integer literal %q", string(raw))
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

	case TOKEN_STRING:
		p.advance()
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindString,
			Start: tok.Start,
			End:   tok.End,
		}), nil

	case TOKEN_IDENT:
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
		first, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}
		if p.match(TOKEN_COMMA) {
			// Tuple literal (a, ...)
			var items []ExprID
			items = append(items, first)
			for p.cur.Type != TOKEN_RPAREN && p.cur.Type != TOKEN_EOF {
				item, err := p.parseTernary()
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
		first, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}
		// Check for list comprehension: [elt for target in iter if cond]
		if p.match(TOKEN_FOR) {
			p.parsingForTarget = true
			target, err := p.parseExpr()
			p.parsingForTarget = false
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_IN); err != nil {
				return ExprNone, err
			}
			iter, err := p.parseOr()
			if err != nil {
				return ExprNone, err
			}
			var ifs []ExprID
			for p.match(TOKEN_IF) {
				ifCond, err := p.parseOr()
				if err != nil {
					return ExprNone, err
				}
				ifs = append(ifs, ifCond)
			}
			if _, err := p.expect(TOKEN_RBRACKET); err != nil {
				return ExprNone, err
			}
			compIdx := uint32(len(p.arena.Comps))
			p.arena.Comps = append(p.arena.Comps, ASTComp{
				Target:  target,
				Iter:    iter,
				IfConds: p.arena.AddExprSlice(ifs),
				IsDict:  false,
			})
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
			item, err := p.parseTernary()
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
		firstKey, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}
		if _, err := p.expect(TOKEN_COLON); err != nil {
			return ExprNone, err
		}
		firstVal, err := p.parseTernary()
		if err != nil {
			return ExprNone, err
		}

		// Check for dict comprehension: {k: v for target in iter if cond}
		if p.match(TOKEN_FOR) {
			p.parsingForTarget = true
			target, err := p.parseExpr()
			p.parsingForTarget = false
			if err != nil {
				return ExprNone, err
			}
			if _, err := p.expect(TOKEN_IN); err != nil {
				return ExprNone, err
			}
			iter, err := p.parseOr()
			if err != nil {
				return ExprNone, err
			}
			var ifs []ExprID
			for p.match(TOKEN_IF) {
				ifCond, err := p.parseOr()
				if err != nil {
					return ExprNone, err
				}
				ifs = append(ifs, ifCond)
			}
			if _, err := p.expect(TOKEN_RBRACE); err != nil {
				return ExprNone, err
			}
			compIdx := uint32(len(p.arena.Comps))
			p.arena.Comps = append(p.arena.Comps, ASTComp{
				Target:  target,
				Iter:    iter,
				IfConds: p.arena.AddExprSlice(ifs),
				IsDict:  true,
			})
			return p.arena.AllocExpr(ASTExpr{
				Kind:  ExprKindDictComp,
				Left:  firstKey,
				Right: firstVal,
				Extra: compIdx,
			}), nil
		}

		// Regular dict literal {k: v, ...}
		dictStart := uint32(len(p.arena.DictPairs))
		p.arena.DictPairs = append(p.arena.DictPairs, ASTDictPair{Key: firstKey, Value: firstVal})

		for p.match(TOKEN_COMMA) && p.cur.Type != TOKEN_RBRACE && p.cur.Type != TOKEN_EOF {
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
			p.arena.DictPairs = append(p.arena.DictPairs, ASTDictPair{Key: k, Value: v})
		}
		if _, err := p.expect(TOKEN_RBRACE); err != nil {
			return ExprNone, err
		}
		count := uint32(len(p.arena.DictPairs)) - dictStart
		return p.arena.AllocExpr(ASTExpr{
			Kind:  ExprKindDict,
			Extra: dictStart,
			Op:    uint8(count),
		}), nil

	case TOKEN_YIELD:
		return p.parseYield()

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

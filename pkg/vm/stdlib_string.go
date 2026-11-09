package vm

import (
	"strconv"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createStringModule() val.Value {
	modVal := val.NewModuleVal("_string", "Fast string formatting utilities")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("formatter_parser"), val.NewModuleFuncVal("formatter_parser", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: formatter_parser() takes exactly 1 argument")
		}
		s := args[0].AsString()
		var results []val.Value

		i := 0
		n := len(s)
		for i < n {
			// Find next '{' or '}'
			nextBrace := -1
			for j := i; j < n; j++ {
				if s[j] == '{' || s[j] == '}' {
					nextBrace = j
					break
				}
			}

			if nextBrace == -1 {
				// No more braces
				results = append(results, val.NewTupleVal(
					val.NewStringVal(s[i:]),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
				))
				break
			}

			if nextBrace > i {
				results = append(results, val.NewTupleVal(
					val.NewStringVal(s[i:nextBrace]),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
				))
				i = nextBrace
			}

			// Handle escaped braces {{ or }}
			if i+1 < n && s[i] == s[i+1] {
				results = append(results, val.NewTupleVal(
					val.NewStringVal(string(s[i])),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
				))
				i += 2
				continue
			}

			if s[i] == '}' {
				panic("ValueError: Single '}' encountered in format string")
			}

			// We found a replacement field '{'
			// Find matching '}' taking nested braces into account
			fieldStart := i + 1
			depth := 1
			fieldEnd := -1
			for j := fieldStart; j < n; j++ {
				if s[j] == '{' {
					depth++
				} else if s[j] == '}' {
					depth--
					if depth == 0 {
						fieldEnd = j
						break
					}
				}
			}

			if fieldEnd == -1 {
				panic("ValueError: Expected '}' before end of string")
			}

			fieldContent := s[fieldStart:fieldEnd]
			i = fieldEnd + 1

			// Parse fieldContent into: fieldName, conversion, formatSpec
			fieldName := fieldContent
			formatSpec := ""
			conversion := ""
			hasConversion := false

			// Find ':' that separates formatSpec (not inside nested braces)
			colonIdx := -1
			nested := 0
		loopColon:
			for idx, ch := range fieldContent {
				switch ch {
				case '{':
					nested++
				case '}':
					nested--
				case ':':
					if nested == 0 {
						colonIdx = idx
						break loopColon
					}
				}
			}

			if colonIdx != -1 {
				fieldName = fieldContent[:colonIdx]
				formatSpec = fieldContent[colonIdx+1:]
			}

			// Check for '!' conversion before ':'
			exclIdx := strings.LastIndex(fieldName, "!")
			if exclIdx != -1 {
				conversion = fieldName[exclIdx+1:]
				fieldName = fieldName[:exclIdx]
				hasConversion = true
			}

			convVal := val.NoneVal()
			if hasConversion {
				convVal = val.NewStringVal(conversion)
			}

			results = append(results, val.NewTupleVal(
				val.NewStringVal(""),
				val.NewStringVal(fieldName),
				val.NewStringVal(formatSpec),
				convVal,
			))
		}

		return val.NewListVal(results...)
	}))

	mod.Dict.Set(val.InternString("formatter_field_name_split"), val.NewModuleFuncVal("formatter_field_name_split", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: formatter_field_name_split() takes exactly 1 argument")
		}
		fieldName := args[0].AsString()
		if fieldName == "" {
			return val.NewTupleVal(val.NewStringVal(""), val.NewListVal())
		}

		// First part: until '.' or '['
		firstEnd := len(fieldName)
		for idx, ch := range fieldName {
			if ch == '.' || ch == '[' {
				firstEnd = idx
				break
			}
		}

		firstStr := fieldName[:firstEnd]
		var firstVal val.Value
		if num, err := strconv.ParseInt(firstStr, 10, 64); err == nil && firstStr != "" {
			firstVal = val.Int64Val(num)
		} else {
			firstVal = val.NewStringVal(firstStr)
		}

		var rest []val.Value
		idx := firstEnd
		n := len(fieldName)
		for idx < n {
			switch fieldName[idx] {
			case '.':
				idx++
				attrStart := idx
				for idx < n && fieldName[idx] != '.' && fieldName[idx] != '[' {
					idx++
				}
				attrName := fieldName[attrStart:idx]
				rest = append(rest, val.NewTupleVal(val.BoolVal(true), val.NewStringVal(attrName)))
			case '[':
				idx++
				itemStart := idx
				for idx < n && fieldName[idx] != ']' {
					idx++
				}
				if idx >= n {
					panic("ValueError: unclosed '[' in format field name")
				}
				itemStr := fieldName[itemStart:idx]
				idx++ // skip ']'
				var itemVal val.Value
				if num, err := strconv.ParseInt(itemStr, 10, 64); err == nil && itemStr != "" {
					itemVal = val.Int64Val(num)
				} else {
					itemVal = val.NewStringVal(itemStr)
				}
				rest = append(rest, val.NewTupleVal(val.BoolVal(false), itemVal))
			default:
				idx++
			}
		}

		return val.NewTupleVal(firstVal, val.NewListVal(rest...))
	}))

	return modVal
}

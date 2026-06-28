package markdown

import "strings"

// This is a small, dependency-free syntax highlighter for fenced code blocks.
// A full lexer library (chroma) adds ~7 MB to the wasm bundle for what amounts
// to coloring comments / strings / numbers / keywords in a notes tool — so we
// tokenize generically here: comment and string delimiters + a per-language
// keyword set cover the common languages well, and the whole thing is pure and
// unit-testable. Unknown languages render as a single plain run (no color).

// hlToken is a colored run of code-block text. A run may contain newlines
// (multi-line strings / block comments); the code-block layout splits on them.
type hlToken struct {
	Text  string
	Color ColorRole
}

// langSyntax describes how to tokenize one family of languages.
type langSyntax struct {
	keywords   map[string]bool
	lineCmt    string // line-comment marker ("//", "#"); "" if none
	blockOpen  string // block-comment open ("/*"); "" if none
	blockClose string
	triple     bool // python-style triple-quoted strings
}

// highlight tokenizes code for the given language into colored runs. Languages
// it doesn't know return one plain run.
func highlight(code, lang string) []hlToken {
	syn, ok := syntaxFor(lang)
	if !ok {
		return []hlToken{{Text: code, Color: ColorCode}}
	}
	var out []hlToken
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, hlToken{Text: plain.String(), Color: ColorCode})
			plain.Reset()
		}
	}
	emit := func(text string, col ColorRole) {
		flush()
		out = append(out, hlToken{Text: text, Color: col})
	}

	i := 0
	for i < len(code) {
		// Block comment.
		if syn.blockOpen != "" && strings.HasPrefix(code[i:], syn.blockOpen) {
			end := strings.Index(code[i+len(syn.blockOpen):], syn.blockClose)
			j := len(code)
			if end >= 0 {
				j = i + len(syn.blockOpen) + end + len(syn.blockClose)
			}
			emit(code[i:j], ColorSynComment)
			i = j
			continue
		}
		// Line comment.
		if syn.lineCmt != "" && strings.HasPrefix(code[i:], syn.lineCmt) {
			j := i + len(syn.lineCmt)
			for j < len(code) && code[j] != '\n' {
				j++
			}
			emit(code[i:j], ColorSynComment)
			i = j
			continue
		}
		c := code[i]
		// Triple-quoted string (python).
		if syn.triple && (c == '"' || c == '\'') && strings.HasPrefix(code[i:], strings.Repeat(string(c), 3)) {
			q := strings.Repeat(string(c), 3)
			end := strings.Index(code[i+3:], q)
			j := len(code)
			if end >= 0 {
				j = i + 3 + end + 3
			}
			emit(code[i:j], ColorSynString)
			i = j
			continue
		}
		// String.
		if c == '"' || c == '\'' || c == '`' {
			j := scanString(code, i, c)
			emit(code[i:j], ColorSynString)
			i = j
			continue
		}
		// Number.
		if isDigit(c) {
			j := i + 1
			for j < len(code) && (isDigit(code[j]) || isNumPart(code[j])) {
				j++
			}
			emit(code[i:j], ColorSynNumber)
			i = j
			continue
		}
		// Identifier / keyword.
		if isIdentStart(c) {
			j := i + 1
			for j < len(code) && isIdentPart(code[j]) {
				j++
			}
			word := code[i:j]
			if syn.keywords[word] {
				emit(word, ColorSynKeyword)
			} else {
				plain.WriteString(word)
			}
			i = j
			continue
		}
		plain.WriteByte(c)
		i++
	}
	flush()
	return out
}

// scanString returns the index just past a string literal opened by quote at
// start. Backtick strings are raw; "/' honor backslash escapes.
func scanString(code string, start int, quote byte) int {
	i := start + 1
	for i < len(code) {
		if quote != '`' && code[i] == '\\' {
			i += 2
			continue
		}
		if code[i] == quote {
			return i + 1
		}
		i++
	}
	return len(code)
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isNumPart(b byte) bool {
	return b == '.' || b == 'x' || b == 'b' || b == 'o' || b == '_' || b == 'e' || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
func isIdentStart(b byte) bool { return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isIdentPart(b byte) bool  { return isIdentStart(b) || isDigit(b) }

// syntaxFor returns the syntax for a language info-string (case-insensitive),
// resolving common aliases. ok is false for unknown languages.
func syntaxFor(lang string) (langSyntax, bool) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go", "golang":
		return cLike(goKeywords), true
	case "js", "javascript", "jsx", "ts", "typescript", "tsx":
		return cLike(jsKeywords), true
	case "rust", "rs":
		return cLike(rustKeywords), true
	case "c", "cpp", "c++", "h", "hpp", "java", "kotlin", "kt", "swift", "cs", "csharp":
		return cLike(cKeywords), true
	case "python", "py":
		return langSyntax{keywords: pyKeywords, lineCmt: "#", triple: true}, true
	case "bash", "sh", "shell", "zsh", "ruby", "rb", "yaml", "yml", "toml", "ini":
		return langSyntax{keywords: shKeywords, lineCmt: "#"}, true
	case "json":
		return langSyntax{keywords: map[string]bool{}}, true
	}
	return langSyntax{}, false
}

// cLike builds a C-family syntax (// and /* */ comments) with the given
// keyword set.
func cLike(kw map[string]bool) langSyntax {
	return langSyntax{keywords: kw, lineCmt: "//", blockOpen: "/*", blockClose: "*/"}
}

func keywordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	goKeywords = keywordSet("break", "case", "chan", "const", "continue", "default", "defer", "else",
		"fallthrough", "for", "func", "go", "goto", "if", "import", "interface", "map", "package",
		"range", "return", "select", "struct", "switch", "type", "var", "nil", "true", "false",
		"string", "int", "int64", "byte", "rune", "bool", "error", "float64")
	jsKeywords = keywordSet("async", "await", "break", "case", "catch", "class", "const", "continue",
		"default", "delete", "do", "else", "export", "extends", "finally", "for", "function", "if",
		"import", "in", "instanceof", "let", "new", "of", "return", "super", "switch", "this", "throw",
		"try", "typeof", "var", "void", "while", "yield", "null", "true", "false", "undefined")
	rustKeywords = keywordSet("as", "async", "await", "break", "const", "continue", "crate", "dyn",
		"else", "enum", "extern", "false", "fn", "for", "if", "impl", "in", "let", "loop", "match",
		"mod", "move", "mut", "pub", "ref", "return", "self", "static", "struct", "super", "trait",
		"true", "type", "unsafe", "use", "where", "while")
	cKeywords = keywordSet("auto", "break", "case", "char", "const", "continue", "default", "do",
		"double", "else", "enum", "extern", "float", "for", "goto", "if", "int", "long", "return",
		"short", "signed", "sizeof", "static", "struct", "switch", "typedef", "union", "unsigned",
		"void", "volatile", "while", "class", "public", "private", "protected", "new", "delete",
		"namespace", "template", "true", "false", "null", "nullptr")
	pyKeywords = keywordSet("and", "as", "assert", "async", "await", "break", "class", "continue",
		"def", "del", "elif", "else", "except", "finally", "for", "from", "global", "if", "import",
		"in", "is", "lambda", "nonlocal", "not", "or", "pass", "raise", "return", "try", "while",
		"with", "yield", "None", "True", "False", "self")
	shKeywords = keywordSet("if", "then", "else", "elif", "fi", "for", "in", "do", "done", "while",
		"case", "esac", "function", "return", "export", "local", "echo", "true", "false")
)

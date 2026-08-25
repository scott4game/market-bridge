/* Market Bridge TDX formula worker; formula-ts MIT notices in THIRD_PARTY_NOTICES.md */
(() => {
  // web/vendor/formula-ts/src/lexer/Token.ts
  var Token = class {
    /**
     * Creates a new Token
     * @param type The token type
     * @param value The token value
     * @param line The line number (1-indexed)
     * @param column The column number (1-indexed)
     */
    constructor(type, value, line, column) {
      this.type = type;
      this.value = value;
      this.line = line;
      this.column = column;
    }
    /**
     * Returns a string representation of the token
     */
    toString() {
      return `Token(${this.type}, ${JSON.stringify(this.value)}, ${this.line}:${this.column})`;
    }
    /**
     * Checks equality with another token
     */
    equals(other) {
      return this.type === other.type && this.value === other.value && this.line === other.line && this.column === other.column;
    }
  };

  // web/vendor/formula-ts/src/errors/index.ts
  var FormulaError = class _FormulaError extends Error {
    constructor(message) {
      super(message);
      this.name = "FormulaError";
      Object.setPrototypeOf(this, _FormulaError.prototype);
    }
  };
  var LexerError = class _LexerError extends FormulaError {
    line;
    column;
    char;
    constructor(message, line, column, char) {
      const charPart = char !== void 0 ? ` (char: ${char})` : "";
      const fullMessage = `Lexer error at line ${line}, column ${column}: ${message}${charPart}`;
      super(fullMessage);
      this.name = "LexerError";
      this.line = line;
      this.column = column;
      this.char = char;
      Object.setPrototypeOf(this, _LexerError.prototype);
    }
  };
  var ParserError = class _ParserError extends FormulaError {
    line;
    column;
    constructor(message, line, column) {
      const fullMessage = `Parser error at line ${line}, column ${column}: ${message}`;
      super(fullMessage);
      this.name = "ParserError";
      this.line = line;
      this.column = column;
      Object.setPrototypeOf(this, _ParserError.prototype);
    }
  };

  // web/vendor/formula-ts/src/lexer/Lexer.ts
  var Lexer = class {
    input;
    position = 0;
    line = 1;
    column = 1;
    currentChar = null;
    // Keywords map for quick lookup
    keywords = /* @__PURE__ */ new Map([
      ["IF", "IF" /* IF */],
      ["AND", "AND" /* AND */],
      ["OR", "OR" /* OR */],
      ["DOTLINE", "DOTLINE" /* DOTLINE */],
      ["STICK", "STICK" /* STICK */],
      ["COLORSTICK", "COLORSTICK" /* COLORSTICK */],
      ["VOLSTICK", "VOLSTICK" /* VOLSTICK */],
      ["NODRAW", "NODRAW" /* NODRAW */]
    ]);
    /**
     * Creates a new Lexer instance
     * @param input The source code to tokenize
     */
    constructor(input) {
      this.input = input;
      this.currentChar = input.length > 0 ? input[0] : null;
    }
    /**
     * Tokenizes the entire input and returns array of tokens
     * @returns Array of tokens including EOF token at the end
     * @throws {LexerError} If an invalid character or syntax is encountered
     */
    tokenize() {
      const tokens = [];
      let lastWasNewline = false;
      while (this.currentChar !== null) {
        if (this.isWhitespace(this.currentChar)) {
          this.skipWhitespace();
          continue;
        }
        if (this.isNewline(this.currentChar)) {
          if (!lastWasNewline) {
            tokens.push(this.makeToken("NEWLINE" /* NEWLINE */, "\n"));
            lastWasNewline = true;
          }
          this.skipNewline();
          continue;
        }
        lastWasNewline = false;
        if (this.currentChar === "/" && this.peek() === "/") {
          this.skipSingleLineComment();
          continue;
        }
        if (this.currentChar === "{") {
          this.skipBlockComment();
          continue;
        }
        if (this.isDigit(this.currentChar) || this.currentChar === "." && this.isDigit(this.peek())) {
          tokens.push(this.readNumber());
          continue;
        }
        if (this.isIdentifierStart(this.currentChar)) {
          tokens.push(this.readIdentifierOrKeyword());
          continue;
        }
        if (this.currentChar === "'" || this.currentChar === '"') {
          tokens.push(this.readString());
          continue;
        }
        const operatorToken = this.readOperatorOrDelimiter();
        if (operatorToken) {
          tokens.push(operatorToken);
          continue;
        }
        throw new LexerError(
          `Unexpected character: '${this.currentChar}'`,
          this.line,
          this.column,
          this.currentChar
        );
      }
      tokens.push(this.makeToken("EOF" /* EOF */, ""));
      return tokens;
    }
    /**
     * Advances to the next character
     */
    advance() {
      this.position++;
      this.column++;
      this.currentChar = this.position < this.input.length ? this.input[this.position] : null;
    }
    /**
     * Looks at the next character without consuming it
     */
    peek(offset = 1) {
      const peekPos = this.position + offset;
      return peekPos < this.input.length ? this.input[peekPos] : null;
    }
    /**
     * Creates a token at the current position
     */
    makeToken(type, value) {
      return new Token(type, value, this.line, this.column - value.length);
    }
    /**
     * Checks if character is whitespace (excluding newlines)
     */
    isWhitespace(char) {
      return char === " " || char === "	";
    }
    /**
     * Checks if character is a newline
     */
    isNewline(char) {
      return char === "\n" || char === "\r";
    }
    /**
     * Checks if character is a digit
     */
    isDigit(char) {
      return char !== null && char >= "0" && char <= "9";
    }
    /**
     * Checks if character is alphabetic
     */
    isAlpha(char) {
      return /\p{L}/u.test(char);
    }
    /**
     * Checks if character can start an identifier
     */
    isIdentifierStart(char) {
      return this.isAlpha(char) || char === "_";
    }
    /**
     * Checks if character can continue an identifier
     */
    isIdentifierPart(char) {
      return this.isIdentifierStart(char) || this.isDigit(char);
    }
    /**
     * Skips whitespace characters
     */
    skipWhitespace() {
      while (this.currentChar !== null && this.isWhitespace(this.currentChar)) {
        this.advance();
      }
    }
    /**
     * Skips newline characters and updates line number
     */
    skipNewline() {
      if (this.currentChar === "\r" && this.peek() === "\n") {
        this.advance();
        this.advance();
      } else {
        this.advance();
      }
      this.line++;
      this.column = 1;
      while (this.currentChar !== null && this.isNewline(this.currentChar)) {
        if (this.currentChar === "\r" && this.peek() === "\n") {
          this.advance();
          this.advance();
        } else {
          this.advance();
        }
        this.line++;
        this.column = 1;
      }
    }
    /**
     * Skips single-line comment starting with //
     */
    skipSingleLineComment() {
      this.advance();
      this.advance();
      while (this.currentChar !== null && !this.isNewline(this.currentChar)) {
        this.advance();
      }
    }
    /**
     * Skips block comment enclosed in { }
     */
    skipBlockComment() {
      const startLine = this.line;
      const startColumn = this.column;
      this.advance();
      while (this.currentChar !== null && this.currentChar !== "}") {
        if (this.isNewline(this.currentChar)) {
          this.skipNewline();
        } else {
          this.advance();
        }
      }
      if (this.currentChar === null) {
        throw new LexerError(
          "Unterminated block comment",
          startLine,
          startColumn,
          "{"
        );
      }
      this.advance();
    }
    /**
     * Reads a number (integer or floating point)
     */
    readNumber() {
      const startColumn = this.column;
      let value = "";
      if (this.currentChar === ".") {
        value += ".";
        this.advance();
      }
      while (this.currentChar !== null && this.isDigit(this.currentChar)) {
        value += this.currentChar;
        this.advance();
      }
      if (this.currentChar === "." && value !== ".") {
        value += ".";
        this.advance();
        while (this.currentChar !== null && this.isDigit(this.currentChar)) {
          value += this.currentChar;
          this.advance();
        }
      }
      return new Token("NUMBER" /* NUMBER */, value, this.line, startColumn);
    }
    /**
     * Reads an identifier or keyword
     */
    readIdentifierOrKeyword() {
      const startColumn = this.column;
      let value = "";
      while (this.currentChar !== null && this.isIdentifierPart(this.currentChar)) {
        value += this.currentChar;
        this.advance();
      }
      const upperValue = value.toUpperCase();
      const keywordType = this.keywords.get(upperValue);
      if (keywordType !== void 0) {
        return new Token(keywordType, upperValue, this.line, startColumn);
      }
      if (upperValue.startsWith("COLOR") && upperValue.length > 5) {
        return new Token("COLOR" /* COLOR */, upperValue, this.line, startColumn);
      }
      if (upperValue.startsWith("LINETHICK") && upperValue.length === 10) {
        const thicknessChar = upperValue[9];
        if (thicknessChar >= "1" && thicknessChar <= "9") {
          return new Token("LINETHICK" /* LINETHICK */, upperValue, this.line, startColumn);
        }
      }
      return new Token("IDENTIFIER" /* IDENTIFIER */, value, this.line, startColumn);
    }
    /**
     * Reads a single-quoted or double-quoted string literal
     */
    readString() {
      const startColumn = this.column;
      const quote = this.currentChar;
      let value = "";
      this.advance();
      while (this.currentChar !== null && this.currentChar !== quote) {
        if (this.currentChar === "\\") {
          this.advance();
          if (this.currentChar === null) {
            break;
          }
          const escapedChar = this.currentChar;
          switch (escapedChar) {
            case "n":
              value += "\n";
              break;
            case "r":
              value += "\r";
              break;
            case "t":
              value += "	";
              break;
            case "\\":
              value += "\\";
              break;
            case "'":
              value += "'";
              break;
            case '"':
              value += '"';
              break;
            default:
              value += escapedChar;
              break;
          }
          this.advance();
          continue;
        }
        if (this.isNewline(this.currentChar)) {
          throw new LexerError("Unterminated string literal", this.line, startColumn, quote ?? "");
        }
        value += this.currentChar;
        this.advance();
      }
      if (this.currentChar !== quote) {
        throw new LexerError("Unterminated string literal", this.line, startColumn, quote ?? "");
      }
      this.advance();
      return new Token("STRING" /* STRING */, value, this.line, startColumn);
    }
    /**
     * Reads an operator or delimiter token
     */
    readOperatorOrDelimiter() {
      const startColumn = this.column;
      const char = this.currentChar;
      if (char === null) {
        return null;
      }
      switch (char) {
        case "+":
          this.advance();
          return new Token("PLUS" /* PLUS */, "+", this.line, startColumn);
        case "-":
          this.advance();
          return new Token("MINUS" /* MINUS */, "-", this.line, startColumn);
        case "*":
          this.advance();
          return new Token("MULTIPLY" /* MULTIPLY */, "*", this.line, startColumn);
        case "/":
          this.advance();
          return new Token("DIVIDE" /* DIVIDE */, "/", this.line, startColumn);
        case "(":
          this.advance();
          return new Token("LPAREN" /* LPAREN */, "(", this.line, startColumn);
        case ")":
          this.advance();
          return new Token("RPAREN" /* RPAREN */, ")", this.line, startColumn);
        case ",":
          this.advance();
          return new Token("COMMA" /* COMMA */, ",", this.line, startColumn);
        case ";":
          this.advance();
          return new Token("SEMICOLON" /* SEMICOLON */, ";", this.line, startColumn);
        case ":":
          this.advance();
          if (this.currentChar === "=") {
            this.advance();
            return new Token("ASSIGN" /* ASSIGN */, ":=", this.line, startColumn);
          }
          return new Token("COLON" /* COLON */, ":", this.line, startColumn);
        case ">":
          this.advance();
          if (this.currentChar === "=") {
            this.advance();
            return new Token("GTE" /* GTE */, ">=", this.line, startColumn);
          }
          return new Token("GT" /* GT */, ">", this.line, startColumn);
        case "<":
          this.advance();
          if (this.currentChar === "=") {
            this.advance();
            return new Token("LTE" /* LTE */, "<=", this.line, startColumn);
          } else if (this.currentChar === ">") {
            this.advance();
            return new Token("NEQ" /* NEQ */, "<>", this.line, startColumn);
          }
          return new Token("LT" /* LT */, "<", this.line, startColumn);
        case "=":
          this.advance();
          return new Token("EQ" /* EQ */, "=", this.line, startColumn);
        default:
          return null;
      }
    }
  };

  // web/vendor/formula-ts/src/parser/ast/nodes.ts
  function isOutputDeclaration(node) {
    return node.type === "OutputDeclaration" /* OutputDeclaration */;
  }

  // web/vendor/formula-ts/src/parser/Parser.ts
  var Parser = class {
    tokens;
    current = 0;
    /**
     * Creates a new Parser instance
     * @param tokens Array of tokens to parse
     */
    constructor(tokens) {
      this.tokens = tokens;
    }
    /**
     * Parses the tokens and returns a Program AST node
     * @returns The root Program node of the AST
     * @throws {ParserError} If the syntax is invalid
     */
    parse() {
      const statements = [];
      this.skipNewlines();
      while (!this.isAtEnd()) {
        if (this.check("NEWLINE" /* NEWLINE */)) {
          this.advance();
          continue;
        }
        statements.push(this.parseStatement());
        this.skipNewlines();
      }
      return {
        type: "Program" /* Program */,
        body: statements
      };
    }
    /**
     * Parses a single statement
     * Handles variable declarations and output declarations
     */
    parseStatement() {
      if (this.check("IDENTIFIER" /* IDENTIFIER */) && this.peekNext()?.type === "ASSIGN" /* ASSIGN */) {
        return this.parseVariableDeclaration();
      }
      if (this.check("IDENTIFIER" /* IDENTIFIER */) && this.peekNext()?.type === "COLON" /* COLON */) {
        return this.parseOutputDeclaration();
      }
      const expression = this.parseExpression();
      this.consume("SEMICOLON" /* SEMICOLON */, "Expected ; after expression statement");
      const statement = {
        type: "ExpressionStatement" /* ExpressionStatement */,
        expression
      };
      return statement;
    }
    /**
     * Parses a variable declaration: VAR := expr;
     */
    parseVariableDeclaration() {
      const nameToken = this.consume("IDENTIFIER" /* IDENTIFIER */, "Expected variable name");
      const name = nameToken.value;
      this.consume("ASSIGN" /* ASSIGN */, "Expected := in variable declaration");
      const value = this.parseExpression();
      this.consume("SEMICOLON" /* SEMICOLON */, "Expected ; after variable declaration");
      return {
        type: "VariableDeclaration" /* VariableDeclaration */,
        name,
        value
      };
    }
    /**
     * Parses an output declaration: NAME: expr, STYLE;
     */
    parseOutputDeclaration() {
      const nameToken = this.consume("IDENTIFIER" /* IDENTIFIER */, "Expected output name");
      const name = nameToken.value;
      this.consume("COLON" /* COLON */, "Expected : in output declaration");
      const value = this.parseExpression();
      let style = void 0;
      if (this.check("COMMA" /* COMMA */)) {
        style = this.parseDrawingStyle();
      }
      this.consume("SEMICOLON" /* SEMICOLON */, "Expected ; after output declaration");
      return {
        type: "OutputDeclaration" /* OutputDeclaration */,
        name,
        value,
        style
      };
    }
    /**
     * Parses drawing style attributes: COLORRED, LINETHICK2, etc.
     */
    parseDrawingStyle() {
      const style = {};
      while (this.check("COMMA" /* COMMA */)) {
        this.advance();
        if (this.check("COLOR" /* COLOR */)) {
          const colorToken = this.advance();
          style.color = colorToken.value;
        } else if (this.check("LINETHICK" /* LINETHICK */)) {
          const thickToken = this.advance();
          const thickness = parseInt(thickToken.value.slice(-1), 10);
          style.size = thickness;
        } else if (this.check("DOTLINE" /* DOTLINE */)) {
          this.advance();
          style.lineStyle = "dotted";
          style.italic = true;
        } else if (this.check("STICK" /* STICK */)) {
          this.advance();
          style.drawMethod = "stick";
          style.bold = true;
        } else if (this.check("COLORSTICK" /* COLORSTICK */)) {
          this.advance();
          style.drawMethod = "colorstick";
        } else if (this.check("VOLSTICK" /* VOLSTICK */)) {
          this.advance();
          style.drawMethod = "volstick";
        } else if (this.check("NODRAW" /* NODRAW */)) {
          this.advance();
          style.hidden = true;
        } else if (this.check("SEMICOLON" /* SEMICOLON */)) {
          break;
        } else {
          throw this.error("Expected drawing style attribute");
        }
      }
      return style;
    }
    /**
     * Parses an expression
     * Entry point for expression parsing with operator precedence
     */
    parseExpression() {
      return this.parseLogicalOr();
    }
    /**
     * Parses logical OR expressions (lowest precedence)
     * LogicalOr -> LogicalAnd (OR LogicalAnd)*
     */
    parseLogicalOr() {
      let left = this.parseLogicalAnd();
      while (this.check("OR" /* OR */)) {
        this.advance();
        const right = this.parseLogicalAnd();
        const binaryExpr = {
          type: "BinaryExpression" /* BinaryExpression */,
          left,
          operator: "||" /* Or */,
          right
        };
        left = binaryExpr;
      }
      return left;
    }
    /**
     * Parses logical AND expressions
     * LogicalAnd -> Comparison (AND Comparison)*
     */
    parseLogicalAnd() {
      let left = this.parseComparison();
      while (this.check("AND" /* AND */)) {
        this.advance();
        const right = this.parseComparison();
        const binaryExpr = {
          type: "BinaryExpression" /* BinaryExpression */,
          left,
          operator: "&&" /* And */,
          right
        };
        left = binaryExpr;
      }
      return left;
    }
    /**
     * Parses comparison expressions
     * Comparison -> Addition (ComparisonOp Addition)*
     */
    parseComparison() {
      let left = this.parseAddition();
      while (this.check("GT" /* GT */) || this.check("LT" /* LT */) || this.check("GTE" /* GTE */) || this.check("LTE" /* LTE */) || this.check("EQ" /* EQ */) || this.check("NEQ" /* NEQ */)) {
        const operatorToken = this.advance();
        const operator = this.tokenTypeToBinaryOperator(operatorToken.type);
        const right = this.parseAddition();
        const binaryExpr = {
          type: "BinaryExpression" /* BinaryExpression */,
          left,
          operator,
          right
        };
        left = binaryExpr;
      }
      return left;
    }
    /**
     * Parses addition and subtraction expressions
     * Addition -> Multiplication ((PLUS | MINUS) Multiplication)*
     */
    parseAddition() {
      let left = this.parseMultiplication();
      while (this.check("PLUS" /* PLUS */) || this.check("MINUS" /* MINUS */)) {
        const operatorToken = this.advance();
        const operator = operatorToken.type === "PLUS" /* PLUS */ ? "+" /* Plus */ : "-" /* Minus */;
        const right = this.parseMultiplication();
        const binaryExpr = {
          type: "BinaryExpression" /* BinaryExpression */,
          left,
          operator,
          right
        };
        left = binaryExpr;
      }
      return left;
    }
    /**
     * Parses multiplication and division expressions
     * Multiplication -> Unary ((MULTIPLY | DIVIDE) Unary)*
     */
    parseMultiplication() {
      let left = this.parseUnary();
      while (this.check("MULTIPLY" /* MULTIPLY */) || this.check("DIVIDE" /* DIVIDE */)) {
        const operatorToken = this.advance();
        const operator = operatorToken.type === "MULTIPLY" /* MULTIPLY */ ? "*" /* Multiply */ : "/" /* Divide */;
        const right = this.parseUnary();
        const binaryExpr = {
          type: "BinaryExpression" /* BinaryExpression */,
          left,
          operator,
          right
        };
        left = binaryExpr;
      }
      return left;
    }
    /**
     * Parses unary expressions
     * Unary -> (MINUS) Unary | Primary
     */
    parseUnary() {
      if (this.check("MINUS" /* MINUS */)) {
        this.advance();
        const operand = this.parseUnary();
        const unaryExpr = {
          type: "UnaryExpression" /* UnaryExpression */,
          operator: "-" /* Minus */,
          operand
        };
        return unaryExpr;
      }
      return this.parsePrimary();
    }
    /**
     * Parses primary expressions
     * Primary -> NUMBER | IDENTIFIER | FunctionCall | LPAREN Expression RPAREN
     */
    parsePrimary() {
      if (this.check("NUMBER" /* NUMBER */)) {
        const token = this.advance();
        const numLiteral = {
          type: "NumberLiteral" /* NumberLiteral */,
          value: parseFloat(token.value)
        };
        return numLiteral;
      }
      if (this.check("STRING" /* STRING */)) {
        const token = this.advance();
        const stringLiteral = {
          type: "StringLiteral" /* StringLiteral */,
          value: token.value
        };
        return stringLiteral;
      }
      if (this.check("IDENTIFIER" /* IDENTIFIER */) || this.check("IF" /* IF */)) {
        const nameToken = this.advance();
        const name = nameToken.value;
        if (this.check("LPAREN" /* LPAREN */)) {
          return this.parseFunctionCall(name);
        }
        const identifier = {
          type: "Identifier" /* Identifier */,
          name
        };
        return identifier;
      }
      if (this.check("LPAREN" /* LPAREN */)) {
        this.advance();
        const expr = this.parseExpression();
        this.consume("RPAREN" /* RPAREN */, "Expected ) after expression");
        return expr;
      }
      throw this.error("Expected expression");
    }
    /**
     * Parses a function call: NAME(arg1, arg2, ...)
     * Assumes the function name has already been consumed
     */
    parseFunctionCall(name) {
      this.consume("LPAREN" /* LPAREN */, "Expected ( after function name");
      const args = [];
      if (!this.check("RPAREN" /* RPAREN */)) {
        do {
          if (this.check("COMMA" /* COMMA */)) {
            this.advance();
          }
          if (this.check("RPAREN" /* RPAREN */) || this.check("COMMA" /* COMMA */)) {
            throw this.error("Expected expression in function arguments");
          }
          args.push(this.parseExpression());
        } while (this.check("COMMA" /* COMMA */));
      }
      this.consume("RPAREN" /* RPAREN */, "Expected ) after function arguments");
      const functionCall = {
        type: "FunctionCall" /* FunctionCall */,
        name,
        arguments: args
      };
      return functionCall;
    }
    /**
     * Converts TokenType to BinaryOperator
     */
    tokenTypeToBinaryOperator(type) {
      switch (type) {
        case "PLUS" /* PLUS */:
          return "+" /* Plus */;
        case "MINUS" /* MINUS */:
          return "-" /* Minus */;
        case "MULTIPLY" /* MULTIPLY */:
          return "*" /* Multiply */;
        case "DIVIDE" /* DIVIDE */:
          return "/" /* Divide */;
        case "GT" /* GT */:
          return ">" /* GreaterThan */;
        case "LT" /* LT */:
          return "<" /* LessThan */;
        case "GTE" /* GTE */:
          return ">=" /* GreaterThanOrEqual */;
        case "LTE" /* LTE */:
          return "<=" /* LessThanOrEqual */;
        case "EQ" /* EQ */:
          return "==" /* Equal */;
        case "NEQ" /* NEQ */:
          return "!=" /* NotEqual */;
        case "AND" /* AND */:
          return "&&" /* And */;
        case "OR" /* OR */:
          return "||" /* Or */;
        default:
          throw this.error(`Unknown binary operator: ${type}`);
      }
    }
    /**
     * Skips any newline tokens
     */
    skipNewlines() {
      while (this.check("NEWLINE" /* NEWLINE */)) {
        this.advance();
      }
    }
    /**
     * Checks if the current token is of the specified type
     */
    check(type) {
      if (this.isAtEnd()) return false;
      return this.peek().type === type;
    }
    /**
     * Advances to the next token and returns the previous one
     */
    advance() {
      if (!this.isAtEnd()) {
        this.current++;
      }
      return this.previous();
    }
    /**
     * Checks if we're at the end of the token stream
     */
    isAtEnd() {
      return this.peek().type === "EOF" /* EOF */;
    }
    /**
     * Returns the current token without consuming it
     */
    peek() {
      return this.tokens[this.current];
    }
    /**
     * Returns the next token without consuming current
     */
    peekNext() {
      if (this.current + 1 < this.tokens.length) {
        return this.tokens[this.current + 1];
      }
      return void 0;
    }
    /**
     * Returns the previous token
     */
    previous() {
      return this.tokens[this.current - 1];
    }
    /**
     * Consumes a token of the expected type or throws an error
     */
    consume(type, message) {
      if (this.check(type)) {
        return this.advance();
      }
      throw this.error(message);
    }
    /**
     * Creates a ParserError with the current token's position
     */
    error(message) {
      const token = this.peek();
      return new ParserError(message, token.line, token.column);
    }
  };

  // web/vendor/formula-ts/src/interpreter/functions/math.ts
  function MA(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      result[i] = sum / period;
    }
    return result;
  }
  function EMA(data, period) {
    const result = new Array(data.length);
    const multiplier = 2 / (period + 1);
    for (let i = 0; i < data.length; i++) {
      if (i === 0) {
        result[i] = data[i];
      } else {
        result[i] = (data[i] - result[i - 1]) * multiplier + result[i - 1];
      }
    }
    return result;
  }
  function SUM(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      result[i] = sum;
    }
    return result;
  }
  function MAX(a, b) {
    const length = Math.min(a.length, b.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      result[i] = Math.max(a[i], b[i]);
    }
    return result;
  }
  function MIN(a, b) {
    const length = Math.min(a.length, b.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      result[i] = Math.min(a[i], b[i]);
    }
    return result;
  }
  function ABS(data) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      result[i] = Math.abs(data[i]);
    }
    return result;
  }
  function SQRT(data) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      result[i] = Math.sqrt(data[i]);
    }
    return result;
  }
  function POW(base, exponent) {
    const length = Math.min(base.length, exponent.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      result[i] = Math.pow(base[i], exponent[i]);
    }
    return result;
  }
  function MOD(dividend, divisor) {
    const length = Math.min(dividend.length, divisor.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      if (divisor[i] === 0) {
        result[i] = NaN;
      } else {
        result[i] = dividend[i] % divisor[i];
      }
    }
    return result;
  }
  function ROUND(data) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      result[i] = Math.round(data[i]);
    }
    return result;
  }
  function EXP(data) {
    return data.map((value) => Math.exp(value));
  }
  function LN(data) {
    return data.map((value) => Math.log(value));
  }
  function LOG(data) {
    return data.map((value) => Math.log10(value));
  }
  function CEILING(data) {
    return data.map((value) => Math.ceil(value));
  }
  function FLOOR(data) {
    return data.map((value) => Math.floor(value));
  }
  function INTPART(data) {
    return data.map((value) => Math.trunc(value));
  }
  function FRACPART(data) {
    return data.map((value) => value - Math.trunc(value));
  }
  function ROUND2(data, digits) {
    const scale = Math.pow(10, digits);
    return data.map((value) => Math.round(value * scale) / scale);
  }
  function SIGN(data) {
    return data.map((value) => {
      if (value > 0) return 1;
      if (value < 0) return -1;
      return 0;
    });
  }
  function SIN(data) {
    return data.map((value) => Math.sin(value));
  }
  function COS(data) {
    return data.map((value) => Math.cos(value));
  }
  function TAN(data) {
    return data.map((value) => Math.tan(value));
  }
  function ASIN(data) {
    return data.map((value) => Math.asin(value));
  }
  function ACOS(data) {
    return data.map((value) => Math.acos(value));
  }
  function ATAN(data) {
    return data.map((value) => Math.atan(value));
  }

  // web/vendor/formula-ts/src/interpreter/functions/reference.ts
  function dynamicPeriod(periods, index) {
    const value = periods.length === 1 ? periods[0] : periods[index];
    if (!Number.isFinite(value)) {
      return 0;
    }
    return Math.max(0, Math.floor(value));
  }
  function DYNAMIC_REF(data, periods) {
    return data.map((_, index) => {
      const period = dynamicPeriod(periods, index);
      return index < period ? NaN : data[index - period];
    });
  }
  function DYNAMIC_REFX(data, periods) {
    return data.map((_, index) => {
      const target = index + dynamicPeriod(periods, index);
      return target >= data.length ? NaN : data[target];
    });
  }
  function dynamicExtreme(data, periods, highest, bars) {
    return data.map((_, index) => {
      const period = dynamicPeriod(periods, index);
      const start = period === 0 ? 0 : index - period + 1;
      if (start < 0) {
        return NaN;
      }
      let selected = data[index];
      let selectedBars = 0;
      for (let cursor = index - 1; cursor >= start; cursor--) {
        if (highest && data[cursor] > selected || !highest && data[cursor] < selected) {
          selected = data[cursor];
          selectedBars = index - cursor;
        }
      }
      return bars ? selectedBars : selected;
    });
  }
  function DYNAMIC_HHV(data, periods) {
    return dynamicExtreme(data, periods, true, false);
  }
  function DYNAMIC_LLV(data, periods) {
    return dynamicExtreme(data, periods, false, false);
  }
  function DYNAMIC_HHVBARS(data, periods) {
    return dynamicExtreme(data, periods, true, true);
  }
  function DYNAMIC_LLVBARS(data, periods) {
    return dynamicExtreme(data, periods, false, true);
  }
  function BACKSET(condition, periods) {
    const result = new Array(condition.length).fill(0);
    for (let index = 0; index < condition.length; index++) {
      if (!condition[index] || Number.isNaN(condition[index])) continue;
      const period = Math.max(1, dynamicPeriod(periods, index));
      for (let cursor = Math.max(0, index - period + 1); cursor <= index; cursor++) result[cursor] = 1;
    }
    return result;
  }
  function zigPoints(data, percentage) {
    if (data.length === 0) return [];
    const threshold = Math.max(0, percentage) / 100;
    let high = 0;
    let low = 0;
    let direction = 0;
    const points = [];
    for (let index = 1; index < data.length; index++) {
      if (data[index] >= data[high]) high = index;
      if (data[index] <= data[low]) low = index;
      if (direction === 0) {
        if (data[index] >= data[low] * (1 + threshold)) {
          points.push({ index: low, kind: "trough" });
          direction = 1;
          high = index;
        } else if (data[index] <= data[high] * (1 - threshold)) {
          points.push({ index: high, kind: "peak" });
          direction = -1;
          low = index;
        }
      } else if (direction > 0 && data[index] <= data[high] * (1 - threshold)) {
        points.push({ index: high, kind: "peak" });
        direction = -1;
        low = index;
      } else if (direction < 0 && data[index] >= data[low] * (1 + threshold)) {
        points.push({ index: low, kind: "trough" });
        direction = 1;
        high = index;
      }
    }
    const finalIndex = direction >= 0 ? high : low;
    const finalKind = direction >= 0 ? "peak" : "trough";
    if (points[points.length - 1]?.index !== finalIndex) points.push({ index: finalIndex, kind: finalKind });
    return points;
  }
  function ZIG(data, percentage) {
    const points = zigPoints(data, percentage);
    if (points.length === 0) return [];
    const result = new Array(data.length).fill(NaN);
    for (let part = 0; part < points.length - 1; part++) {
      const from = points[part].index;
      const to = points[part + 1].index;
      for (let index = from; index <= to; index++) {
        const ratio = to === from ? 0 : (index - from) / (to - from);
        result[index] = data[from] + (data[to] - data[from]) * ratio;
      }
    }
    const first = points[0].index;
    const last = points[points.length - 1].index;
    for (let index = 0; index < first; index++) result[index] = data[first];
    for (let index = last; index < data.length; index++) result[index] = data[last];
    return result;
  }
  function ZIG_TURN(data, percentage, order, kind, bars) {
    const points = zigPoints(data, percentage).filter((point) => point.kind === kind);
    const wanted = Math.max(1, Math.floor(order));
    let cursor = 0;
    const eligible = [];
    return data.map((_, index) => {
      while (cursor < points.length && points[cursor].index <= index) eligible.push(points[cursor++]);
      const point = eligible[eligible.length - wanted];
      if (!point) return NaN;
      return bars ? index - point.index : data[point.index];
    });
  }

  // web/vendor/formula-ts/src/interpreter/functions/logical.ts
  function IF(condition, a, b) {
    const length = Math.min(condition.length, a.length, b.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      result[i] = condition[i] !== 0 && !Number.isNaN(condition[i]) ? a[i] : b[i];
    }
    return result;
  }
  function IFN(condition, a, b) {
    return IF(condition, b, a);
  }
  function CROSS(a, b) {
    const length = Math.min(a.length, b.length);
    const result = new Array(length);
    result[0] = 0;
    for (let i = 1; i < length; i++) {
      const wasBelowPreviously = a[i - 1] < b[i - 1];
      const isAboveOrEqualNow = a[i] >= b[i];
      result[i] = wasBelowPreviously && isAboveOrEqualNow ? 1 : 0;
    }
    return result;
  }
  function LONGCROSS(a, b, period) {
    const length = Math.min(a.length, b.length);
    const n = Math.floor(period);
    const result = new Array(length).fill(0);
    for (let i = 1; i < length; i++) {
      if (!(a[i - 1] <= b[i - 1] && a[i] > b[i]) || i < n) {
        continue;
      }
      let stayedBelow = true;
      for (let j = 1; j <= n; j++) {
        if (a[i - j] >= b[i - j]) {
          stayedBelow = false;
          break;
        }
      }
      result[i] = stayedBelow ? 1 : 0;
    }
    return result;
  }
  function EVERY(data, period) {
    const result = new Array(data.length).fill(0);
    const n = Math.floor(period);
    for (let i = n - 1; i < data.length; i++) {
      let allNonZero = true;
      for (let j = i - n + 1; j <= i; j++) {
        if (data[j] === 0 || Number.isNaN(data[j])) {
          allNonZero = false;
          break;
        }
      }
      result[i] = allNonZero ? 1 : 0;
    }
    return result;
  }
  function EXIST(data, period) {
    const result = new Array(data.length).fill(0);
    const n = Math.floor(period);
    for (let i = n - 1; i < data.length; i++) {
      let hasNonZero = false;
      for (let j = i - n + 1; j <= i; j++) {
        if (data[j] !== 0 && !Number.isNaN(data[j])) {
          hasNonZero = true;
          break;
        }
      }
      result[i] = hasNonZero ? 1 : 0;
    }
    return result;
  }
  function BARSLAST(data) {
    const result = new Array(data.length);
    let lastNonZeroIndex = -1;
    for (let i = 0; i < data.length; i++) {
      if (data[i] !== 0 && !Number.isNaN(data[i])) {
        result[i] = 0;
        lastNonZeroIndex = i;
      } else if (lastNonZeroIndex >= 0) {
        result[i] = i - lastNonZeroIndex;
      } else {
        result[i] = NaN;
      }
    }
    return result;
  }
  function BARSLASTCOUNT(data) {
    const result = new Array(data.length);
    let count = 0;
    for (let i = 0; i < data.length; i++) {
      if (data[i] !== 0 && !Number.isNaN(data[i])) {
        count++;
      } else {
        count = 0;
      }
      result[i] = count;
    }
    return result;
  }
  function COUNT(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < period - 1; i++) {
      result[i] = NaN;
    }
    for (let i = period - 1; i < data.length; i++) {
      let count = 0;
      for (let j = i - period + 1; j <= i; j++) {
        if (data[j] !== 0 && !Number.isNaN(data[j])) {
          count++;
        }
      }
      result[i] = count;
    }
    return result;
  }
  function FILTER(data, period) {
    const n = Math.floor(period);
    const result = new Array(data.length).fill(0);
    let lastSignal = -n - 1;
    for (let i = 0; i < data.length; i++) {
      if (data[i] !== 0 && !Number.isNaN(data[i]) && i - lastSignal >= n) {
        result[i] = 1;
        lastSignal = i;
      }
    }
    return result;
  }
  function NOT(data) {
    return data.map((value) => value !== 0 && !Number.isNaN(value) ? 0 : 1);
  }

  // web/vendor/formula-ts/src/interpreter/functions/statistics.ts
  function STD(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      const mean = sum / period;
      let variance2 = 0;
      for (let j = 0; j < period; j++) {
        const diff = data[i - j] - mean;
        variance2 += diff * diff;
      }
      variance2 /= period;
      result[i] = Math.sqrt(variance2);
    }
    return result;
  }
  function VAR(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      const mean = sum / period;
      let variance2 = 0;
      for (let j = 0; j < period; j++) {
        const diff = data[i - j] - mean;
        variance2 += diff * diff;
      }
      variance2 /= period;
      result[i] = variance2;
    }
    return result;
  }
  function VARP(data, period) {
    return VAR(data, period);
  }
  function STDP(data, period) {
    return STD(data, period);
  }
  function STDDEV(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      const mean = sum / period;
      let variance2 = 0;
      for (let j = 0; j < period; j++) {
        const diff = data[i - j] - mean;
        variance2 += diff * diff;
      }
      result[i] = period < 2 ? 0 : Math.sqrt(variance2 / (period - 1));
    }
    return result;
  }
  function DEVSQ(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      const mean = sum / period;
      let devsq = 0;
      for (let j = 0; j < period; j++) {
        const diff = data[i - j] - mean;
        devsq += diff * diff;
      }
      result[i] = devsq;
    }
    return result;
  }
  function FORCAST(data, period) {
    return rollingRegression(data, period, (slope, intercept, n) => intercept + slope * (n - 1));
  }
  function SLOPE(data, period) {
    return rollingRegression(data, period, (slope) => slope);
  }
  function COVAR(a, b, period) {
    return rollingPairStats(a, b, period, covariance);
  }
  function RELATE(a, b, period) {
    return rollingPairStats(a, b, period, (windowA, windowB) => {
      const cov = covariance(windowA, windowB);
      const varA = variance(windowA);
      const varB = variance(windowB);
      return varA === 0 || varB === 0 ? NaN : cov / Math.sqrt(varA * varB);
    });
  }
  function BETA(a, b, period) {
    return rollingPairStats(a, b, period, (windowA, windowB) => {
      const varB = variance(windowB);
      return varB === 0 ? NaN : covariance(windowA, windowB) / varB;
    });
  }
  function MEDIAN(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      const window = [];
      for (let j = 0; j < period; j++) {
        window.push(data[i - j]);
      }
      window.sort((a, b) => a - b);
      const mid = Math.floor(period / 2);
      if (period % 2 === 1) {
        result[i] = window[mid];
      } else {
        result[i] = (window[mid - 1] + window[mid]) / 2;
      }
    }
    return result;
  }
  function AVEDEV(data, period) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      let sum = 0;
      for (let j = 0; j < period; j++) {
        sum += data[i - j];
      }
      const mean = sum / period;
      let sumAbsDev = 0;
      for (let j = 0; j < period; j++) {
        sumAbsDev += Math.abs(data[i - j] - mean);
      }
      const avedev = sumAbsDev / period;
      result[i] = avedev;
    }
    return result;
  }
  function rollingRegression(data, period, fn) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      const window = data.slice(i - period + 1, i + 1);
      const { slope, intercept } = linearRegression(window);
      result[i] = fn(slope, intercept, window.length);
    }
    return result;
  }
  function rollingPairStats(a, b, period, fn) {
    const length = Math.min(a.length, b.length);
    const result = new Array(length);
    for (let i = 0; i < length; i++) {
      if (i < period - 1) {
        result[i] = NaN;
        continue;
      }
      result[i] = fn(a.slice(i - period + 1, i + 1), b.slice(i - period + 1, i + 1));
    }
    return result;
  }
  function average(values) {
    return values.reduce((sum, value) => sum + value, 0) / values.length;
  }
  function variance(values) {
    const mean = average(values);
    return values.reduce((sum, value) => {
      const diff = value - mean;
      return sum + diff * diff;
    }, 0) / values.length;
  }
  function covariance(a, b) {
    const meanA = average(a);
    const meanB = average(b);
    let sum = 0;
    for (let i = 0; i < a.length; i++) {
      sum += (a[i] - meanA) * (b[i] - meanB);
    }
    return sum / a.length;
  }
  function linearRegression(values) {
    const n = values.length;
    let sumX = 0;
    let sumY = 0;
    let sumXY = 0;
    let sumXX = 0;
    for (let i = 0; i < values.length; i++) {
      const x = i;
      const y = values[i];
      sumX += x;
      sumY += y;
      sumXY += x * y;
      sumXX += x * x;
    }
    const denominator = n * sumXX - sumX * sumX;
    if (denominator === 0) {
      return { slope: 0, intercept: values[values.length - 1] };
    }
    const slope = (n * sumXY - sumX * sumY) / denominator;
    const intercept = (sumY - slope * sumX) / n;
    return { slope, intercept };
  }

  // web/vendor/formula-ts/src/interpreter/functions/technical.ts
  function SMA(data, N, M) {
    const result = new Array(data.length);
    if (data.length === 0) {
      return result;
    }
    result[0] = data[0];
    for (let i = 1; i < data.length; i++) {
      result[i] = (M * data[i] + (N - M) * result[i - 1]) / N;
    }
    return result;
  }
  function WMA(data, N) {
    const result = new Array(data.length);
    const weightSum = N * (N + 1) / 2;
    for (let i = 0; i < data.length; i++) {
      if (i < N - 1) {
        result[i] = NaN;
        continue;
      }
      let weightedSum = 0;
      for (let j = 0; j < N; j++) {
        const weight = j + 1;
        weightedSum += data[i - N + 1 + j] * weight;
      }
      result[i] = weightedSum / weightSum;
    }
    return result;
  }
  function DMA(data, alpha) {
    const result = new Array(data.length);
    if (data.length === 0) {
      return result;
    }
    result[0] = data[0];
    for (let i = 1; i < data.length; i++) {
      const currentAlpha = alpha[Math.min(i, alpha.length - 1)];
      result[i] = currentAlpha * data[i] + (1 - currentAlpha) * result[i - 1];
    }
    return result;
  }
  function CONST(data) {
    if (data.length === 0) {
      return [];
    }
    return new Array(data.length).fill(data[data.length - 1]);
  }
  function RSI(data, N) {
    const result = new Array(data.length);
    for (let i = 0; i < N; i++) {
      result[i] = NaN;
    }
    if (data.length <= N) {
      return result;
    }
    let avgGain = 0;
    let avgLoss = 0;
    for (let i = 1; i <= N; i++) {
      const change = data[i] - data[i - 1];
      if (change > 0) {
        avgGain += change;
      } else {
        avgLoss += Math.abs(change);
      }
    }
    avgGain /= N;
    avgLoss /= N;
    const rs = avgLoss === 0 ? avgGain > 0 ? Infinity : 0 : avgGain / avgLoss;
    result[N] = avgLoss === 0 && avgGain === 0 ? 50 : 100 - 100 / (1 + rs);
    for (let i = N + 1; i < data.length; i++) {
      const change = data[i] - data[i - 1];
      const gain = change > 0 ? change : 0;
      const loss = change < 0 ? Math.abs(change) : 0;
      avgGain = (avgGain * (N - 1) + gain) / N;
      avgLoss = (avgLoss * (N - 1) + loss) / N;
      const currentRS = avgLoss === 0 ? avgGain > 0 ? Infinity : 0 : avgGain / avgLoss;
      result[i] = avgLoss === 0 && avgGain === 0 ? 50 : 100 - 100 / (1 + currentRS);
    }
    return result;
  }

  // web/vendor/formula-ts/src/interpreter/functions/pattern.ts
  function UPNDAY(close, n) {
    const period = Math.floor(n);
    const result = new Array(close.length).fill(0);
    for (let i = period; i < close.length; i++) {
      let isUpN = true;
      for (let j = 0; j < period; j++) {
        if (close[i - j] <= close[i - j - 1]) {
          isUpN = false;
          break;
        }
      }
      result[i] = isUpN ? 1 : 0;
    }
    return result;
  }
  function DOWNNDAY(close, n) {
    const period = Math.floor(n);
    const result = new Array(close.length).fill(0);
    for (let i = period; i < close.length; i++) {
      let isDownN = true;
      for (let j = 0; j < period; j++) {
        if (close[i - j] >= close[i - j - 1]) {
          isDownN = false;
          break;
        }
      }
      result[i] = isDownN ? 1 : 0;
    }
    return result;
  }
  function NDAY(cond, n) {
    const period = Math.floor(n);
    const result = new Array(cond.length).fill(0);
    for (let i = period - 1; i < cond.length; i++) {
      let allTrue = true;
      for (let j = 0; j < period; j++) {
        if (cond[i - j] === 0) {
          allTrue = false;
          break;
        }
      }
      result[i] = allTrue ? 1 : 0;
    }
    return result;
  }
  function NDAY_AB(a, b, n) {
    const period = Math.floor(n);
    const length = Math.min(a.length, b.length);
    const result = new Array(length).fill(0);
    for (let i = period - 1; i < length; i++) {
      let allGreater = true;
      for (let j = 0; j < period; j++) {
        if (!(a[i - j] > b[i - j])) {
          allGreater = false;
          break;
        }
      }
      result[i] = allGreater ? 1 : 0;
    }
    return result;
  }
  function LAST(condition, from, to) {
    const fromN = Math.floor(from);
    const toN = Math.floor(to);
    const result = new Array(condition.length).fill(0);
    for (let i = fromN; i < condition.length; i++) {
      let allTrue = true;
      for (let j = toN; j <= fromN; j++) {
        if (condition[i - j] === 0 || Number.isNaN(condition[i - j])) {
          allTrue = false;
          break;
        }
      }
      result[i] = allTrue ? 1 : 0;
    }
    return result;
  }
  function EXISTR(condition, from, to) {
    const fromN = Math.floor(from);
    const toN = Math.floor(to);
    const result = new Array(condition.length).fill(0);
    for (let i = fromN; i < condition.length; i++) {
      for (let j = toN; j <= fromN; j++) {
        if (condition[i - j] !== 0 && !Number.isNaN(condition[i - j])) {
          result[i] = 1;
          break;
        }
      }
    }
    return result;
  }
  function RANGE(A, B, C) {
    const len = Math.max(A.length, B.length, C.length);
    const result = new Array(len).fill(0);
    for (let i = 0; i < len; i++) {
      const a = A[Math.min(i, A.length - 1)];
      const b = B[Math.min(i, B.length - 1)];
      const c = C[Math.min(i, C.length - 1)];
      const min = Math.min(b, c);
      const max = Math.max(b, c);
      result[i] = a >= min && a <= max ? 1 : 0;
    }
    return result;
  }
  function BETWEEN(A, B, C) {
    return RANGE(A, B, C);
  }

  // web/vendor/formula-ts/src/interpreter/functions/chip.ts
  function WINNER(close, volume, targetPrice, lookback = 100) {
    const result = new Array(close.length).fill(0);
    for (let i = 0; i < close.length; i++) {
      const startIdx = Math.max(0, i - lookback + 1);
      let totalVolume = 0;
      let winVolume = 0;
      for (let j = startIdx; j <= i; j++) {
        totalVolume += volume[j];
        if (close[j] < targetPrice[i]) {
          winVolume += volume[j];
        }
      }
      result[i] = totalVolume > 0 ? winVolume / totalVolume : 0;
    }
    return result;
  }
  function LWINNER(close, volume, targetPrice, lookback = 20) {
    return WINNER(close, volume, targetPrice, lookback);
  }
  function COST(close, volume, percent, lookback = 100) {
    const result = new Array(close.length).fill(0);
    for (let i = 0; i < close.length; i++) {
      const startIdx = Math.max(0, i - lookback + 1);
      const priceVolumePairs = [];
      let totalVolume = 0;
      for (let j = startIdx; j <= i; j++) {
        priceVolumePairs.push({ price: close[j], volume: volume[j] });
        totalVolume += volume[j];
      }
      priceVolumePairs.sort((a, b) => a.price - b.price);
      const targetVolume = totalVolume * percent[i] / 100;
      let cumulativeVolume = 0;
      for (const pair of priceVolumePairs) {
        cumulativeVolume += pair.volume;
        if (cumulativeVolume >= targetVolume) {
          result[i] = pair.price;
          break;
        }
      }
      if (result[i] === 0 && priceVolumePairs.length > 0) {
        result[i] = priceVolumePairs[priceVolumePairs.length - 1].price;
      }
    }
    return result;
  }
  function VALUEWHEN(cond, X) {
    const result = new Array(cond.length).fill(0);
    let lastValue = 0;
    for (let i = 0; i < cond.length; i++) {
      if (cond[i] !== 0) {
        lastValue = X[Math.min(i, X.length - 1)];
      }
      result[i] = lastValue;
    }
    return result;
  }
  function TOPRANGE(X, period = 20) {
    const lookback = Math.floor(period);
    const result = new Array(X.length).fill(0);
    for (let i = 0; i < X.length; i++) {
      const startIdx = Math.max(0, i - lookback + 1);
      let maxValue = -Infinity;
      for (let j = startIdx; j <= i; j++) {
        if (X[j] > maxValue) {
          maxValue = X[j];
        }
      }
      result[i] = X[i] === maxValue ? 1 : 0;
    }
    return result;
  }
  function LOWRANGE(X, period = 20) {
    const lookback = Math.floor(period);
    const result = new Array(X.length).fill(0);
    for (let i = 0; i < X.length; i++) {
      const startIdx = Math.max(0, i - lookback + 1);
      let minValue = Infinity;
      for (let j = startIdx; j <= i; j++) {
        if (X[j] < minValue) {
          minValue = X[j];
        }
      }
      result[i] = X[i] === minValue ? 1 : 0;
    }
    return result;
  }

  // web/vendor/formula-ts/src/interpreter/functions/marketData.ts
  var OPEN = {
    name: "OPEN",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      return context.getMarketDataField("OPEN");
    }
  };
  var HIGH = {
    name: "HIGH",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      return context.getMarketDataField("HIGH");
    }
  };
  var LOW = {
    name: "LOW",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      return context.getMarketDataField("LOW");
    }
  };
  var CLOSE = {
    name: "CLOSE",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      return context.getMarketDataField("CLOSE");
    }
  };
  var VOL = {
    name: "VOL",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      return context.getMarketDataField("VOLUME");
    }
  };
  var AMOUNT = {
    name: "AMOUNT",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      const result = context.getMarketDataField("AMOUNT");
      if (result.some((v) => isNaN(v))) {
        throw new Error(
          'AMOUNT function requires marketData.amount field. Please provide the "amount" field in your market data.'
        );
      }
      return result;
    }
  };
  var ADVANCE = {
    name: "ADVANCE",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      const result = context.getMarketDataField("ADVANCE");
      if (result.some((v) => isNaN(v))) {
        throw new Error(
          'ADVANCE function requires marketData.advance field. This field is only available for index data. Please provide the "advance" field in your market data.'
        );
      }
      return result;
    }
  };
  var DECLINE = {
    name: "DECLINE",
    minArgs: 0,
    maxArgs: 0,
    execute: (_args, context) => {
      const result = context.getMarketDataField("DECLINE");
      if (result.some((v) => isNaN(v))) {
        throw new Error(
          'DECLINE function requires marketData.decline field. This field is only available for index data. Please provide the "decline" field in your market data.'
        );
      }
      return result;
    }
  };

  // web/vendor/formula-ts/src/interpreter/functions/datetime.ts
  function DATE(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      const date = new Date(timestamps[i]);
      const year = date.getFullYear();
      const month = String(date.getMonth() + 1).padStart(2, "0");
      const day = String(date.getDate()).padStart(2, "0");
      result[i] = parseInt(`${year}${month}${day}`);
    }
    return result;
  }
  function TIME(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      const date = new Date(timestamps[i]);
      const hour = String(date.getHours()).padStart(2, "0");
      const minute = String(date.getMinutes()).padStart(2, "0");
      const second = String(date.getSeconds()).padStart(2, "0");
      result[i] = parseInt(`${hour}${minute}${second}`);
    }
    return result;
  }
  function YEAR(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      result[i] = new Date(timestamps[i]).getFullYear();
    }
    return result;
  }
  function MONTH(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      result[i] = new Date(timestamps[i]).getMonth() + 1;
    }
    return result;
  }
  function DAY(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      result[i] = new Date(timestamps[i]).getDate();
    }
    return result;
  }
  function HOUR(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      result[i] = new Date(timestamps[i]).getHours();
    }
    return result;
  }
  function MINUTE(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      result[i] = new Date(timestamps[i]).getMinutes();
    }
    return result;
  }
  function WEEKDAY(timestamps) {
    const result = new Array(timestamps.length);
    for (let i = 0; i < timestamps.length; i++) {
      const day = new Date(timestamps[i]).getDay();
      result[i] = day === 0 ? 7 : day;
    }
    return result;
  }

  // web/vendor/formula-ts/src/interpreter/functions/period.ts
  function detectPeriod(timestamps) {
    if (timestamps.length < 2) {
      return 101;
    }
    const diffs = [];
    for (let i = 1; i < Math.min(10, timestamps.length); i++) {
      diffs.push(timestamps[i] - timestamps[i - 1]);
    }
    diffs.sort((a, b) => a - b);
    const medianDiff = diffs[Math.floor(diffs.length / 2)];
    const MINUTE2 = 60 * 1e3;
    const HOUR2 = 60 * MINUTE2;
    const DAY2 = 24 * HOUR2;
    if (medianDiff < 1.5 * MINUTE2) return 1;
    if (medianDiff < 7 * MINUTE2) return 5;
    if (medianDiff < 20 * MINUTE2) return 15;
    if (medianDiff < 45 * MINUTE2) return 30;
    if (medianDiff < 1.5 * HOUR2) return 60;
    if (medianDiff < 1.5 * DAY2) return 101;
    if (medianDiff < 8 * DAY2) return 102;
    return 103;
  }
  function PERIOD(timestamps) {
    const period = detectPeriod(timestamps);
    return new Array(timestamps.length).fill(period);
  }
  function BARSCOUNT(data) {
    if (typeof data === "number") {
      return new Array(data).fill(data);
    }
    const result = new Array(data.length);
    let count = 0;
    for (let i = 0; i < data.length; i++) {
      if (!Number.isNaN(data[i])) {
        count++;
      }
      result[i] = count;
    }
    return result;
  }
  function ISLASTBAR(dataLength) {
    const result = new Array(dataLength).fill(0);
    result[dataLength - 1] = 1;
    return result;
  }
  function BARSSINCE(condition) {
    const result = new Array(condition.length);
    let firstTrueIndex = -1;
    for (let i = 0; i < condition.length; i++) {
      if (condition[i] !== 0 && !Number.isNaN(condition[i])) {
        firstTrueIndex = i;
        break;
      }
    }
    if (firstTrueIndex === -1) {
      result.fill(NaN);
      return result;
    }
    for (let i = 0; i < firstTrueIndex; i++) {
      result[i] = NaN;
    }
    for (let i = firstTrueIndex; i < condition.length; i++) {
      result[i] = i - firstTrueIndex;
    }
    return result;
  }
  function CURRBARSCOUNT(dataLength) {
    return Array.from({ length: dataLength }, (_, index) => dataLength - index);
  }
  function TOTALBARSCOUNT(dataLength) {
    return new Array(dataLength).fill(dataLength);
  }
  function SUMBARS(data, target) {
    const result = new Array(data.length);
    for (let i = 0; i < data.length; i++) {
      let sum = 0;
      result[i] = NaN;
      for (let j = i; j >= 0; j--) {
        sum += data[j];
        if (sum >= target) {
          result[i] = i - j + 1;
          break;
        }
      }
    }
    return result;
  }

  // web/vendor/formula-ts/src/interpreter/Interpreter.ts
  var Interpreter = class {
    context;
    /**
     * Create a new interpreter
     * @param context - Execution context with market data and function registry
     */
    constructor(context) {
      this.context = context;
    }
    /**
     * Execute a program (root AST node)
     * @param program - Program AST node
     */
    visitProgram(program) {
      for (const statement of program.body) {
        this.visitStatement(statement);
      }
    }
    /**
     * Visit a statement node
     * @param statement - Statement node
     */
    visitStatement(statement) {
      switch (statement.type) {
        case "VariableDeclaration" /* VariableDeclaration */:
          this.visitVariableDeclaration(statement);
          break;
        case "OutputDeclaration" /* OutputDeclaration */:
          this.visitOutputDeclaration(statement);
          break;
        case "ExpressionStatement" /* ExpressionStatement */:
          const result = this.visitExpression(statement.expression);
          if (Array.isArray(result) && !this.isNumberArray(result)) {
            this.context.addDrawings(result);
          }
          break;
        default:
          throw new Error(`Unknown statement type: ${statement.type}`);
      }
    }
    /**
     * Visit a variable declaration
     * @param node - VariableDeclaration node
     */
    visitVariableDeclaration(node) {
      const value = this.visitExpression(node.value);
      if (this.isDrawingEventArray(value)) {
        this.context.addDrawings(value);
        return;
      }
      this.context.setVariable(node.name, this.expectNumberArray(value, node.name));
    }
    /**
     * Visit an output declaration
     * @param node - OutputDeclaration node
     */
    visitOutputDeclaration(node) {
      const value = this.visitExpression(node.value);
      if (this.isDrawingEventArray(value)) {
        this.context.addDrawings(value);
        return;
      }
      const numericValue = this.expectNumberArray(value, node.name);
      this.context.setOutput(node.name, numericValue);
      this.context.setVariable(node.name, numericValue);
    }
    /**
     * Visit an expression node
     * @param expression - Expression node
     * @returns Evaluated value as number array
     */
    visitExpression(expression) {
      switch (expression.type) {
        case "BinaryExpression" /* BinaryExpression */:
          return this.visitBinaryExpression(expression);
        case "UnaryExpression" /* UnaryExpression */:
          return this.visitUnaryExpression(expression);
        case "FunctionCall" /* FunctionCall */:
          return this.visitFunctionCall(expression);
        case "Identifier" /* Identifier */:
          return this.visitIdentifier(expression);
        case "NumberLiteral" /* NumberLiteral */:
          return this.visitNumberLiteral(expression);
        case "StringLiteral" /* StringLiteral */:
          return this.visitStringLiteral(expression);
        default:
          throw new Error(`Unknown expression type: ${expression.type}`);
      }
    }
    /**
     * Visit a binary expression
     * @param node - BinaryExpression node
     * @returns Evaluated value as number array
     */
    visitBinaryExpression(node) {
      const left = this.expectNumberArray(this.visitExpression(node.left), "binary left operand");
      const right = this.expectNumberArray(this.visitExpression(node.right), "binary right operand");
      const length = Math.min(left.length, right.length);
      const result = new Array(length);
      for (let i = 0; i < length; i++) {
        result[i] = this.evaluateBinaryOperation(left[i], node.operator, right[i]);
      }
      return result;
    }
    /**
     * Evaluate a binary operation on two numbers
     * @param left - Left operand
     * @param operator - Binary operator
     * @param right - Right operand
     * @returns Result of the operation
     */
    evaluateBinaryOperation(left, operator, right) {
      switch (operator) {
        // Arithmetic
        case "+" /* Plus */:
          return left + right;
        case "-" /* Minus */:
          return left - right;
        case "*" /* Multiply */:
          return left * right;
        case "/" /* Divide */:
          return left / right;
        case "%" /* Modulo */:
          return left % right;
        case "^" /* Power */:
          return Math.pow(left, right);
        // Comparison (return 1 for true, 0 for false)
        case "==" /* Equal */:
          return left === right ? 1 : 0;
        case "!=" /* NotEqual */:
          return left !== right ? 1 : 0;
        case "<" /* LessThan */:
          return left < right ? 1 : 0;
        case "<=" /* LessThanOrEqual */:
          return left <= right ? 1 : 0;
        case ">" /* GreaterThan */:
          return left > right ? 1 : 0;
        case ">=" /* GreaterThanOrEqual */:
          return left >= right ? 1 : 0;
        // Logical (treat non-zero as true)
        case "&&" /* And */:
          return this.isTruthy(left) && this.isTruthy(right) ? 1 : 0;
        case "||" /* Or */:
          return this.isTruthy(left) || this.isTruthy(right) ? 1 : 0;
        default:
          throw new Error(`Unknown binary operator: ${operator}`);
      }
    }
    /**
     * Visit a unary expression
     * @param node - UnaryExpression node
     * @returns Evaluated value as number array
     */
    visitUnaryExpression(node) {
      const operand = this.expectNumberArray(this.visitExpression(node.operand), "unary operand");
      const result = new Array(operand.length);
      for (let i = 0; i < operand.length; i++) {
        result[i] = this.evaluateUnaryOperation(node.operator, operand[i]);
      }
      return result;
    }
    /**
     * Evaluate a unary operation on a number
     * @param operator - Unary operator
     * @param operand - Operand
     * @returns Result of the operation
     */
    evaluateUnaryOperation(operator, operand) {
      switch (operator) {
        case "-" /* Minus */:
          return -operand;
        case "!" /* Not */:
          return this.isTruthy(operand) ? 0 : 1;
        default:
          throw new Error(`Unknown unary operator: ${operator}`);
      }
    }
    /**
     * Visit a function call
     * @param node - FunctionCall node
     * @returns Evaluated value as number array
     */
    visitFunctionCall(node) {
      const functionName = node.name.toUpperCase();
      const args = node.arguments.map((arg) => this.visitExpression(arg));
      const numericArgs = () => args.map((arg, index) => this.expectNumberArray(arg, `${functionName} argument ${index + 1}`));
      switch (functionName) {
        case "MA": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`MA expects 2 arguments, got ${args.length}`);
          }
          const maPeriod = this.getConstantValue(values[1]);
          return MA(values[0], maPeriod);
        }
        case "EMA": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`EMA expects 2 arguments, got ${args.length}`);
          }
          const emaPeriod = this.getConstantValue(values[1]);
          return EMA(values[0], emaPeriod);
        }
        case "SUM": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`SUM expects 2 arguments, got ${args.length}`);
          }
          const sumPeriod = this.getConstantValue(values[1]);
          return SUM(values[0], sumPeriod);
        }
        case "MAX": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`MAX expects 2 arguments, got ${args.length}`);
          }
          return MAX(values[0], values[1]);
        }
        case "MIN": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`MIN expects 2 arguments, got ${args.length}`);
          }
          return MIN(values[0], values[1]);
        }
        case "REF":
        case "REFV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`${functionName} expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_REF(values[0], values[1]);
        }
        case "REFX":
        case "REFXV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`${functionName} expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_REFX(values[0], values[1]);
        }
        case "BACKSET": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`BACKSET expects 2 arguments, got ${args.length}`);
          }
          return BACKSET(values[0], values[1]);
        }
        case "ZIG": {
          const values = numericArgs();
          if (args.length !== 2) throw new Error(`ZIG expects 2 arguments, got ${args.length}`);
          const selector = this.getConstantValue(values[0]);
          const fields = ["OPEN", "HIGH", "LOW", "CLOSE"];
          if (!Number.isInteger(selector) || selector < 0 || selector > 3) throw new Error("ZIG price selector must be 0-3");
          return ZIG(this.context.getMarketDataField(fields[selector]), this.getConstantValue(values[1]));
        }
        case "PEAK":
        case "PEAKBARS":
        case "TROUGH":
        case "TROUGHBARS": {
          const values = numericArgs();
          if (args.length !== 3) throw new Error(`${functionName} expects 3 arguments, got ${args.length}`);
          const selector = this.getConstantValue(values[0]);
          const fields = ["OPEN", "HIGH", "LOW", "CLOSE"];
          if (!Number.isInteger(selector) || selector < 0 || selector > 3) throw new Error(`${functionName} price selector must be 0-3`);
          return ZIG_TURN(
            this.context.getMarketDataField(fields[selector]),
            this.getConstantValue(values[1]),
            this.getConstantValue(values[2]),
            functionName.startsWith("PEAK") ? "peak" : "trough",
            functionName.endsWith("BARS")
          );
        }
        case "HHV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`HHV expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_HHV(values[0], values[1]);
        }
        case "HHVBARS": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`HHVBARS expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_HHVBARS(values[0], values[1]);
        }
        case "LLV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`LLV expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_LLV(values[0], values[1]);
        }
        case "LLVBARS": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`LLVBARS expects 2 arguments, got ${args.length}`);
          }
          return DYNAMIC_LLVBARS(values[0], values[1]);
        }
        case "IF":
        case "IFF": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`${functionName} expects 3 arguments, got ${args.length}`);
          }
          return IF(values[0], values[1], values[2]);
        }
        case "IFN": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`IFN expects 3 arguments, got ${args.length}`);
          }
          return IFN(values[0], values[1], values[2]);
        }
        case "CROSS": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`CROSS expects 2 arguments, got ${args.length}`);
          }
          return CROSS(values[0], values[1]);
        }
        case "LONGCROSS": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`LONGCROSS expects 3 arguments, got ${args.length}`);
          }
          return LONGCROSS(values[0], values[1], this.getConstantValue(values[2]));
        }
        case "ABS": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`ABS expects 1 argument, got ${args.length}`);
          }
          return ABS(values[0]);
        }
        case "SQRT": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`SQRT expects 1 argument, got ${args.length}`);
          }
          return SQRT(values[0]);
        }
        case "POW": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`POW expects 2 arguments, got ${args.length}`);
          }
          return POW(values[0], values[1]);
        }
        case "MOD": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`MOD expects 2 arguments, got ${args.length}`);
          }
          return MOD(values[0], values[1]);
        }
        case "ROUND": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`ROUND expects 1 argument, got ${args.length}`);
          }
          return ROUND(values[0]);
        }
        case "EXP":
        case "LN":
        case "LOG":
        case "CEILING":
        case "FLOOR":
        case "INTPART":
        case "FRACPART":
        case "SIGN":
        case "SIN":
        case "COS":
        case "TAN":
        case "ASIN":
        case "ACOS":
        case "ATAN": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`${functionName} expects 1 argument, got ${args.length}`);
          }
          switch (functionName) {
            case "EXP":
              return EXP(values[0]);
            case "LN":
              return LN(values[0]);
            case "LOG":
              return LOG(values[0]);
            case "CEILING":
              return CEILING(values[0]);
            case "FLOOR":
              return FLOOR(values[0]);
            case "INTPART":
              return INTPART(values[0]);
            case "FRACPART":
              return FRACPART(values[0]);
            case "SIGN":
              return SIGN(values[0]);
            case "SIN":
              return SIN(values[0]);
            case "COS":
              return COS(values[0]);
            case "TAN":
              return TAN(values[0]);
            case "ASIN":
              return ASIN(values[0]);
            case "ACOS":
              return ACOS(values[0]);
            case "ATAN":
              return ATAN(values[0]);
          }
        }
        case "ROUND2": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`ROUND2 expects 2 arguments, got ${args.length}`);
          }
          return ROUND2(values[0], this.getConstantValue(values[1]));
        }
        case "DRAWNULL":
          if (args.length !== 0) {
            throw new Error(`DRAWNULL expects 0 arguments, got ${args.length}`);
          }
          return this.visitNumberLiteral({ type: "NumberLiteral" /* NumberLiteral */, value: NaN });
        case "EVERY": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`EVERY expects 2 arguments, got ${args.length}`);
          }
          return EVERY(values[0], this.getConstantValue(values[1]));
        }
        case "EXIST": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`EXIST expects 2 arguments, got ${args.length}`);
          }
          return EXIST(values[0], this.getConstantValue(values[1]));
        }
        case "BARSLAST": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`BARSLAST expects 1 argument, got ${args.length}`);
          }
          return BARSLAST(values[0]);
        }
        case "BARSLASTCOUNT": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`BARSLASTCOUNT expects 1 argument, got ${args.length}`);
          }
          return BARSLASTCOUNT(values[0]);
        }
        case "COUNT": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`COUNT expects 2 arguments, got ${args.length}`);
          }
          return COUNT(values[0], this.getConstantValue(values[1]));
        }
        case "FILTER": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`FILTER expects 2 arguments, got ${args.length}`);
          }
          return FILTER(values[0], this.getConstantValue(values[1]));
        }
        case "NOT": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`NOT expects 1 argument, got ${args.length}`);
          }
          return NOT(values[0]);
        }
        case "STD": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`STD expects 2 arguments, got ${args.length}`);
          }
          return STD(values[0], this.getConstantValue(values[1]));
        }
        case "VAR": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`VAR expects 2 arguments, got ${args.length}`);
          }
          return VAR(values[0], this.getConstantValue(values[1]));
        }
        case "STDP": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`STDP expects 2 arguments, got ${args.length}`);
          }
          return STDP(values[0], this.getConstantValue(values[1]));
        }
        case "STDDEV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`STDDEV expects 2 arguments, got ${args.length}`);
          }
          return STDDEV(values[0], this.getConstantValue(values[1]));
        }
        case "VARP": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`VARP expects 2 arguments, got ${args.length}`);
          }
          return VARP(values[0], this.getConstantValue(values[1]));
        }
        case "DEVSQ": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`DEVSQ expects 2 arguments, got ${args.length}`);
          }
          return DEVSQ(values[0], this.getConstantValue(values[1]));
        }
        case "FORCAST": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`FORCAST expects 2 arguments, got ${args.length}`);
          }
          return FORCAST(values[0], this.getConstantValue(values[1]));
        }
        case "SLOPE": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`SLOPE expects 2 arguments, got ${args.length}`);
          }
          return SLOPE(values[0], this.getConstantValue(values[1]));
        }
        case "COVAR":
        case "RELATE":
        case "BETA": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`${functionName} expects 3 arguments, got ${args.length}`);
          }
          const period = this.getConstantValue(values[2]);
          if (functionName === "COVAR") {
            return COVAR(values[0], values[1], period);
          }
          if (functionName === "RELATE") {
            return RELATE(values[0], values[1], period);
          }
          return BETA(values[0], values[1], period);
        }
        case "MEDIAN": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`MEDIAN expects 2 arguments, got ${args.length}`);
          }
          return MEDIAN(values[0], this.getConstantValue(values[1]));
        }
        case "AVEDEV": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`AVEDEV expects 2 arguments, got ${args.length}`);
          }
          return AVEDEV(values[0], this.getConstantValue(values[1]));
        }
        case "SMA": {
          const values = numericArgs();
          if (args.length !== 2 && args.length !== 3) {
            throw new Error(`SMA expects 2 or 3 arguments, got ${args.length}`);
          }
          if (args.length === 2) {
            return MA(values[0], this.getConstantValue(values[1]));
          }
          return SMA(
            values[0],
            this.getConstantValue(values[1]),
            this.getConstantValue(values[2])
          );
        }
        case "WMA": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`WMA expects 2 arguments, got ${args.length}`);
          }
          return WMA(values[0], this.getConstantValue(values[1]));
        }
        case "RSI": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`RSI expects 2 arguments, got ${args.length}`);
          }
          return RSI(values[0], this.getConstantValue(values[1]));
        }
        case "DMA": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`DMA expects 2 arguments, got ${args.length}`);
          }
          return DMA(values[0], values[1]);
        }
        case "CONST": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`CONST expects 1 argument, got ${args.length}`);
          }
          return CONST(values[0]);
        }
        // Market data functions
        case "OPEN":
          if (args.length !== 0) {
            throw new Error(`OPEN expects 0 arguments, got ${args.length}`);
          }
          return OPEN.execute([], this.context);
        case "HIGH":
          if (args.length !== 0) {
            throw new Error(`HIGH expects 0 arguments, got ${args.length}`);
          }
          return HIGH.execute([], this.context);
        case "LOW":
          if (args.length !== 0) {
            throw new Error(`LOW expects 0 arguments, got ${args.length}`);
          }
          return LOW.execute([], this.context);
        case "CLOSE":
          if (args.length !== 0) {
            throw new Error(`CLOSE expects 0 arguments, got ${args.length}`);
          }
          return CLOSE.execute([], this.context);
        case "VOL":
          if (args.length !== 0) {
            throw new Error(`VOL expects 0 arguments, got ${args.length}`);
          }
          return VOL.execute([], this.context);
        case "AMOUNT":
          if (args.length !== 0) {
            throw new Error(`AMOUNT expects 0 arguments, got ${args.length}`);
          }
          return AMOUNT.execute([], this.context);
        case "ADVANCE":
          if (args.length !== 0) {
            throw new Error(`ADVANCE expects 0 arguments, got ${args.length}`);
          }
          return ADVANCE.execute([], this.context);
        case "DECLINE":
          if (args.length !== 0) {
            throw new Error(`DECLINE expects 0 arguments, got ${args.length}`);
          }
          return DECLINE.execute([], this.context);
        // DateTime functions
        case "DATE":
          if (args.length !== 0) {
            throw new Error(`DATE expects 0 arguments, got ${args.length}`);
          }
          return DATE(this.context.getMarketDataField("TIMESTAMP"));
        case "TIME":
          if (args.length !== 0) {
            throw new Error(`TIME expects 0 arguments, got ${args.length}`);
          }
          return TIME(this.context.getMarketDataField("TIMESTAMP"));
        case "YEAR":
          if (args.length !== 0) {
            throw new Error(`YEAR expects 0 arguments, got ${args.length}`);
          }
          return YEAR(this.context.getMarketDataField("TIMESTAMP"));
        case "MONTH":
          if (args.length !== 0) {
            throw new Error(`MONTH expects 0 arguments, got ${args.length}`);
          }
          return MONTH(this.context.getMarketDataField("TIMESTAMP"));
        case "DAY":
          if (args.length !== 0) {
            throw new Error(`DAY expects 0 arguments, got ${args.length}`);
          }
          return DAY(this.context.getMarketDataField("TIMESTAMP"));
        case "HOUR":
          if (args.length !== 0) {
            throw new Error(`HOUR expects 0 arguments, got ${args.length}`);
          }
          return HOUR(this.context.getMarketDataField("TIMESTAMP"));
        case "MINUTE":
          if (args.length !== 0) {
            throw new Error(`MINUTE expects 0 arguments, got ${args.length}`);
          }
          return MINUTE(this.context.getMarketDataField("TIMESTAMP"));
        case "WEEKDAY":
          if (args.length !== 0) {
            throw new Error(`WEEKDAY expects 0 arguments, got ${args.length}`);
          }
          return WEEKDAY(this.context.getMarketDataField("TIMESTAMP"));
        // Period functions
        case "PERIOD":
          if (args.length !== 0) {
            throw new Error(`PERIOD expects 0 arguments, got ${args.length}`);
          }
          return PERIOD(this.context.getMarketDataField("TIMESTAMP"));
        case "BARSCOUNT":
          if (args.length !== 0 && args.length !== 1) {
            throw new Error(`BARSCOUNT expects 0 or 1 arguments, got ${args.length}`);
          }
          if (args.length === 1) {
            return BARSCOUNT(this.expectNumberArray(args[0], "BARSCOUNT argument 1"));
          }
          return BARSCOUNT(this.context.getDataLength());
        case "CURRBARSCOUNT":
          if (args.length !== 0) {
            throw new Error(`CURRBARSCOUNT expects 0 arguments, got ${args.length}`);
          }
          return CURRBARSCOUNT(this.context.getDataLength());
        case "TOTALBARSCOUNT":
          if (args.length !== 0) {
            throw new Error(`TOTALBARSCOUNT expects 0 arguments, got ${args.length}`);
          }
          return TOTALBARSCOUNT(this.context.getDataLength());
        case "ISLASTBAR":
          if (args.length !== 0) {
            throw new Error(`ISLASTBAR expects 0 arguments, got ${args.length}`);
          }
          return ISLASTBAR(this.context.getDataLength());
        case "BARSTATUS":
          if (args.length !== 0) {
            throw new Error(`BARSTATUS expects 0 arguments, got ${args.length}`);
          }
          return this.barStatus();
        case "BARSSINCE": {
          const values = numericArgs();
          if (args.length !== 1) {
            throw new Error(`BARSSINCE expects 1 argument, got ${args.length}`);
          }
          return BARSSINCE(values[0]);
        }
        case "SUMBARS": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`SUMBARS expects 2 arguments, got ${args.length}`);
          }
          return SUMBARS(values[0], this.getConstantValue(values[1]));
        }
        // Pattern functions
        case "UPNDAY": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`UPNDAY expects 2 arguments, got ${args.length}`);
          }
          const upndayN = this.getConstantValue(values[1]);
          return UPNDAY(values[0], upndayN);
        }
        case "DOWNNDAY": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`DOWNNDAY expects 2 arguments, got ${args.length}`);
          }
          const downndayN = this.getConstantValue(values[1]);
          return DOWNNDAY(values[0], downndayN);
        }
        case "NDAY": {
          const values = numericArgs();
          if (args.length !== 2 && args.length !== 3) {
            throw new Error(`NDAY expects 2 or 3 arguments, got ${args.length}`);
          }
          if (args.length === 3) {
            return NDAY_AB(values[0], values[1], this.getConstantValue(values[2]));
          }
          const ndayN = this.getConstantValue(values[1]);
          return NDAY(values[0], ndayN);
        }
        case "LAST":
        case "EXISTR": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`${functionName} expects 3 arguments, got ${args.length}`);
          }
          const from = this.getConstantValue(values[1]);
          const to = this.getConstantValue(values[2]);
          if (functionName === "LAST") {
            return LAST(values[0], from, to);
          }
          return EXISTR(values[0], from, to);
        }
        case "RANGE": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`RANGE expects 3 arguments, got ${args.length}`);
          }
          return RANGE(values[0], values[1], values[2]);
        }
        case "BETWEEN": {
          const values = numericArgs();
          if (args.length !== 3) {
            throw new Error(`BETWEEN expects 3 arguments, got ${args.length}`);
          }
          return BETWEEN(values[0], values[1], values[2]);
        }
        // Chip distribution functions
        case "WINNER": {
          const values = numericArgs();
          if (args.length < 3 || args.length > 4) {
            throw new Error(`WINNER expects 3-4 arguments, got ${args.length}`);
          }
          if (args.length === 4) {
            const winnerLookback = this.getConstantValue(values[3]);
            return WINNER(values[0], values[1], values[2], winnerLookback);
          }
          return WINNER(values[0], values[1], values[2]);
        }
        case "LWINNER": {
          const values = numericArgs();
          if (args.length < 3 || args.length > 4) {
            throw new Error(`LWINNER expects 3-4 arguments, got ${args.length}`);
          }
          if (args.length === 4) {
            const lwinnerLookback = this.getConstantValue(values[3]);
            return LWINNER(values[0], values[1], values[2], lwinnerLookback);
          }
          return LWINNER(values[0], values[1], values[2]);
        }
        case "COST": {
          const values = numericArgs();
          if (args.length < 3 || args.length > 4) {
            throw new Error(`COST expects 3-4 arguments, got ${args.length}`);
          }
          if (args.length === 4) {
            const costLookback = this.getConstantValue(values[3]);
            return COST(values[0], values[1], values[2], costLookback);
          }
          return COST(values[0], values[1], values[2]);
        }
        case "VALUEWHEN": {
          const values = numericArgs();
          if (args.length !== 2) {
            throw new Error(`VALUEWHEN expects 2 arguments, got ${args.length}`);
          }
          return VALUEWHEN(values[0], values[1]);
        }
        case "TOPRANGE": {
          const values = numericArgs();
          if (args.length < 1 || args.length > 2) {
            throw new Error(`TOPRANGE expects 1-2 arguments, got ${args.length}`);
          }
          if (args.length === 2) {
            const toprangePeriod = this.getConstantValue(values[1]);
            return TOPRANGE(values[0], toprangePeriod);
          }
          return TOPRANGE(values[0]);
        }
        case "LOWRANGE": {
          const values = numericArgs();
          if (args.length < 1 || args.length > 2) {
            throw new Error(`LOWRANGE expects 1-2 arguments, got ${args.length}`);
          }
          if (args.length === 2) {
            const lowrangePeriod = this.getConstantValue(values[1]);
            return LOWRANGE(values[0], lowrangePeriod);
          }
          return LOWRANGE(values[0]);
        }
        case "DRAWTEXT":
          if (args.length !== 3) {
            throw new Error(`DRAWTEXT expects 3 arguments, got ${args.length}`);
          }
          return this.buildPointDrawings(functionName, args[0], args[1], void 0, args[2]);
        case "DRAWICON":
        case "DRAWNUMBER":
          if (args.length !== 3) {
            throw new Error(`${functionName} expects 3 arguments, got ${args.length}`);
          }
          return this.buildPointDrawings(functionName, args[0], args[1], args[2]);
        case "STICKLINE":
          if (args.length !== 5) {
            throw new Error(`STICKLINE expects 5 arguments, got ${args.length}`);
          }
          return this.buildStickLineDrawings(args);
        case "DRAWLINE":
          if (args.length !== 5) {
            throw new Error(`DRAWLINE expects 5 arguments, got ${args.length}`);
          }
          return this.buildDrawLineEvents(args);
        case "POLYLINE":
          if (args.length !== 2) {
            throw new Error(`POLYLINE expects 2 arguments, got ${args.length}`);
          }
          return this.buildPointDrawings(functionName, args[0], args[1]);
        case "DRAWBAND":
          if (args.length !== 4) {
            throw new Error(`DRAWBAND expects 4 arguments, got ${args.length}`);
          }
          return this.buildFullRangeDrawings(functionName, args, [
            "upper",
            "upperColor",
            "lower",
            "lowerColor"
          ]);
        case "DRAWKLINE":
          if (args.length !== 4) {
            throw new Error(`DRAWKLINE expects 4 arguments, got ${args.length}`);
          }
          return this.buildFullRangeDrawings(functionName, args, ["high", "open", "low", "close"]);
        default:
          throw new Error(`Unknown function: ${node.name}`);
      }
    }
    /**
     * Get a constant value from an array (all elements should be the same)
     * @param arr - Number array
     * @returns The constant value
     */
    getConstantValue(arr) {
      if (arr.length === 0) {
        throw new Error("Cannot get constant value from empty array");
      }
      const firstValue = arr.find((v) => !isNaN(v));
      return firstValue !== void 0 ? firstValue : arr[0];
    }
    isNumberArray(value) {
      return Array.isArray(value) && value.every((item) => typeof item === "number");
    }
    isDrawingEventArray(value) {
      return Array.isArray(value) && value.__formulaDrawingEvents === true;
    }
    markDrawingEvents(drawings) {
      return Object.assign(drawings, { __formulaDrawingEvents: true });
    }
    expectNumberArray(value, label) {
      if (!this.isNumberArray(value)) {
        throw new Error(`${label} must be numeric`);
      }
      return value;
    }
    visitStringLiteral(node) {
      return node.value;
    }
    isTruthy(value) {
      return value !== 0 && !Number.isNaN(value);
    }
    scalarOrArrayAt(value, index) {
      return index < value.length ? value[index] : NaN;
    }
    textValueAt(value, index) {
      if (typeof value === "string") {
        return value;
      }
      if (this.isNumberArray(value)) {
        return String(this.scalarOrArrayAt(value, index));
      }
      throw new Error("drawing text argument must be text or numeric");
    }
    validateDrawingNumericArgs(functionName, length, values) {
      return values.map((value) => {
        const numeric = this.expectNumberArray(value, `${functionName} arguments`);
        if (numeric.length !== length) {
          throw new Error(`${functionName}: array length mismatch`);
        }
        return numeric;
      });
    }
    drawingValueLength(functionName, values) {
      let length = 0;
      for (const value of values) {
        const numeric = this.expectNumberArray(value, `${functionName} arguments`);
        if (length === 0) {
          length = numeric.length;
        } else if (numeric.length !== length) {
          throw new Error(`${functionName}: array length mismatch`);
        }
      }
      return length || this.context.getDataLength();
    }
    buildPointDrawings(functionName, conditionValue, priceValue, numericValue, textValue) {
      const condition = this.expectNumberArray(conditionValue, `${functionName} first argument`);
      const numericArgs = numericValue === void 0 ? [priceValue] : [priceValue, numericValue];
      const [price, numeric] = this.validateDrawingNumericArgs(functionName, condition.length, numericArgs);
      const drawings = [];
      for (let i = 0; i < condition.length; i++) {
        if (!this.isTruthy(condition[i])) {
          continue;
        }
        const event = {
          function: functionName,
          barIndex: i,
          values: {
            price: this.scalarOrArrayAt(price, i)
          }
        };
        if (numeric !== void 0) {
          event.values.value = this.scalarOrArrayAt(numeric, i);
        }
        if (textValue !== void 0) {
          event.text = this.textValueAt(textValue, i);
        }
        drawings.push(event);
      }
      return this.markDrawingEvents(drawings);
    }
    buildStickLineDrawings(args) {
      const condition = this.expectNumberArray(args[0], "STICKLINE first argument");
      const [price1, price2, width, empty] = this.validateDrawingNumericArgs(
        "STICKLINE",
        condition.length,
        args.slice(1)
      );
      const drawings = [];
      for (let i = 0; i < condition.length; i++) {
        if (!this.isTruthy(condition[i])) {
          continue;
        }
        drawings.push({
          function: "STICKLINE",
          barIndex: i,
          values: {
            price1: this.scalarOrArrayAt(price1, i),
            price2: this.scalarOrArrayAt(price2, i),
            width: this.scalarOrArrayAt(width, i),
            empty: this.scalarOrArrayAt(empty, i)
          }
        });
      }
      return this.markDrawingEvents(drawings);
    }
    buildDrawLineEvents(args) {
      const cond1 = this.expectNumberArray(args[0], "DRAWLINE first argument");
      const cond2 = this.expectNumberArray(args[2], "DRAWLINE third argument");
      if (cond1.length !== cond2.length) {
        throw new Error("DRAWLINE: condition array length mismatch");
      }
      const [price1, price2, expand] = this.validateDrawingNumericArgs(
        "DRAWLINE",
        cond1.length,
        [args[1], args[3], args[4]]
      );
      const drawings = [];
      let startIndex = -1;
      let startPrice = NaN;
      for (let i = 0; i < cond1.length; i++) {
        if (this.isTruthy(cond1[i])) {
          startIndex = i;
          startPrice = this.scalarOrArrayAt(price1, i);
        }
        if (startIndex < 0 || !this.isTruthy(cond2[i]) || i < startIndex) {
          continue;
        }
        drawings.push({
          function: "DRAWLINE",
          barIndex: startIndex,
          values: {
            startBar: startIndex,
            startPrice,
            endBar: i,
            endPrice: this.scalarOrArrayAt(price2, i),
            expand: this.scalarOrArrayAt(expand, i)
          }
        });
        startIndex = -1;
        startPrice = NaN;
      }
      return this.markDrawingEvents(drawings);
    }
    buildFullRangeDrawings(functionName, args, keys) {
      const length = this.drawingValueLength(functionName, args);
      const numericArgs = this.validateDrawingNumericArgs(functionName, length, args);
      const drawings = [];
      for (let i = 0; i < length; i++) {
        const values = {};
        for (let j = 0; j < keys.length; j++) {
          values[keys[j]] = this.scalarOrArrayAt(numericArgs[j], i);
        }
        drawings.push({
          function: functionName,
          barIndex: i,
          values
        });
      }
      return this.markDrawingEvents(drawings);
    }
    barStatus() {
      const length = this.context.getDataLength();
      const result = new Array(length).fill(2);
      if (length > 0) {
        result[0] = 1;
        result[length - 1] = length === 1 ? 1 : 3;
      }
      return result;
    }
    /**
     * Visit an identifier
     * @param node - Identifier node
     * @returns Evaluated value as number array
     */
    visitIdentifier(node) {
      const name = node.name;
      const upperName = name.toUpperCase();
      const marketDataFunctionNames = ["OPEN", "HIGH", "LOW", "CLOSE", "VOL", "AMOUNT", "ADVANCE", "DECLINE"];
      if (marketDataFunctionNames.includes(upperName)) {
        switch (upperName) {
          case "OPEN":
            return OPEN.execute([], this.context);
          case "HIGH":
            return HIGH.execute([], this.context);
          case "LOW":
            return LOW.execute([], this.context);
          case "CLOSE":
            return CLOSE.execute([], this.context);
          case "VOL":
            return VOL.execute([], this.context);
          case "AMOUNT":
            return AMOUNT.execute([], this.context);
          case "ADVANCE":
            return ADVANCE.execute([], this.context);
          case "DECLINE":
            return DECLINE.execute([], this.context);
        }
      }
      const datetimeFunctionNames = ["DATE", "TIME", "YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "WEEKDAY"];
      if (datetimeFunctionNames.includes(upperName)) {
        const timestamps = this.context.getMarketDataField("TIMESTAMP");
        switch (upperName) {
          case "DATE":
            return DATE(timestamps);
          case "TIME":
            return TIME(timestamps);
          case "YEAR":
            return YEAR(timestamps);
          case "MONTH":
            return MONTH(timestamps);
          case "DAY":
            return DAY(timestamps);
          case "HOUR":
            return HOUR(timestamps);
          case "MINUTE":
            return MINUTE(timestamps);
          case "WEEKDAY":
            return WEEKDAY(timestamps);
        }
      }
      const periodFunctionNames = ["PERIOD", "BARSCOUNT", "ISLASTBAR"];
      if (periodFunctionNames.includes(upperName)) {
        switch (upperName) {
          case "PERIOD":
            return PERIOD(this.context.getMarketDataField("TIMESTAMP"));
          case "BARSCOUNT":
            return BARSCOUNT(this.context.getDataLength());
          case "ISLASTBAR":
            return ISLASTBAR(this.context.getDataLength());
        }
      }
      if (this.context.isMarketDataField(name)) {
        return this.context.getMarketDataField(name);
      }
      if (this.context.hasVariable(name)) {
        const value = this.context.getVariable(name);
        if (value === void 0) {
          throw new Error(`Variable ${name} is undefined`);
        }
        return value;
      }
      throw new Error(`Undefined identifier: ${name}`);
    }
    /**
     * Visit a number literal
     * @param node - NumberLiteral node
     * @returns Evaluated value as number array (all elements are the same)
     */
    visitNumberLiteral(node) {
      const length = this.context.getDataLength();
      return new Array(length).fill(node.value);
    }
    /**
     * Get the execution context
     * @returns Execution context
     */
    getContext() {
      return this.context;
    }
  };

  // web/vendor/formula-ts/src/interpreter/Context.ts
  var ExecutionContext = class {
    /** Market data arrays for OHLCV */
    marketData;
    /** User-defined variables */
    variables;
    /** Output declarations */
    outputs;
    /** Rendering-agnostic drawing events */
    drawings;
    /** Function registry for built-in functions */
    functionRegistry;
    /**
     * Create a new execution context
     * @param marketData - Array of market data records
     * @param functionRegistry - Registry of available functions
     */
    constructor(marketData, functionRegistry) {
      this.marketData = marketData;
      this.variables = /* @__PURE__ */ new Map();
      this.outputs = /* @__PURE__ */ new Map();
      this.drawings = [];
      this.functionRegistry = functionRegistry;
    }
    /**
     * Get market data array for a specific field
     * @param field - Field name (OPEN, CLOSE, HIGH, LOW, VOLUME, AMOUNT, TIMESTAMP, TRADABLESHARES, ADVANCE, DECLINE)
     * @returns Array of values for the field
     */
    getMarketDataField(field) {
      const upperField = field.toUpperCase();
      switch (upperField) {
        case "OPEN":
        case "O":
          return this.marketData.map((d) => d.open);
        case "CLOSE":
        case "C":
          return this.marketData.map((d) => d.close);
        case "HIGH":
        case "H":
          return this.marketData.map((d) => d.high);
        case "LOW":
        case "L":
          return this.marketData.map((d) => d.low);
        case "VOLUME":
        case "VOL":
        case "V":
          return this.marketData.map((d) => d.volume);
        case "TIMESTAMP":
          return this.marketData.map((d) => d.timestamp);
        case "AMOUNT":
        case "AMO":
          return this.marketData.map((d) => d.amount ?? NaN);
        case "TRADABLESHARES":
          return this.marketData.map((d) => d.tradableShares ?? NaN);
        case "ADVANCE":
          return this.marketData.map((d) => d.advance ?? NaN);
        case "DECLINE":
          return this.marketData.map((d) => d.decline ?? NaN);
        default:
          throw new Error(`Unknown market data field: ${field}`);
      }
    }
    /**
     * Get the length of market data
     * @returns Number of data points
     */
    getDataLength() {
      return this.marketData.length;
    }
    /**
     * Set a variable value
     * @param name - Variable name
     * @param value - Variable value (array)
     */
    setVariable(name, value) {
      this.variables.set(name, value);
    }
    /**
     * Get a variable value
     * @param name - Variable name
     * @returns Variable value (array) or undefined if not found
     */
    getVariable(name) {
      return this.variables.get(name);
    }
    /**
     * Check if a variable exists
     * @param name - Variable name
     * @returns True if variable exists
     */
    hasVariable(name) {
      return this.variables.has(name);
    }
    /**
     * Set an output value
     * @param name - Output name
     * @param value - Output value (array)
     */
    setOutput(name, value) {
      this.outputs.set(name, value);
    }
    /**
     * Append drawing events emitted by a formula function
     * @param drawings - Drawing events to append
     */
    addDrawings(drawings) {
      this.drawings.push(...drawings);
    }
    /**
     * Get an output value
     * @param name - Output name
     * @returns Output value (array) or undefined if not found
     */
    getOutput(name) {
      return this.outputs.get(name);
    }
    /**
     * Get all outputs
     * @returns Map of output names to values
     */
    getOutputs() {
      return this.outputs;
    }
    /**
     * Get all drawing events
     * @returns Rendering-agnostic drawing events
     */
    getDrawings() {
      return this.drawings;
    }
    /**
     * Get all variables
     * @returns Map of variable names to values
     */
    getVariables() {
      return this.variables;
    }
    /**
     * Get the function registry
     * @returns Function registry
     */
    getFunctionRegistry() {
      return this.functionRegistry;
    }
    /**
     * Check if a name is a market data field
     * @param name - Name to check
     * @returns True if it's a market data field
     */
    isMarketDataField(name) {
      const upperName = name.toUpperCase();
      return [
        "OPEN",
        "CLOSE",
        "HIGH",
        "LOW",
        "VOLUME",
        "VOL",
        "V",
        "O",
        "C",
        "H",
        "L",
        "AMO",
        "TIMESTAMP",
        "AMOUNT",
        "TRADABLESHARES",
        "ADVANCE",
        "DECLINE"
      ].includes(upperName);
    }
  };

  // web/vendor/formula-ts/src/interpreter/IncrementalContext.ts
  var IncrementalContext = class extends ExecutionContext {
    /** Cache for intermediate computation results */
    cache;
    /** Starting index for new data points */
    startIndex;
    /** Previous market data length */
    previousDataLength;
    /**
     * Create a new incremental execution context
     * @param marketData - Array of market data records (includes old + new data)
     * @param functionRegistry - Registry of available functions
     * @param startIndex - Index where new data begins (for incremental calculation)
     */
    constructor(marketData, functionRegistry, startIndex = 0) {
      super(marketData, functionRegistry);
      this.cache = /* @__PURE__ */ new Map();
      this.startIndex = startIndex;
      this.previousDataLength = startIndex;
    }
    /**
     * Set a cached value for a computation
     * @param key - Cache key (variable/output name)
     * @param value - Computed value array
     * @param expressionHash - Hash of the expression
     */
    setCachedValue(key, value, expressionHash) {
      this.cache.set(key, {
        value: [...value],
        // Deep copy to prevent mutations
        expressionHash,
        validUpTo: value.length - 1
      });
    }
    /**
     * Get a cached value if valid
     * @param key - Cache key
     * @param expressionHash - Hash of the expression
     * @returns Cached value or undefined if not found/invalid
     */
    getCachedValue(key, expressionHash) {
      const entry = this.cache.get(key);
      if (!entry) {
        return void 0;
      }
      if (entry.expressionHash !== expressionHash) {
        return void 0;
      }
      return [...entry.value];
    }
    /**
     * Check if we should use cached results (incremental mode)
     * @returns True if in incremental mode
     */
    isIncrementalMode() {
      return this.startIndex > 0;
    }
    /**
     * Get the starting index for new calculations
     * @returns Start index for incremental calculations
     */
    getStartIndex() {
      return this.startIndex;
    }
    /**
     * Get the previous data length (before new data was added)
     * @returns Previous data length
     */
    getPreviousDataLength() {
      return this.previousDataLength;
    }
    /**
     * Clear all cached values
     */
    clearCache() {
      this.cache.clear();
    }
    /**
     * Restore variable from cache
     * Useful when reusing computed variables from previous runs
     * @param name - Variable name
     * @param value - Variable value from previous result
     */
    restoreVariable(name, value) {
      this.setVariable(name, value);
    }
    /**
     * Restore output from cache
     * Useful when reusing computed outputs from previous runs
     * @param name - Output name
     * @param value - Output value from previous result
     */
    restoreOutput(name, value) {
      this.setOutput(name, value);
    }
    /**
     * Create a hash for an expression (simple string hash)
     * @param expression - Expression string
     * @returns Hash string
     */
    static hashExpression(expression) {
      let hash = 0;
      for (let i = 0; i < expression.length; i++) {
        const char = expression.charCodeAt(i);
        hash = (hash << 5) - hash + char;
        hash = hash & hash;
      }
      return hash.toString(36);
    }
  };

  // web/vendor/formula-ts/src/interpreter/FunctionRegistry.ts
  var FunctionRegistry = class {
    functions = /* @__PURE__ */ new Map();
    /**
     * Register a new formula function
     * @param fn The formula function to register
     * @throws Error if function is already registered (case-insensitive)
     */
    register(fn) {
      const normalizedName = fn.name.toUpperCase();
      if (this.functions.has(normalizedName)) {
        throw new Error(`Function ${normalizedName} is already registered`);
      }
      this.functions.set(normalizedName, fn);
    }
    /**
     * Get a registered function by name (case-insensitive)
     * @param name The function name
     * @returns The function or undefined if not found
     */
    get(name) {
      const normalizedName = name.toUpperCase();
      return this.functions.get(normalizedName);
    }
    /**
     * Check if a function is registered (case-insensitive)
     * @param name The function name
     * @returns True if function is registered, false otherwise
     */
    has(name) {
      const normalizedName = name.toUpperCase();
      return this.functions.has(normalizedName);
    }
    /**
     * Get all registered function names in uppercase
     * @returns Array of function names
     */
    getAllNames() {
      return Array.from(this.functions.keys());
    }
  };

  // web/vendor/formula-ts/src/FormulaEngine.ts
  var FormulaEngine = class {
    registry;
    /**
     * Create a new FormulaEngine instance
     * @param registry Optional custom function registry
     */
    constructor(registry) {
      this.registry = registry || new FunctionRegistry();
    }
    /**
     * Parse a formula string into an AST
     * @param formula Formula source code
     * @returns Parsed AST program
     * @throws {LexerError} If lexical analysis fails
     * @throws {ParserError} If parsing fails
     */
    parse(formula) {
      const lexer = new Lexer(formula);
      const tokens = lexer.tokenize();
      const parser = new Parser(tokens);
      return parser.parse();
    }
    /**
     * Evaluate a formula with market data
     * @param formula Formula source code
     * @param marketData Array of market data records
     * @returns Formula result containing outputs and variables
     * @throws {LexerError} If lexical analysis fails
     * @throws {ParserError} If parsing fails
     * @throws {RuntimeError} If execution fails
     */
    evaluate(formula, marketData) {
      const ast = this.parse(formula);
      const context = new ExecutionContext(marketData, this.registry);
      const interpreter = new Interpreter(context);
      interpreter.visitProgram(ast);
      const outputs = [];
      const outputsMap = context.getOutputs();
      for (const [name, data] of outputsMap) {
        const statement = ast.body.find(
          (item) => isOutputDeclaration(item) && item.name === name
        );
        outputs.push({
          name,
          data,
          style: statement?.style
        });
      }
      const variables = {};
      const variablesMap = context.getVariables();
      for (const [name, value] of variablesMap) {
        variables[name] = value;
      }
      return {
        outputs,
        variables,
        drawings: context.getDrawings()
      };
    }
    /**
     * Evaluate a formula incrementally with new market data
     * Only calculates values for new data points, reusing previous results
     *
     * @param formula Formula source code (must be the same as previous evaluation)
     * @param newData Complete array of market data (old + new data)
     * @param previousResult Previous formula result to build upon
     * @returns Formula result containing outputs and variables
     * @throws {LexerError} If lexical analysis fails
     * @throws {ParserError} If parsing fails
     * @throws {RuntimeError} If execution fails
     *
     * @example
     * ```typescript
     * // Initial evaluation with 100 data points
     * const result1 = engine.evaluate(formula, marketData.slice(0, 100));
     *
     * // Add 10 new data points and evaluate incrementally
     * const result2 = engine.evaluateIncremental(
     *   formula,
     *   marketData.slice(0, 110),
     *   result1
     * );
     * // Only calculates for the 10 new points, much faster!
     * ```
     */
    evaluateIncremental(formula, newData, previousResult) {
      if (!previousResult || !previousResult.outputs || previousResult.outputs.length === 0) {
        throw new Error("Previous result is required for incremental evaluation");
      }
      const previousLength = previousResult.outputs[0].data.length;
      if (newData.length < previousLength) {
        throw new Error(
          `New data (${newData.length} points) must have at least as many points as previous data (${previousLength} points)`
        );
      }
      if (newData.length === previousLength) {
        return previousResult;
      }
      const ast = this.parse(formula);
      const context = new IncrementalContext(newData, this.registry, previousLength);
      for (const [name, value] of Object.entries(previousResult.variables)) {
        context.restoreVariable(name, value);
      }
      for (const output of previousResult.outputs) {
        context.restoreOutput(output.name, output.data);
      }
      const interpreter = new Interpreter(context);
      interpreter.visitProgram(ast);
      const outputs = [];
      const outputsMap = context.getOutputs();
      for (const [name, data] of outputsMap) {
        const statement = ast.body.find(
          (item) => isOutputDeclaration(item) && item.name === name
        );
        outputs.push({
          name,
          data,
          style: statement?.style
        });
      }
      const variables = {};
      const variablesMap = context.getVariables();
      for (const [name, value] of variablesMap) {
        variables[name] = value;
      }
      return {
        outputs,
        variables,
        drawings: context.getDrawings()
      };
    }
    /**
     * Get the function registry
     * @returns Function registry instance
     */
    getRegistry() {
      return this.registry;
    }
  };

  // web/formula-worker.js
  var MAX_FORMULA_BYTES = 64 * 1024;
  var MAX_BARS = 25e4;
  var MAX_DRAWING_STATEMENTS = 32;
  var MAX_DRAWING_EVENTS = 5e5;
  var MARKET_FIELDS = /* @__PURE__ */ new Set([
    "OPEN",
    "O",
    "HIGH",
    "H",
    "LOW",
    "L",
    "CLOSE",
    "C",
    "VOL",
    "VOLUME",
    "V",
    "AMOUNT",
    "AMO",
    "TIMESTAMP",
    "DATE",
    "TIME",
    "YEAR",
    "MONTH",
    "DAY",
    "HOUR",
    "MINUTE",
    "WEEKDAY",
    "PERIOD",
    "BARSCOUNT",
    "CURRBARSCOUNT",
    "TOTALBARSCOUNT",
    "ISLASTBAR",
    "BARSTATUS",
    "DRAWNULL",
    "ADVANCE",
    "DECLINE",
    "TRADABLESHARES"
  ]);
  var KNOWN_FUNCTIONS = new Set(`
MA EMA SUM MAX MIN ABS SQRT POW EXP LN LOG MOD CEILING FLOOR INTPART FRACPART ROUND ROUND2 SIGN SIN COS TAN ASIN ACOS ATAN
REF REFV REFX REFXV BACKSET ZIG PEAK PEAKBARS TROUGH TROUGHBARS HHV LLV HHVBARS LLVBARS IF IFF IFN CROSS LONGCROSS EVERY EXIST BARSLAST BARSLASTCOUNT COUNT FILTER NOT
STD STDP STDDEV VAR VARP DEVSQ FORCAST SLOPE COVAR RELATE BETA MEDIAN AVEDEV SMA WMA DMA CONST RSI UPNDAY DOWNNDAY NDAY LAST EXISTR RANGE BETWEEN
WINNER LWINNER COST VALUEWHEN TOPRANGE LOWRANGE MACD_DIF MACD_DEA MACD_MACD KDJ_K KDJ_D KDJ_J SAR CCI DMI_PDI DMI_MDI DMI_ADX DMI_ADXR ADX ADXR TRIX OBV BIAS ROC MTM WR PSY
OPEN HIGH LOW CLOSE VOL AMOUNT ADVANCE DECLINE DATE TIME YEAR MONTH DAY HOUR MINUTE WEEKDAY PERIOD BARSCOUNT CURRBARSCOUNT TOTALBARSCOUNT ISLASTBAR BARSTATUS BARSSINCE SUMBARS
DRAWTEXT DRAWICON DRAWNUMBER STICKLINE DRAWLINE POLYLINE DRAWBAND DRAWKLINE
`.trim().split(/\s+/));
  var DRAWING_FUNCTIONS = /* @__PURE__ */ new Set(["DRAWTEXT", "DRAWICON", "DRAWNUMBER", "STICKLINE", "DRAWLINE", "POLYLINE", "DRAWBAND", "DRAWKLINE"]);
  var FUTURE_FUNCTIONS = /* @__PURE__ */ new Set(["REFX", "REFXV", "BACKSET", "ZIG", "PEAK", "PEAKBARS", "TROUGH", "TROUGHBARS"]);
  var EXTERNAL_DATA_FUNCTIONS = /* @__PURE__ */ new Set(["FINANCE", "DYNAINFO", "EXTERNVALUE", "EXTDATA_USER", "GPJYVALUE", "BLOCKSETNUM", "HORCALC"]);
  var RESERVED_WORDS = /* @__PURE__ */ new Set(["AND", "OR", "NOT", "IF", "TRUE", "FALSE"]);
  function splitStatements(source) {
    const statements = [];
    let start = 0;
    let quote = "";
    let comment = false;
    for (let index = 0; index < source.length; index++) {
      const char = source[index];
      if (comment) {
        if (char === "}") comment = false;
        continue;
      }
      if (quote) {
        if (char === "\\") index++;
        else if (char === quote) quote = "";
        continue;
      }
      if (char === "{") comment = true;
      else if (char === "'" || char === '"') quote = char;
      else if (char === ";") {
        const statement = source.slice(start, index).trim();
        if (statement) statements.push(statement);
        start = index + 1;
      }
    }
    const trailing = source.slice(start).trim();
    if (trailing) statements.push(trailing);
    return statements;
  }
  function stripCommentsAndStrings(source) {
    return source.replace(/\{[\s\S]*?\}/g, " ").replace(/\/\/[^\n]*/g, " ").replace(/'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"/g, " ");
  }
  function styleFromSuffix(suffix) {
    const style = {};
    for (const raw of suffix.split(",").map((value) => value.trim().toUpperCase()).filter(Boolean)) {
      if (/^COLOR[0-9A-F]{6}$/.test(raw)) style.color = `#${raw.slice(5)}`;
      else if (raw.startsWith("COLOR")) style.color = raw;
      else if (/^LINETHICK[1-9]$/.test(raw)) style.lineWidth = Number(raw.slice(-1));
      else if (raw === "COLORSTICK" || raw === "VOLSTICK" || raw === "STICK") style.drawMethod = raw;
      else if (raw === "NODRAW") style.hidden = true;
      else if (raw === "DOTLINE" || raw === "DASHLINE") style.lineStyle = raw;
    }
    return style;
  }
  function drawingStatement(statement) {
    const cleaned = statement.replace(/^\s*(?:\{[\s\S]*?\}\s*)+/, "");
    const match = /^([\p{L}_][\p{L}\p{N}_]*)\s*\(/u.exec(cleaned);
    if (!match || !DRAWING_FUNCTIONS.has(match[1].toUpperCase())) return null;
    let quote = "";
    let depth = 0;
    let end = -1;
    for (let index = cleaned.indexOf("("); index < cleaned.length; index++) {
      const char = cleaned[index];
      if (quote) {
        if (char === "\\") index++;
        else if (char === quote) quote = "";
        continue;
      }
      if (char === "'" || char === '"') quote = char;
      else if (char === "(") depth++;
      else if (char === ")" && --depth === 0) {
        end = index;
        break;
      }
    }
    if (end < 0) throw new Error(`${match[1]} \u7F3A\u5C11\u53F3\u62EC\u53F7`);
    const suffix = cleaned.slice(end + 1).trim().replace(/^,/, "");
    return { function: match[1].toUpperCase(), expression: cleaned.slice(0, end + 1), style: styleFromSuffix(suffix) };
  }
  function preprocess(source) {
    if (new TextEncoder().encode(source).length > MAX_FORMULA_BYTES) throw new Error("\u516C\u5F0F\u8D85\u8FC7 64 KiB \u9650\u5236");
    const core = [];
    const drawings = [];
    for (const statement of splitStatements(source)) {
      const drawing = drawingStatement(statement);
      if (drawing) drawings.push(drawing);
      else core.push(statement);
    }
    if (drawings.length > MAX_DRAWING_STATEMENTS) throw new Error("\u7ED8\u56FE\u8BED\u53E5\u4E0D\u80FD\u8D85\u8FC7 32 \u6761");
    return { core: core.map((value) => `${value};`).join("\n"), drawings };
  }
  function analyzeIdentifiers(source) {
    const clean = stripCommentsAndStrings(source);
    const declared = /* @__PURE__ */ new Set();
    for (const match of clean.matchAll(/([\p{L}_][\p{L}\p{N}_]*)\s*(?::=|:(?!=))/gu)) declared.add(match[1].toUpperCase());
    const calls = /* @__PURE__ */ new Set();
    for (const match of clean.matchAll(/([\p{L}_][\p{L}\p{N}_]*)\s*\(/gu)) calls.add(match[1].toUpperCase());
    const external = [...calls].filter((name) => EXTERNAL_DATA_FUNCTIONS.has(name));
    if (external.length) throw new Error(`\u516C\u5F0F\u9700\u8981\u5F53\u524D K \u7EBF\u672A\u63D0\u4F9B\u7684\u5916\u90E8\u6570\u636E\uFF1A${external.join(", ")}\uFF1B\u4E0D\u80FD\u4FDD\u5B58`);
    const unknownFunctions = [...calls].filter((name) => !KNOWN_FUNCTIONS.has(name));
    if (unknownFunctions.length) throw new Error(`\u4E0D\u652F\u6301\u7684\u51FD\u6570\uFF1A${unknownFunctions.join(", ")}`);
    const parameters = /* @__PURE__ */ new Set();
    for (const match of clean.matchAll(/[\p{L}_][\p{L}\p{N}_]*/gu)) {
      const name = match[0].toUpperCase();
      if (declared.has(name) || calls.has(name) || MARKET_FIELDS.has(name) || RESERVED_WORDS.has(name) || name.startsWith("COLOR") || name.startsWith("LINETHICK") || ["NODRAW", "DOTLINE", "DASHLINE", "VOLSTICK", "STICK"].includes(name)) continue;
      parameters.add(name);
    }
    const future = [...calls].filter((name) => FUTURE_FUNCTIONS.has(name));
    return { parameters: [...parameters], warnings: future.length ? [`\u542B\u672A\u6765\u51FD\u6570\uFF1A${future.join(", ")}\uFF1B\u5386\u53F2\u4FE1\u53F7\u53EF\u80FD\u91CD\u7ED8`] : [] };
  }
  function parameterPrefix(parameters) {
    return (parameters || []).map((parameter) => {
      const name = String(parameter.name || "").toUpperCase();
      const value = Number(parameter.value);
      if (!/^[\p{L}_][\p{L}\p{N}_]*$/u.test(name) || !Number.isFinite(value)) throw new Error(`\u53C2\u6570 ${name || "?"} \u65E0\u6548`);
      return `${name}:=${value};`;
    }).join("\n");
  }
  function compile(source, parameters = []) {
    const prepared = preprocess(source);
    const analysis = analyzeIdentifiers(source);
    const supplied = new Set(parameters.map((parameter) => String(parameter.name).toUpperCase()));
    const missing = analysis.parameters.filter((name) => !supplied.has(name));
    const prefix = parameterPrefix(parameters);
    const engine = new FormulaEngine();
    engine.parse(`${prefix}
${prepared.core}`);
    for (const drawing of prepared.drawings) engine.parse(`${prefix}
${prepared.core}
${drawing.expression};`);
    return { prepared, ...analysis, missing };
  }
  function normalizeMarketData(bars) {
    if (!Array.isArray(bars) || bars.length === 0) throw new Error("\u6CA1\u6709\u53EF\u7528\u4E8E\u9884\u89C8\u7684 K \u7EBF\u6570\u636E");
    if (bars.length > MAX_BARS) throw new Error("\u5355\u6B21\u8BA1\u7B97\u4E0D\u80FD\u8D85\u8FC7 250000 \u6839 K \u7EBF");
    return bars.map((bar) => ({
      timestamp: Number(bar.timestamp),
      open: Number(bar.open),
      high: Number(bar.high),
      low: Number(bar.low),
      close: Number(bar.close),
      volume: Number(bar.volume || 0),
      amount: bar.turnover === void 0 || bar.turnover === null ? void 0 : Number(bar.turnover)
    }));
  }
  function sanitizeNumbers(value) {
    if (Array.isArray(value)) return value.map((item) => Number.isFinite(item) ? item : sanitizeNumbers(item));
    if (value && typeof value === "object") {
      const output = {};
      for (const [key, item] of Object.entries(value)) output[key] = sanitizeNumbers(item);
      return output;
    }
    return typeof value === "number" && !Number.isFinite(value) ? null : value;
  }
  function evaluate(source, parameters, bars) {
    const compiled = compile(source, parameters);
    if (compiled.missing.length) throw new Error(`\u8BF7\u914D\u7F6E\u53C2\u6570\uFF1A${compiled.missing.join(", ")}`);
    const prefix = parameterPrefix(parameters);
    const data = normalizeMarketData(bars);
    const engine = new FormulaEngine();
    const coreResult = engine.evaluate(`${prefix}
${compiled.prepared.core}`, data);
    const drawings = [];
    compiled.prepared.drawings.forEach((drawing, statementIndex) => {
      const result = engine.evaluate(`${prefix}
${compiled.prepared.core}
${drawing.expression};`, data);
      for (const item of result.drawings || []) drawings.push({ ...item, statementIndex, style: drawing.style });
      if (drawings.length > MAX_DRAWING_EVENTS) throw new Error("\u7ED8\u56FE\u4E8B\u4EF6\u8D85\u8FC7 500000 \u6761\u9650\u5236");
    });
    return sanitizeNumbers({ outputs: coreResult.outputs, drawings, warnings: compiled.warnings });
  }
  self.onmessage = (event) => {
    const { id, type, formula, parameters, bars } = event.data || {};
    try {
      const result = type === "evaluate" ? evaluate(formula, parameters, bars) : compile(formula, parameters);
      self.postMessage({ id, ok: true, result });
    } catch (error) {
      self.postMessage({ id, ok: false, error: error?.message || String(error) });
    }
  };
})();

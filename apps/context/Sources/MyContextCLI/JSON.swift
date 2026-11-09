import Foundation

/// Retain numeric tokens verbatim, including integers beyond IEEE-754 precision.
indirect enum JSON: Equatable, Sendable {
  case object([String: JSON])
  case array([JSON])
  case string(String)
  case number(String)
  case bool(Bool)
  case null

  init(data: Data) throws {
    var parser = Parser(bytes: Array(data))
    self = try parser.value()
    parser.space()
    guard parser.index == parser.bytes.count else { throw CLIError.usage("Invalid JSON") }
  }

  func rendered(pretty: Bool = false, depth: Int = 0) -> String {
    let indent = pretty ? String(repeating: "  ", count: depth + 1) : ""
    let end = pretty ? "\n" + String(repeating: "  ", count: depth) : ""
    let separator = pretty ? ",\n" : ","
    switch self {
    case .object(let values):
      if values.isEmpty { return "{}" }
      return "{" + (pretty ? "\n" : "")
        + values.keys.sorted().map {
          indent + JSON.string($0).rendered() + (pretty ? ": " : ":")
            + values[$0]!.rendered(pretty: pretty, depth: depth + 1)
        }.joined(separator: separator) + end + "}"
    case .array(let values):
      if values.isEmpty { return "[]" }
      return "[" + (pretty ? "\n" : "")
        + values.map { indent + $0.rendered(pretty: pretty, depth: depth + 1) }.joined(
          separator: separator) + end + "]"
    case .string(let value): return String(data: try! JSONEncoder().encode(value), encoding: .utf8)!
    case .number(let value): return value
    case .bool(let value): return value ? "true" : "false"
    case .null: return "null"
    }
  }

  private struct Parser {
    let bytes: [UInt8]
    var index = 0
    var depth = 0
    mutating func space() {
      while index < bytes.count && [9, 10, 13, 32].contains(bytes[index]) { index += 1 }
    }
    mutating func value() throws -> JSON {
      guard depth < 128 else { throw CLIError.usage("JSON nesting exceeds 128 levels") }
      depth += 1
      defer { depth -= 1 }
      space()
      guard index < bytes.count else { throw CLIError.usage("Invalid JSON") }
      let start = index
      switch bytes[index] {
      case 34:
        index += 1
        while index < bytes.count {
          if bytes[index] == 92 {
            index += 2
            continue
          }
          if bytes[index] == 34 {
            index += 1
            guard
              let string = try? JSONDecoder().decode(String.self, from: Data(bytes[start..<index]))
            else {
              throw CLIError.usage("Invalid JSON string")
            }
            return .string(string)
          }
          index += 1
        }
      case 123:
        index += 1
        space()
        var object: [String: JSON] = [:]
        if take(125) { return .object(object) }
        while true {
          guard case .string(let key) = try value() else { break }
          space()
          guard take(58) else { break }
          object[key] = try value()
          space()
          if take(125) { return .object(object) }
          guard take(44) else { break }
        }
      case 91:
        index += 1
        space()
        var values: [JSON] = []
        if take(93) { return .array(values) }
        while true {
          values.append(try value())
          space()
          if take(93) { return .array(values) }
          guard take(44) else { break }
        }
      default:
        while index < bytes.count && ![9, 10, 13, 32, 44, 93, 125].contains(bytes[index]) {
          index += 1
        }
        let token = String(decoding: bytes[start..<index], as: UTF8.self)
        if token == "true" { return .bool(true) }
        if token == "false" { return .bool(false) }
        if token == "null" { return .null }
        if token.range(
          of: #"^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$"#, options: .regularExpression)
          != nil
        {
          return .number(token)
        }
      }
      throw CLIError.usage("Invalid JSON")
    }
    mutating func take(_ byte: UInt8) -> Bool {
      guard index < bytes.count && bytes[index] == byte else { return false }
      index += 1
      return true
    }
  }
}

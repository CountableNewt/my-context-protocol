import Foundation

enum CLIError: Error {
  case usage(String)
  case request(String)
  case api(Int, JSON)
  case canceled
  var message: String {
    switch self {
    case .usage(let message), .request(let message): return message
    case .api(let status, _): return "API request failed (HTTP \(status))"
    case .canceled: return "Command canceled"
    }
  }
  var exitCode: Int32 {
    switch self {
    case .usage: return 2
    case .api(401, _): return 3
    case .api(403, _): return 4
    case .canceled: return 130
    default: return 1
    }
  }
}

struct Options {
  static let booleans: Set<String> = [
    "help", "version", "no-browser", "session-stdin", "compact", "json",
  ]
  static let values: Set<String> = [
    "url", "web-url", "profile", "config", "project", "id", "release-id", "compiled-skill-id",
    "key-id", "data", "query", "set", "timeout",
  ]
  var words: [String] = []
  var flags: [String: [String]] = [:]
  subscript(_ key: String) -> String { flags[key]?.last ?? "" }
  init(_ args: [String]) throws {
    var index = 0
    while index < args.count {
      defer { index += 1 }
      let arg = args[index] == "-h" ? "--help" : args[index]
      guard arg.hasPrefix("--") else {
        words.append(arg)
        continue
      }
      let pieces = arg.dropFirst(2).split(
        separator: "=", maxSplits: 1, omittingEmptySubsequences: false
      ).map(String.init)
      let key = pieces[0]
      if Self.booleans.contains(key) {
        guard pieces.count == 1 else { throw CLIError.usage("--\(key) does not take a value") }
        flags[key] = ["true"]
        continue
      }
      guard Self.values.contains(key) else { throw CLIError.usage("Unknown flag --\(key)") }
      var value: String
      if pieces.count == 2 {
        value = pieces[1]
      } else {
        index += 1
        guard index < args.count else { throw CLIError.usage("--\(key) requires a value") }
        value = args[index]
      }
      guard key == "query" || key == "set" || flags[key] == nil else {
        throw CLIError.usage("--\(key) may only be specified once")
      }
      flags[key, default: []].append(value)
    }
  }
  static func jsonErrors(_ args: [String]) -> Bool {
    var index = 0
    while index < args.count {
      if args[index] == "--json" { return true }
      let key = String(args[index].dropFirst(2))
      if args[index].hasPrefix("--") && values.contains(key) { index += 1 }
      index += 1
    }
    return false
  }
}

struct Profile: Codable {
  var url = "", webURL = "", session = "", project = ""
  init() {}
  init(from decoder: Decoder) throws {
    let values = try decoder.container(keyedBy: CodingKeys.self)
    url = try values.decodeIfPresent(String.self, forKey: .url) ?? ""
    webURL = try values.decodeIfPresent(String.self, forKey: .webURL) ?? ""
    session = try values.decodeIfPresent(String.self, forKey: .session) ?? ""
    project = try values.decodeIfPresent(String.self, forKey: .project) ?? ""
  }
  private enum CodingKeys: String, CodingKey {
    case url, session, project
    case webURL = "web_url"
  }
}
struct Configuration: Codable {
  var profiles: [String: Profile] = [:]
  init() {}
  private enum CodingKeys: String, CodingKey { case profiles }
  init(from decoder: Decoder) throws {
    let values = try decoder.container(keyedBy: CodingKeys.self)
    profiles = try values.decodeIfPresent([String: Profile].self, forKey: .profiles) ?? [:]
  }
  static func load(_ path: String) throws -> Self {
    guard FileManager.default.fileExists(atPath: path) else { return Self() }
    return try JSONDecoder().decode(Self.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
  }
  func save(_ path: String) throws {
    let file = URL(fileURLWithPath: path)
    let directory = file.deletingLastPathComponent()
    try FileManager.default.createDirectory(
      at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    let temporary = directory.appendingPathComponent(".myctx-\(UUID().uuidString)")
    defer { try? FileManager.default.removeItem(at: temporary) }
    let data = try JSONEncoder().encode(self)
    guard
      FileManager.default.createFile(
        atPath: temporary.path, contents: data, attributes: [.posixPermissions: 0o600])
    else { throw CLIError.request("Unable to write configuration") }
    let handle = try FileHandle(forWritingTo: temporary)
    try handle.synchronize()
    try handle.close()
    guard rename(temporary.path, file.path) == 0 else {
      throw CLIError.request("Unable to replace configuration")
    }
  }
  static func path(_ env: [String: String]) -> String {
    if !environment("CONFIG", env).isEmpty { return environment("CONFIG", env) }
    #if os(macOS)
      let base = URL(fileURLWithPath: env["HOME"] ?? NSHomeDirectory()).appendingPathComponent(
        "Library/Application Support")
    #else
      let base = URL(
        fileURLWithPath: env["XDG_CONFIG_HOME"] ?? (env["HOME"] ?? NSHomeDirectory()) + "/.config")
    #endif
    let primary = base.appendingPathComponent("myctx/config.json").path
    let legacy = base.appendingPathComponent("context/config.json").path
    return !FileManager.default.fileExists(atPath: primary)
      && FileManager.default.fileExists(atPath: legacy) ? legacy : primary
  }
}
func environment(_ key: String, _ env: [String: String]) -> String {
  let branded = env["MYCTX_" + key] ?? ""
  return branded.isEmpty ? env["CONTEXT_" + key] ?? "" : branded
}
func first(_ values: String...) -> String { values.first(where: { !$0.isEmpty }) ?? "" }
func validateOrigin(_ raw: String) throws -> String {
  guard !raw.unicodeScalars.contains(where: { $0.value <= 32 || $0.value == 127 }),
    let url = URLComponents(string: raw), let host = url.host, !host.isEmpty,
    url.user == nil, url.password == nil, url.query == nil, url.fragment == nil,
    url.path.isEmpty || url.path == "/",
    url.scheme == "https"
      || (url.scheme == "http" && ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host))
  else {
    throw CLIError.usage(
      "URL must be an HTTPS API origin without credentials, path, query or fragment (HTTP is allowed for loopback)"
    )
  }
  return raw.hasSuffix("/") ? String(raw.dropLast()) : raw
}

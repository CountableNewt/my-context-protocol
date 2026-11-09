import Foundation
import MyContextCLI

#if canImport(Darwin)
  import Darwin
#else
  import Glibc
#endif

signal(SIGINT, SIG_IGN)
signal(SIGTERM, SIG_IGN)
let task = Task {
  await CLI().main(args: Array(CommandLine.arguments.dropFirst()), version: BuildVersion.value)
}
let interrupt = DispatchSource.makeSignalSource(signal: SIGINT, queue: .global())
let termination = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .global())
interrupt.setEventHandler { task.cancel() }
termination.setEventHandler { task.cancel() }
interrupt.resume()
termination.resume()
let code = await task.value
exit(code)

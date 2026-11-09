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
// Construct handlers outside top-level main-actor isolation: Dispatch invokes
// them on a global queue, and canceling a Sendable Task requires no actor hop.
nonisolated func cancellationSource(for signal: Int32, task: Task<Int32, Never>)
  -> any DispatchSourceSignal
{
  let source = DispatchSource.makeSignalSource(signal: signal, queue: .global())
  source.setEventHandler { @Sendable in task.cancel() }
  source.resume()
  return source
}
let interrupt = cancellationSource(for: SIGINT, task: task)
let termination = cancellationSource(for: SIGTERM, task: task)
let code = await task.value
exit(code)

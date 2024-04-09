# Banan: a simple queued job runner

## Install

Requires Go version ≥ `1.21.7`.

```bash
$ go install github.com/pietv/banan@latest
```

## Execution

**Banan** ([basque](https://translate.google.com/?sl=eu&tl=en&text=banan&op=translate):
*"one by one"*, *"separately"*) executes the command
right away or waits for another instance of that
command to complete (if that command has been run using **banan**).
The command is identified by the name of the executable (for example:
for a command line `bash -c echo 123` the identification name is `bash`).

**Banan** consults the log to check if other instances are being run at the moment.
The name of the log file is the name of the executable minus the file extension.
When the log is written, it is locked using the operating system's file locking
mechanisms.

By default, the log file is created in the `$TMPDIR` directory; that can be overridden
with the `--log-dir` flag.

```bash
$ banan --log-dir=. -- bash -c echo running; sleep 2&
$ banan --log-dir=. -- bash -c echo running; sleep 2&
$ banan --log-dir=. -- bash -c echo running; sleep 2
```

```bash
$ cat bash.log
```

```json
{"time":"2024-04-09 12:12:56 UTC","state":"Processing","pid":45772}
{"time":"2024-04-09 12:12:56 UTC","state":"Done","pid":45772,"system_time":"1.288ms","user_time":"702µs"}
{"time":"2024-04-09 12:12:56 UTC","state":"Processing","pid":45781}
{"time":"2024-04-09 12:12:56 UTC","state":"Done","pid":45781,"system_time":"1.92ms","user_time":"1.119ms"}
{"time":"2024-04-09 12:12:56 UTC","state":"Processing","pid":45790}
{"time":"2024-04-09 12:12:56 UTC","state":"Done","pid":45790,"system_time":"1.874ms","user_time":"997µs"}
```

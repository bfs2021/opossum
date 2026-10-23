The files `opossum up` of release v0.49.0 (commit 86def810) left under `$XDG_STATE_HOME/opossum/<project>/` for a
project with `restart:` and a `configs:` entry, taken from a real `up` against the Go fake runtime and left as they
were: the project is `fixt`, the supervisor it started has since exited (its pid is a process that is not it now).
A later release reads these (TestAStateTheEarlierReleaseWroteIsStillRead). Do not edit them to suit the code; to
follow a newer release, add its own directory beside this one.

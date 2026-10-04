# The lost request, sent again exactly as it was: the guard must refuse it,
# since the statement it named is gone.
ovid delete st:tally.Count:2 --expect fa799c299f8e
exit 0

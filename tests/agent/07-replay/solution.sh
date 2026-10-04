# The lost request, sent again exactly as it was: the guard must refuse it,
# since the statement it named is gone.
ovid delete st:tally.Count:2 --expect adb929245855
exit 0

# Output before a runtime error is kept, and the exit code is 1.
puts "before"
[1, 2].frobnicate
puts "never"

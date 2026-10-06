# Blocks see and assign the enclosing locals; their params shadow them.
total = 0
[1, 2, 3].each { |x| total += x }
puts total

x = 10
[1, 2].each { |x| puts x }
puts x

# A local first assigned inside a block stays inside it.
[1].each { |y| inner = y * 2 }
puts defined_later = 5

# Methods do not see the caller's locals.
def counter(start)
  n = start
  3.times { n += 1 }
  n
end
puts counter(7)

# return inside a block returns from the method.
def first_even(a)
  a.each do |v|
    return v if v.even?
  end
  nil
end
p first_even([3, 5, 8, 9, 10])
p first_even([1, 3])

# yield passes values to the caller's block and gets its result back.
def twice
  [yield(1), yield(2)]
end
p twice { |v| v * 100 }

# break leaves only the innermost iteration.
(1..3).each do |i|
  (1..3).each do |j|
    break if j > i
    print i * 10 + j, " "
  end
end
puts

n = 0
loop do
  n += 1
  next if n.odd?
  break if n > 6
  print n, " "
end
puts

# A Ruby program run by an interpreter written in Ovid.
def fib(n)
  return n if n < 2
  fib(n - 1) + fib(n - 2)
end

def greet(name)
  "Hello, #{name.capitalize}!"
end

puts greet("ovid")
puts "fib(20) = #{fib(20)}"

squares = (1..10).map { |x| x * x }
puts "squares: #{squares.inspect}"
puts "even squares sum: #{squares.select { |x| x.even? }.sum}"

words = "the quick brown fox jumps over the lazy dog".split
puts words.map { |w| w.upcase }.join(" ")
puts "longest: #{words.reduce { |a, b| a.length >= b.length ? a : b }}"
puts words.uniq.sort.inspect

3.times do |i|
  print i, " "
end
puts

i = 0
until i >= 5
  i += 1
  next if i == 2
  break if i == 4
  puts "i=#{i}"
end

def each_indexed(a)
  a.each_with_index do |x, idx|
    yield x, idx if block_given?
  end
end
each_indexed([10, 20, 30]) { |x, idx| puts "#{idx}: #{x}" }

fizz = (1..15).map do |n|
  if n % 15 == 0 then "FizzBuzz"
  elsif n % 3 == 0 then "Fizz"
  elsif n % 5 == 0 then "Buzz"
  else n.to_s
  end
end
puts fizz.join(",")

h = []
h << 3 << 1 << 2
h[5] = 9
p h
puts -7 / 2, -7 % 3, 2 ** 10
puts "%d items, %s" % [3, "ok"]

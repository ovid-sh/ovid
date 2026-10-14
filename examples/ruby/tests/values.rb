# Strings
s = "Hello, World"
puts s.length, s.upcase, s.downcase, s.reverse
puts s.include?("World"), s.start_with?("Hell"), s.end_with?("x")
p s[0], s[-1], s[99]
p "  padded  ".strip, "a,b,,c".split(","), "x" * 3
p "42".to_i + 1, "-7 apples".to_i, 65.chr
t = "abc"
t << "def"
p t
p "tab\there", "quote\"", 'single #{not interpolated}'
puts "#{1 + 2} and #{"nested #{3 * 3}"}"

# Integers: floor division and modulo, like Ruby
p 7 / 2, -7 / 2, 7 / -2, 7 % 3, -7 % 3, 7 % -3
p 2 ** 0, 3 ** 4, 10.abs, -10.abs, 1234.digits
p 5.zero?, 0.zero?, 3.even?, -4.even?

# Arrays
a = [5, 3, 8, 1]
p a.sort, a.min, a.max, a.sum, a.reverse, a.length
p a.map { |v| v * 2 }.select { |v| v > 5 }.reject { |v| v == 16 }
p a.include?(8), a.include?(9), a.first, a.last, [].first
p a.count { |v| v.odd? }, a.any? { |v| v > 7 }, a.all? { |v| v > 0 }
p a.find { |v| v > 4 }, a.reduce(100) { |acc, v| acc - v }
p [1, [2, 3]], [1, 1, 2].uniq, [1, 2] + [3], a.pop, a
b = []
b.push(1, 2)
b[4] = 5
p b, b[-1], b.join("-")
a.each_with_index { |v, i| print i, "=", v, " " }
puts
puts [1, [2, [3]]]

# Truthiness, nil, ranges
p nil.nil?, 0.nil?, nil.to_s, nil.inspect
puts "0 is truthy" if 0
puts "empty string is truthy" if ""
p (1..5).to_a, (1..5).include?(3), (1..4).sum
p 1 == 1, "a" == "a", [1, [2]] == [1, [2]], "a" != "b", "abc" < "abd"
p true && "yes", nil || "default", !nil, (not true)
# classes are not objects here; puts prints the same name
puts 3.class, "s".class, [].class, nil.class
